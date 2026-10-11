package agent

import (
	"regexp"
	"strings"
)

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

// piiLabels 是联系方式前面的说明词；去掉联系方式时一起去掉，免得“手机号”被当成要找“手机”。
var piiLabels = regexp.MustCompile(`(?:我的)?(?:手机号码|手机号|电话号码|联系电话|电话|手机|电子邮箱|邮箱地址|邮箱|身份证号码|身份证号|身份证|银行卡号|银行卡|卡号)\s*(?:是|为|：|:)?\s*$`)

// StripPII 从用户的话里去掉手机号、邮箱、证件号、卡号及其前面的说明词（“我手机号 138…”），用于规划和检索：
// 联系方式不会变成检索词，也不会在回答里被复述。没有这些信息时原样返回。
func StripPII(s string) string {
	var b strings.Builder
	rest := s
	for {
		loc := firstPII(rest)
		if loc == nil {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(piiLabels.ReplaceAllString(rest[:loc[0]], ""))
		b.WriteString(" ")
		rest = rest[loc[1]:]
	}
}

func firstPII(s string) []int {
	var best []int
	for _, re := range []*regexp.Regexp{piiEmail, piiIDCard, piiCard, piiPhone} {
		if loc := re.FindStringIndex(s); loc != nil && (best == nil || loc[0] < best[0]) {
			best = loc
		}
	}
	return best
}
