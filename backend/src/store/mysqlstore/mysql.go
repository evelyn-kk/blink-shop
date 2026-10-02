// Package mysqlstore 是 store.Store 的 MySQL 实现。所有 SQL 都使用参数占位符。
package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/evelyn-kk/blink-shop/backend/src/store"
)

// Store 实现 store.Store。
type Store struct {
	db  *sql.DB
	cfg *mysql.Config
	now func() time.Time
	// txRetries 记录因唯一键冲突或死锁而重试的事务次数，用于测试确认正常并发下不需要重试。
	txRetries atomic.Int64
}

// TxRetries 返回累计的事务重试次数。
func (s *Store) TxRetries() int64 { return s.txRetries.Load() }

var _ store.Store = (*Store)(nil)

// Open 按 DSN 建立连接池（不立即连接）。无论 DSN 怎么写，都强制：解析时间、UTC 时区、utf8mb4。
func Open(dsn string) (*Store, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("MYSQL_DSN 格式不正确")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.Collation = "utf8mb4_0900_ai_ci"
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	cfg.MultiStatements = false

	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, errors.New("MYSQL_DSN 配置不可用")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	return &Store{db: db, cfg: cfg, now: func() time.Time { return time.Now().UTC() }}, nil
}

// DB 暴露连接池，供健康检查和测试使用。
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// SetClock 仅供测试固定时间。
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// timestamp 返回截断到毫秒的 UTC 时间，与 DATETIME(3) 精度一致，保证写入值和读回值相等。
func (s *Store) timestamp() time.Time { return s.now().UTC().Truncate(time.Millisecond) }

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type txKey struct{ s *Store }

// q 返回当前 context 中属于本 Store 的事务；没有则返回连接池。
func (s *Store) q(ctx context.Context) querier {
	if tx, ok := ctx.Value(txKey{s}).(*sql.Tx); ok {
		return tx
	}
	return s.db
}

// WithTx 在事务中执行 fn；fn 返回错误或 panic 时回滚。已在事务中时直接复用。
func (s *Store) WithTx(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	if _, ok := ctx.Value(txKey{s}).(*sql.Tx); ok {
		return fn(ctx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
			return
		}
		if cerr := tx.Commit(); cerr != nil {
			err = fmt.Errorf("commit tx: %w", cerr)
		}
	}()
	return fn(context.WithValue(ctx, txKey{s}, tx))
}

var duplicateKeyPattern = regexp.MustCompile(`for key '(?:[^.']+\.)?([^']+)'`)

// mapErr 把驱动错误转换为 store 的可识别错误。
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) && myErr.Number == 1062 {
		key := "unknown"
		if m := duplicateKeyPattern.FindStringSubmatch(myErr.Message); m != nil {
			key = m[1]
		}
		return &store.ConflictError{Key: key}
	}
	return err
}

// jsonArray 序列化 NOT NULL 的数组列；nil 切片写成 []。
func jsonArray[T any](v []T) (string, error) {
	if v == nil {
		return "[]", nil
	}
	b, err := json.Marshal(v)
	return string(b), err
}

// jsonObject 序列化 NOT NULL 的对象列；nil map 写成 {}。
func jsonObject[V any](v map[string]V) (string, error) {
	if v == nil {
		return "{}", nil
	}
	b, err := json.Marshal(v)
	return string(b), err
}

// nullableJSON 用于允许 NULL 的 JSON 列：空值写 NULL。
func nullableJSON[T any](v T) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if s := string(b); s == "null" || s == "{}" || s == "[]" {
		return nil, nil
	}
	return string(b), nil
}

// fromJSON 解析 JSON 列；结构不符合时返回带列名的错误，而不是静默得到零值。
func fromJSON(column string, raw []byte, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("列 %s 的 JSON 结构不合法: %w", column, err)
	}
	return nil
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

func timePtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	t := nt.Time.UTC()
	return &t
}

// orNow 返回 t（非零时）或当前时间，均截断到毫秒。
func (s *Store) orNow(t time.Time) time.Time {
	if t.IsZero() {
		return s.timestamp()
	}
	return t.UTC().Truncate(time.Millisecond)
}
