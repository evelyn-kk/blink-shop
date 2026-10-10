package agent

import "regexp"

// 写入轨迹和日志前的脱敏：手机号、邮箱、身份证号、银行卡号换成掩码。只用于观测数据，业务数据（评价、地址等）本身不改。
var (
	piiPhone  = regexp.MustCompile(`(?:\+?86[-\s]?)?1[3-9]\d[-\s]?\d{4}[-\s]?\d{4}`)
	piiEmail  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	piiIDCard = regexp.MustCompile(`\b\d{17}[\dXx]\b`)
	piiCard   = regexp.MustCompile(`\b\d{16,19}\b`)
)

// RedactPII 把文本里的手机号、邮箱、身份证号、银行卡号换成占位符。
func RedactPII(s string) string {
	s = piiEmail.ReplaceAllString(s, "[邮箱]")
	s = piiIDCard.ReplaceAllString(s, "[证件号]")
	s = piiCard.ReplaceAllString(s, "[卡号]")
	return piiPhone.ReplaceAllString(s, "[手机号]")
}

// redactValue 递归脱敏 map / 数组里的字符串。
func redactValue(v any) any {
	switch x := v.(type) {
	case string:
		return RedactPII(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = redactValue(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = redactValue(val)
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, val := range x {
			out[i] = RedactPII(val)
		}
		return out
	}
	return v
}
