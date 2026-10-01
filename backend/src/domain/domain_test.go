package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseMoney(t *testing.T) {
	tests := []struct {
		in      string
		want    Money
		wantStr string
		wantErr bool
	}{
		{"0", 0, "0.00", false},
		{"2999", 299900, "2999.00", false},
		{"2999.5", 299950, "2999.50", false},
		{"0.01", 1, "0.01", false},
		{" 12.30 ", 1230, "12.30", false},
		{"99999999.99", 9999999999, "99999999.99", false},
		{"1.005", 0, "", true}, // 超过两位小数不四舍五入，直接拒绝
		{"-1", 0, "", true},
		{"", 0, "", true},
		{"abc", 0, "", true},
		{"1e3", 0, "", true},
		{".5", 0, "", true},
		{"1.2.3", 0, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseMoney(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseMoney(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil || got != tt.want || got.String() != tt.wantStr {
				t.Fatalf("ParseMoney(%q) = %v (%s), %v; want %v (%s)", tt.in, int64(got), got, err, int64(tt.want), tt.wantStr)
			}
		})
	}
}

func TestMoneyArithmeticIsExact(t *testing.T) {
	// float 计算 0.1*3 会得到 0.30000000000000004，定点金额不会。
	if got := MustMoney("0.10").Mul(3); got.String() != "0.30" {
		t.Fatalf("0.10 * 3 = %s", got)
	}
	total := MustMoney("2999.00").Mul(2) + MustMoney("129.00") - MustMoney("30.00")
	if total.String() != "6097.00" {
		t.Fatalf("total = %s", total)
	}
	if neg := MustMoney("1.00") - MustMoney("1.50"); neg.String() != "-0.50" {
		t.Fatalf("negative format = %s", neg)
	}
}

func TestMoneyJSONAndScan(t *testing.T) {
	b, _ := json.Marshal(struct {
		Price Money `json:"price"`
	}{MustMoney("2999")})
	if string(b) != `{"price":"2999.00"}` {
		t.Fatalf("marshal = %s", b)
	}
	for _, in := range []string{`"12.3"`, `12.3`, `"12.30"`} {
		var m Money
		if err := json.Unmarshal([]byte(in), &m); err != nil || m.String() != "12.30" {
			t.Fatalf("unmarshal %s = %s, %v", in, m, err)
		}
	}
	for _, bad := range []string{`"-1"`, `"1.234"`, `true`, `null`} {
		var m Money
		if err := json.Unmarshal([]byte(bad), &m); err == nil && bad != `null` {
			t.Fatalf("unmarshal %s should fail", bad)
		}
	}

	var m Money
	for _, src := range []any{[]byte("88.50"), "88.50"} {
		if err := m.Scan(src); err != nil || m.String() != "88.50" {
			t.Fatalf("scan %v = %s, %v", src, m, err)
		}
	}
	if err := m.Scan(nil); err == nil {
		t.Fatal("scan NULL should fail")
	}
	if v, _ := MustMoney("5").Value(); v != "5.00" {
		t.Fatalf("Value = %v", v)
	}
}

// TestRateRangeOnEveryEntry 确认 [0, 1] 的约束在解析、JSON 读入、数据库读出、写入数据库四个入口一致生效。
func TestRateRangeOnEveryEntry(t *testing.T) {
	tests := []struct {
		in    string
		ok    bool
		canon string
	}{
		{"0", true, "0.0000"},
		{"0.95", true, "0.9500"},
		{"0.0001", true, "0.0001"},
		{"1", true, "1.0000"},
		{"1.0000", true, "1.0000"},
		{"1.0001", false, ""},
		{"1.5", false, ""},
		{"9.9999", false, ""}, // DECIMAL(5,4) 的上限也必须被拒绝
		{"-0.1", false, ""},
		{"-0.0001", false, ""},
		{"0.12345", false, ""}, // 超过 4 位小数
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			check := func(entry string, got Rate, err error) {
				t.Helper()
				if tt.ok && (err != nil || got.String() != tt.canon) {
					t.Errorf("%s(%q) = %s, %v; want %s", entry, tt.in, got, err, tt.canon)
				}
				if !tt.ok && err == nil {
					t.Errorf("%s(%q) = %s; want error", entry, tt.in, got)
				}
			}

			r, err := ParseRate(tt.in)
			check("ParseRate", r, err)

			for _, raw := range []string{`"` + tt.in + `"`, tt.in} {
				var j Rate
				check("UnmarshalJSON "+raw, j, json.Unmarshal([]byte(raw), &j))
				if !tt.ok && j != 0 {
					t.Errorf("failed UnmarshalJSON(%s) must not modify the value, got %s", raw, j)
				}
			}

			var sc Rate
			check("Scan", sc, sc.Scan([]byte(tt.in)))
		})
	}

	// 写库前校验：越界值即使绕过解析直接构造，也写不进数据库。
	for _, r := range []Rate{-1, 10001, 99999} {
		if _, err := r.Value(); err == nil {
			t.Errorf("Rate(%d).Value() should fail", int64(r))
		}
	}
	if v, err := MustRate("0.88").Value(); err != nil || v != "0.8800" {
		t.Errorf("Value = %v, %v", v, err)
	}
	b, _ := json.Marshal(MustRate("0.95"))
	if string(b) != `"0.9500"` {
		t.Errorf("marshal = %s", b)
	}
}

