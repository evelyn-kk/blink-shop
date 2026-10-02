// Package domain 定义跨层共享的领域类型、角色和状态；不依赖任何基础设施。
package domain

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Money 是以“分”为单位的定点金额，对应 MySQL DECIMAL(10,2)。金额计算一律使用 Money，禁止 float。
// JSON 序列化为两位小数的字符串（如 "2999.00"），与上游接口保持一致。
type Money int64

const moneyScale = 2

// ParseMoney 解析 "2999"、"2999.5"、"2999.50"；超过两位小数或负数返回错误。
func ParseMoney(s string) (Money, error) {
	v, err := parseFixed(s, moneyScale)
	if err != nil {
		return 0, fmt.Errorf("金额 %q 不合法: %w", s, err)
	}
	if v < 0 {
		return 0, fmt.Errorf("金额 %q 不能为负数", s)
	}
	return Money(v), nil
}

// MustMoney 用于常量和种子数据，解析失败直接 panic。
func MustMoney(s string) Money {
	m, err := ParseMoney(s)
	if err != nil {
		panic(err)
	}
	return m
}

func (m Money) String() string { return formatFixed(int64(m), moneyScale) }

// Mul 计算单价 × 数量。
func (m Money) Mul(quantity int) Money { return m * Money(quantity) }

// MulRate 计算金额 × 比例，四舍五入到分（例如 99.99 × 0.95 = 94.9905 → 94.99）。比例必须在 [0, 1]。
func (m Money) MulRate(r Rate) Money {
	return Money((int64(m)*int64(r) + int64(rateOne)/2) / int64(rateOne))
}

func (m Money) MarshalJSON() ([]byte, error) { return json.Marshal(m.String()) }

// UnmarshalJSON 接受字符串 "12.30" 或数字 12.3；负数视为非法。
func (m *Money) UnmarshalJSON(b []byte) error {
	var v int64
	if err := unmarshalFixed(b, moneyScale, &v); err != nil {
		return err
	}
	if v < 0 {
		return fmt.Errorf("金额不能为负数")
	}
	*m = Money(v)
	return nil
}

// Value 写库时使用十进制字符串，避免浮点误差。
func (m Money) Value() (driver.Value, error) { return m.String(), nil }

func (m *Money) Scan(src any) error {
	v, err := scanFixed(src, moneyScale)
	if err != nil {
		return fmt.Errorf("scan money: %w", err)
	}
	*m = Money(v)
	return nil
}

// Rate 是 4 位小数的比例（如折扣率 0.9500），对应 MySQL DECIMAL(5,4)。
// 合法范围是 [0, 1]：解析、JSON 读入、数据库读出、写入数据库四个入口都用 Validate 校验，
// 数据库层另有 CHECK 约束（0002 迁移）兜底。
type Rate int64

const (
	rateScale = 4
	rateOne   = Rate(10000) // 1.0000
)

// Validate 检查比例是否在 [0, 1] 内。
func (r Rate) Validate() error {
	if r < 0 || r > rateOne {
		return fmt.Errorf("比例 %s 必须在 0 到 1 之间", r)
	}
	return nil
}

func ParseRate(s string) (Rate, error) {
	v, err := parseFixed(s, rateScale)
	if err != nil {
		return 0, fmt.Errorf("比例 %q 不合法: %w", s, err)
	}
	if err := Rate(v).Validate(); err != nil {
		return 0, err
	}
	return Rate(v), nil
}

func MustRate(s string) Rate {
	r, err := ParseRate(s)
	if err != nil {
		panic(err)
	}
	return r
}

func (r Rate) String() string               { return formatFixed(int64(r), rateScale) }
func (r Rate) MarshalJSON() ([]byte, error) { return json.Marshal(r.String()) }

// Value 写库前校验，越界的比例不会被写入。
func (r Rate) Value() (driver.Value, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r.String(), nil
}

func (r *Rate) UnmarshalJSON(b []byte) error {
	var v int64
	if err := unmarshalFixed(b, rateScale, &v); err != nil {
		return err
	}
	if err := Rate(v).Validate(); err != nil {
		return err
	}
	*r = Rate(v)
	return nil
}

func (r *Rate) Scan(src any) error {
	v, err := scanFixed(src, rateScale)
	if err != nil {
		return fmt.Errorf("scan rate: %w", err)
	}
	if err := Rate(v).Validate(); err != nil {
		return fmt.Errorf("scan rate: %w", err)
	}
	*r = Rate(v)
	return nil
}

func unmarshalFixed(b []byte, scale int, dst *int64) error {
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, `"`) {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	}
	v, err := parseFixed(s, scale)
	if err != nil {
		return err
	}
	*dst = v
	return nil
}

// parseFixed 把十进制字符串转成放大 10^scale 倍的整数，不经过 float。
func parseFixed(s string, scale int) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("不能为空")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	intPart, fracPart, _ := strings.Cut(s, ".")
	if intPart == "" || len(fracPart) > scale || strings.ContainsAny(intPart+fracPart, "+-eE ") {
		return 0, fmt.Errorf("最多 %d 位小数", scale)
	}
	fracPart += strings.Repeat("0", scale-len(fracPart))
	v, err := strconv.ParseInt(intPart+fracPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("不是合法数字")
	}
	if neg {
		v = -v
	}
	return v, nil
}

func formatFixed(v int64, scale int) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	div := int64(1)
	for i := 0; i < scale; i++ {
		div *= 10
	}
	return fmt.Sprintf("%s%d.%0*d", sign, v/div, scale, v%div)
}

func scanFixed(src any, scale int) (int64, error) {
	switch t := src.(type) {
	case []byte:
		return parseFixed(string(t), scale)
	case string:
		return parseFixed(t, scale)
	case int64:
		return parseFixed(strconv.FormatInt(t, 10), scale)
	case nil:
		return 0, fmt.Errorf("值为 NULL")
	default:
		return 0, fmt.Errorf("不支持的类型 %T", src)
	}
}
