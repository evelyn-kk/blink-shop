package mysqlstore

import (
	"bufio"
	"strings"
	"testing"

	"github.com/evelyn-kk/blink-shop/backend/migrations"
)

// TestMigrationsFrozen：migrations/checksums.txt 记录已发布迁移的 sha256。已发布的迁移文件不能修改或删除；
// 新迁移必须登记（在末尾追加一行），这样改动迁移会在 CI 里直接失败，而不是等到上线时 Migrate 校验 checksum 才报错。
func TestMigrationsFrozen(t *testing.T) {
	raw, err := migrations.FS.ReadFile("checksums.txt")
	if err != nil {
		t.Fatal(err)
	}
	frozen := map[string]string{}
	var order []string
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			t.Fatalf("bad line %q", line)
		}
		frozen[f[1]] = f[0]
		order = append(order, f[1])
	}
	ms, err := LoadMigrations(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i, m := range ms {
		seen[m.Name] = true
		sum, ok := frozen[m.Name]
		if !ok {
			t.Errorf("%s 没有登记到 migrations/checksums.txt（新迁移在末尾追加一行）", m.Name)
			continue
		}
		if sum != m.Checksum {
			t.Errorf("%s 已发布，不能修改（写一个新的迁移）", m.Name)
		}
		if i < len(order) && order[i] != m.Name {
			t.Errorf("checksums.txt 第 %d 项是 %s，应按版本顺序是 %s", i+1, order[i], m.Name)
		}
	}
	for name := range frozen {
		if !seen[name] {
			t.Errorf("已发布的迁移 %s 被删除", name)
		}
	}
}