func TestStateMachines(t *testing.T) {
	type tc struct {
		name string
		err  error
		ok   bool
	}
	tests := []tc{
		{"order pending->paid", OrderPendingPayment.CanTransitionTo(OrderPaid), true},
		{"order pending->cancelled", OrderPendingPayment.CanTransitionTo(OrderCancelled), true},
		{"order paid->shipped", OrderPaid.CanTransitionTo(OrderShipped), true},
		{"order shipped->completed", OrderShipped.CanTransitionTo(OrderCompleted), true},
		{"order paid->cancelled forbidden", OrderPaid.CanTransitionTo(OrderCancelled), false},
		{"order pending->shipped forbidden", OrderPendingPayment.CanTransitionTo(OrderShipped), false},
		{"order completed is final", OrderCompleted.CanTransitionTo(OrderPaid), false},
		{"order cancelled is final", OrderCancelled.CanTransitionTo(OrderPendingPayment), false},
		{"payment pending->paid", PaymentPending.CanTransitionTo(PaymentPaid), true},
		{"payment pending->closed", PaymentPending.CanTransitionTo(PaymentClosed), true},
		{"payment paid->closed forbidden", PaymentPaid.CanTransitionTo(PaymentClosed), false},
		{"account active->inactive", StatusActive.CanTransitionTo(StatusInactive), true},
		{"account inactive->active", StatusInactive.CanTransitionTo(StatusActive), true},
		{"account any->risk", StatusInactive.CanTransitionTo(StatusRisk), true},
		{"account same state is not a transition", StatusActive.CanTransitionTo(StatusActive), false},
		{"product active->deleted", ProductActive.CanTransitionTo(ProductDeleted), true},
		{"product risk->deleted", ProductRisk.CanTransitionTo(ProductDeleted), true},
		{"product deleted is final", ProductDeleted.CanTransitionTo(ProductActive), false},
		{"run queued->running", RunQueued.CanTransitionTo(RunRunning), true},
		{"run running->cancelled", RunRunning.CanTransitionTo(RunCancelled), true},
		{"run completed is final", RunCompleted.CanTransitionTo(RunCancelled), false},
		{"run queued->completed forbidden", RunQueued.CanTransitionTo(RunCompleted), false},
		{"doc uploaded->parsing", DocUploaded.CanTransitionTo(DocParsing), true},
		{"doc indexing->indexed", DocIndexing.CanTransitionTo(DocIndexed), true},
		{"doc failed->parsing (retry)", DocFailed.CanTransitionTo(DocParsing), true},
		{"doc uploaded->indexed forbidden", DocUploaded.CanTransitionTo(DocIndexed), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if (tt.err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", tt.err, tt.ok)
			}
			if !tt.ok {
				var invalid *ErrInvalidTransition
				if !errors.As(tt.err, &invalid) {
					t.Fatalf("error type = %T, want *ErrInvalidTransition", tt.err)
				}
			}
		})
	}
}

func TestStockStatusOf(t *testing.T) {
	cases := map[int]StockStatus{-1: StockOutOfStock, 0: StockOutOfStock, 1: StockLow, 10: StockLow, 11: StockInStock}
	for qty, want := range cases {
		if got := StockStatusOf(qty); got != want {
			t.Errorf("StockStatusOf(%d) = %s, want %s", qty, got, want)
		}
	}
}

func TestSensitiveFieldsNeverSerialized(t *testing.T) {
	b, _ := json.Marshal(AuthToken{TokenHash: "deadbeef", AccountID: "acct_1"})
	if strings.Contains(string(b), "deadbeef") {
		t.Fatalf("token hash serialized: %s", b)
	}
	b, _ = json.Marshal(StoredFile{FileID: "file_1", ObjectKey: "private/key"})
	if strings.Contains(string(b), "private/key") {
		t.Fatalf("object key serialized: %s", b)
	}
	cfg := AppConfig{Key: "ai.api_key", Value: "sk-live", Secret: true}.Masked()
	if cfg.Value != SecretMask {
		t.Fatalf("secret not masked: %+v", cfg)
	}
	if plain := (AppConfig{Key: "x", Value: "1"}).Masked(); plain.Value != "1" {
		t.Fatalf("non-secret masked: %+v", plain)
	}
	if empty := (AppConfig{Key: "k", Secret: true}).Masked(); empty.Value != "" {
		t.Fatalf("unset secret should stay empty: %+v", empty)
	}
}

func TestNewID(t *testing.T) {
	a, b := NewID(PrefixOrder), NewID(PrefixOrder)
	if !strings.HasPrefix(a, "o_") || len(a) != len("o_")+24 || a == b {
		t.Fatalf("NewID = %q, %q", a, b)
	}
}
