package mysqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// Migration 是一个版本化迁移文件。
type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// LoadMigrations 读取并按版本排序迁移文件；文件名不规范或版本重复都会报错。
func LoadMigrations(files fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue // README、embed.go 等非 SQL 文件
		}
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("迁移文件名不规范: %s（应为 NNNN_描述.sql）", e.Name())
		}
		version, _ := strconv.Atoi(m[1])
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("迁移版本 %04d 重复: %s 与 %s", version, prev, e.Name())
		}
		seen[version] = e.Name()
		body, err := fs.ReadFile(files, e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		out = append(out, Migration{Version: version, Name: e.Name(), SQL: string(body), Checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

const (
	migrationLock        = "blink_shop_schema_migrations"
	migrationLockSeconds = 60
)

// PendingMigrations 返回尚未应用的迁移数量；schema_migrations 不存在时视为全部未应用。
// 数据库里有本程序不认识的更新版本迁移（新版本已升级、旧版本回滚运行）时不计入，旧程序照常就绪。
func (s *Store) PendingMigrations(ctx context.Context, files fs.FS) (int, error) {
	_, pending, _, err := s.MigrationPlan(ctx, files)
	return len(pending), err
}

// MigrationPlan 返回已应用、待执行的迁移文件名，以及数据库里有、本程序没有的版本（说明数据库比程序新，例如回滚部署）。
func (s *Store) MigrationPlan(ctx context.Context, files fs.FS) (applied, pending []string, unknown []int, err error) {
	migrations, err := LoadMigrations(files)
	if err != nil {
		return nil, nil, nil, err
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'schema_migrations'`).Scan(&exists); err != nil {
		return nil, nil, nil, err
	}
	done := map[int]bool{}
	if exists > 0 {
		rows, err := s.db.QueryContext(ctx, "SELECT version FROM schema_migrations ORDER BY version")
		if err != nil {
			return nil, nil, nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				return nil, nil, nil, err
			}
			done[v] = true
		}
		if err := rows.Err(); err != nil {
			return nil, nil, nil, err
		}
	}
	known := map[int]bool{}
	for _, m := range migrations {
		known[m.Version] = true
		if done[m.Version] {
			applied = append(applied, m.Name)
		} else {
			pending = append(pending, m.Name)
		}
	}
	for v := range done {
		if !known[v] {
			unknown = append(unknown, v)
		}
	}
	sort.Ints(unknown)
	return applied, pending, unknown, nil
}

// 不会创建数据库本身，数据库必须事先存在。
func (s *Store) Migrate(ctx context.Context, files fs.FS) ([]string, error) {
	migrations, err := LoadMigrations(files)
	if err != nil {
		return nil, err
	}

	// 迁移文件包含多条语句，单独开一个允许 multiStatements 的连接，业务连接池不开启。
	cfg := s.cfg.Clone()
	cfg.MultiStatements = true
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	defer conn.Close()

	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", migrationLock, migrationLockSeconds).Scan(&got); err != nil {
		return nil, fmt.Errorf("获取迁移锁失败: %w", err)
	}
	if got.Int64 != 1 {
		return nil, errors.New("获取迁移锁超时：可能有其他实例正在迁移")
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK(?)", migrationLock) //nolint:errcheck

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INT          NOT NULL,
  name       VARCHAR(255) NOT NULL,
  checksum   CHAR(64)     NOT NULL,
  applied_at DATETIME(3)  NOT NULL,
  PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
		return nil, fmt.Errorf("创建 schema_migrations 失败: %w", err)
	}

	applied := map[int]string{}
	rows, err := conn.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			rows.Close()
			return nil, err
		}
		applied[v] = sum
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	known := map[int]bool{}
	for _, m := range migrations {
		known[m.Version] = true
		if sum, ok := applied[m.Version]; ok && sum != m.Checksum {
			return nil, fmt.Errorf("迁移 %s 在应用后被修改（checksum 不一致）；已发布的迁移只能追加新版本", m.Name)
		}
	}
	for v := range applied {
		if !known[v] {
			return nil, fmt.Errorf("数据库已应用版本 %04d，但代码中没有该迁移；请升级代码后再启动", v)
		}
	}

	var done []string
	for _, m := range migrations {
		if _, ok := applied[m.Version]; ok {
			continue
		}
		if _, err := conn.ExecContext(ctx, m.SQL); err != nil {
			return done, fmt.Errorf("执行迁移 %s 失败: %w", m.Name, err)
		}
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)",
			m.Version, m.Name, m.Checksum, s.timestamp()); err != nil {
			return done, fmt.Errorf("记录迁移 %s 失败: %w", m.Name, err)
		}
		done = append(done, m.Name)
	}
	return done, nil
}
