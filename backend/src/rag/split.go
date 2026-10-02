package rag

import (
	"strings"
	"unicode/utf8"
)

// 切块参数（与上游一致：每块最多 800 字，长段落切开时相邻块重叠约 100 字）。
const (
	MaxChunkRunes = 800
	OverlapRunes  = 100
	MaxChunks     = 500
	maxChunkTitle = 120
)

// Chunk 是切块结果；Index 从 0 开始连续编号。
type Chunk struct {
	Index   int
	Title   string
	Content string
}

// Split 把清洗后的正文切成块。策略（见 backend/README.md“知识文档”）：
//  1. faq 类型且能识别出“问：/Q:”开头的问题时，每个问题连同其后的回答一块，块标题为问题；第一个问题之前的内容按普通段落切；
//  2. 否则按空行分段，相邻段落合并到不超过 MaxChunkRunes；
//  3. 单个段落超长时按句末标点（。！？；!?;）再按逗号切句，句子打包成块，相邻块带上一块末尾约 100 字作为重叠；
//  4. 单句仍超长时按固定长度硬切，同样带重叠。
//
// 每块不超过 MaxChunkRunes 字，块的顺序与原文一致。
func Split(title, docType, content string) []Chunk {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	if docType == "faq" {
		if preamble, chunks := splitFAQ(content); len(chunks) > 0 {
			return number(append(splitParagraphs(title, preamble), chunks...))
		}
	}
	return number(splitParagraphs(title, content))
}

func splitParagraphs(title, content string) []Chunk {
	var out []Chunk
	var cur []string
	curLen := 0
	flush := func() {
		if len(cur) > 0 {
			out = append(out, Chunk{Title: title, Content: strings.Join(cur, "\n\n")})
			cur, curLen = nil, 0
		}
	}
	for _, para := range strings.Split(content, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		n := utf8.RuneCountInString(para)
		if n > MaxChunkRunes {
			flush()
			for _, piece := range splitLong(para) {
				out = append(out, Chunk{Title: title, Content: piece})
			}
			continue
		}
		if curLen > 0 && curLen+2+n > MaxChunkRunes {
			flush()
		}
		cur = append(cur, para)
		if curLen > 0 {
			curLen += 2
		}
		curLen += n
	}
	flush()
	return out
}

func number(chunks []Chunk) []Chunk {
	for i := range chunks {
		chunks[i].Index = i
		chunks[i].Title = clip(chunks[i].Title, maxChunkTitle)
	}
	return chunks
}

// splitLong 切开超长段落：先切句再打包，每块开头带上一块末尾的重叠文字。
func splitLong(para string) []string {
	sentences := splitSentences(para)
	var out []string
	var cur strings.Builder
	curLen := 0
	emit := func() {
		if curLen == 0 {
			return
		}
		text := strings.TrimSpace(cur.String())
		out = append(out, text)
		tail := lastRunes(text, OverlapRunes)
		cur.Reset()
		cur.WriteString(tail)
		curLen = utf8.RuneCountInString(tail)
	}
	pending := false // cur 中是否有尚未输出的新内容（不只是重叠）
	for _, s := range sentences {
		n := utf8.RuneCountInString(s)
		if curLen+n > MaxChunkRunes && pending {
			emit()
			pending = false
		}
		if curLen+n > MaxChunkRunes {
			// 重叠 + 本句仍超长：丢掉重叠，单句硬切。
			cur.Reset()
			curLen = 0
			pieces := hardSplit(s)
			out = append(out, pieces[:len(pieces)-1]...)
			last := pieces[len(pieces)-1]
			cur.WriteString(last)
			curLen = utf8.RuneCountInString(last)
			pending = true
			continue
		}
		cur.WriteString(s)
		curLen += n
		pending = true
	}
	if pending {
		emit()
	}
	return out
}

// splitSentences 在句末标点之后切开（标点保留在前一句），换行也视为句子边界；
// 切出的句子仍超长时再按逗号、顿号切。
func splitSentences(text string) []string {
	parts := cutAfter(text, "。！？；!?;\n")
	var out []string
	for _, p := range parts {
		if utf8.RuneCountInString(p) > MaxChunkRunes {
			out = append(out, cutAfter(p, "，、,")...)
			continue
		}
		out = append(out, p)
	}
	return out
}

func cutAfter(text, marks string) []string {
	var out []string
	start := 0
	for i, r := range text {
		if strings.ContainsRune(marks, r) {
			end := i + utf8.RuneLen(r)
			out = append(out, text[start:end])
			start = end
		}
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

// hardSplit 按固定长度切，相邻片段重叠 OverlapRunes 字。
func hardSplit(s string) []string {
	r := []rune(s)
	var out []string
	for start := 0; ; {
		end := min(start+MaxChunkRunes, len(r))
		out = append(out, string(r[start:end]))
		if end == len(r) {
			return out
		}
		start = end - OverlapRunes
	}
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

var questionPrefixes = []string{"Q:", "Q：", "问：", "问:", "问题：", "问题:"}

// splitFAQ 识别以“问：”“Q:”等开头的问题行，每个问题和其后直到下一个问题的内容为一块。
// 返回第一个问题之前的内容（按普通段落处理）和问答块；识别不出问题时 chunks 为空。
func splitFAQ(content string) (preamble string, chunks []Chunk) {
	var pre []string
	var question string
	var answer []string
	flush := func() {
		if question == "" {
			return
		}
		text := strings.Join(append([]string{question}, answer...), "\n")
		q := strings.TrimSpace(trimAnyPrefix(question, questionPrefixes))
		if utf8.RuneCountInString(text) <= MaxChunkRunes {
			chunks = append(chunks, Chunk{Title: q, Content: text})
		} else {
			for _, piece := range splitLong(text) {
				chunks = append(chunks, Chunk{Title: q, Content: piece})
			}
		}
		question, answer = "", nil
	}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case hasAnyPrefix(trimmed, questionPrefixes):
			flush()
			question = trimmed
		case question != "":
			if trimmed != "" {
				answer = append(answer, trimmed)
			}
		default:
			pre = append(pre, line)
		}
	}
	flush()
	return strings.TrimSpace(strings.Join(pre, "\n")), chunks
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func trimAnyPrefix(s string, prefixes []string) string {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return s[len(p):]
		}
	}
	return s
}
