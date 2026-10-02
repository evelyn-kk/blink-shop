package rag

import (
	"strings"
	"unicode"
)

// MaxQueryTerms 限制一次检索的词数，避免很长的问题拼出过大的 SQL。
const MaxQueryTerms = 24

// Term 是检索词及其权重。
type Term struct {
	Text   string
	Weight float64
}

// 含这些字的二元组没有检索意义（语气词、疑问词）。
const stopChars = "吗呢吧的了么怎什哪啊呀嘛呗哦"

// 常见的提问用语，本身不表达内容。
var stopBigrams = map[string]bool{
	"多少": true, "多久": true, "可以": true, "能不": true, "是否": true, "有没": true, "没有": true, "如何": true,
	"为什": true, "一下": true, "请问": true, "能否": true, "怎样": true, "这个": true, "那个": true, "需要": true,
}

// QueryTerms 把问题拆成检索词：汉字按相邻两字（bigram）切分，含语气词的二元组丢弃，常见提问用语（如“多少”）
// 权重降为 0.2；单个汉字的片段保留单字（权重 0.5）；字母数字片段（型号、品牌、单位）整体作为一个词，权重为 2。
// 全部转小写、去重，最多 MaxQueryTerms 个。
func QueryTerms(query string) []Term {
	var out []Term
	seen := map[string]bool{}
	add := func(text string, w float64) {
		text = strings.ToLower(text)
		if text == "" || seen[text] || len(out) >= MaxQueryTerms {
			return
		}
		seen[text] = true
		out = append(out, Term{Text: text, Weight: w})
	}
	var run []rune
	runIsHan := false
	flush := func() {
		defer func() { run = nil }()
		if len(run) == 0 {
			return
		}
		if !runIsHan {
			if len(run) >= 2 || unicode.IsDigit(run[0]) {
				add(string(run), 2)
			}
			return
		}
		if len(run) == 1 {
			if !strings.ContainsRune(stopChars, run[0]) {
				add(string(run), 0.5)
			}
			return
		}
		for i := 0; i+2 <= len(run); i++ {
			bg := string(run[i : i+2])
			if strings.ContainsRune(stopChars, run[i]) || strings.ContainsRune(stopChars, run[i+1]) {
				continue
			}
			if stopBigrams[bg] {
				add(bg, 0.2)
				continue
			}
			add(bg, 1)
		}
	}
	for _, r := range query {
		isHan := unicode.Is(unicode.Han, r)
		if !isHan && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(run) > 0 && isHan != runIsHan {
			flush()
		}
		runIsHan = isHan
		run = append(run, r)
	}
	flush()
	return out
}

func termTexts(terms []Term) []string {
	out := make([]string, len(terms))
	for i, t := range terms {
		out[i] = t.Text
	}
	return out
}
