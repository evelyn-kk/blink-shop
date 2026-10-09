// Package risk 在调用模型或工具之前做风险检查：命中阻断词的对话不进入规划和工具层。
package risk

import (
	"context"
	"strings"
	"unicode"
)

// Match 是一次命中：Word 是命中的阻断词。
type Match struct {
	Word string
}

// Checker 判断一段用户输入是否应该被拦截。
type Checker interface {
	Check(ctx context.Context, text string) (Match, bool)
}

// WordList 按阻断词表检查：不区分大小写、忽略空白地做包含匹配。Words 每次检查时读取，便于接动态配置。
type WordList struct {
	Words func(ctx context.Context) []string
}

// Static 用固定词表创建检查器。
func Static(words ...string) WordList {
	return WordList{Words: func(context.Context) []string { return words }}
}

// Check 返回第一个命中的词（按词表顺序）。
func (w WordList) Check(ctx context.Context, text string) (Match, bool) {
	if w.Words == nil {
		return Match{}, false
	}
	normalized := normalize(text)
	if normalized == "" {
		return Match{}, false
	}
	for _, word := range w.Words(ctx) {
		if n := normalize(word); n != "" && strings.Contains(normalized, n) {
			return Match{Word: strings.TrimSpace(word)}, true
		}
	}
	return Match{}, false
}

// normalize 去掉空白并转小写，让“绕过 风控”也能命中“绕过风控”。
func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Split 把逗号分隔的词表拆成词（去空白、去空项）。
func Split(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
