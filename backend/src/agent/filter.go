package agent

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxFinalRunes = 4000

var (
	// 模型可能带出来的内部标签：<final>、<tool ...>、<item>...</item>、<observation> 等。
	tagPattern   = regexp.MustCompile(`</?(final|tool|tool_call|item|observation|answer|json|think|thinking)\b[^>]*>`)
	fencePattern = regexp.MustCompile("(?s)```[a-zA-Z]*\\n?(.*?)```")
	// 泄露内部提示的行：含内部规则标记，或以这些前缀开头。
	leakPrefixes = []string{"system:", "system prompt", "系统提示", "可用工具：", "参数 schema", "规则：", "{{tools}}"}
)

// FilterFinal 清理模型的最终回答：去掉围栏和内部标签、删掉不在可信集里的商品 ID、删掉泄露内部提示的行、限制长度。
// 返回清理后的文本和被删掉的商品 ID。
func FilterFinal(text string, evidence map[string]bool) (string, []string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = fencePattern.ReplaceAllString(text, "$1")
	text = tagPattern.ReplaceAllString(text, "")
	var removed []string
	text = productIDPattern.ReplaceAllStringFunc(text, func(id string) string {
		if evidence[id] {
			return "" // 可信的 ID 也不该出现在正文里（卡片里有），只去掉标识
		}
		if !containsStr(removed, id) {
			removed = append(removed, id)
		}
		return ""
	})
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		leak := strings.Contains(trimmed, internalRuleMarker)
		for _, p := range leakPrefixes {
			if strings.HasPrefix(lower, p) {
				leak = true
			}
		}
		if leak {
			continue
		}
		lines = append(lines, strings.TrimRight(line, " \t"))
	}
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	// 去掉 ID 后留下的空括号和多余空格
	out = strings.NewReplacer("（）", "", "()", "", "[]", "", "「」", "", "  ", " ").Replace(out)
	if utf8.RuneCountInString(out) > maxFinalRunes {
		out = string([]rune(out)[:maxFinalRunes]) + "…"
	}
	return out, removed
}
