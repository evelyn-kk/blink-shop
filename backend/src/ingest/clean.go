package ingest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// PlainText 统一换行、去掉控制字符、压缩行内空白，并把连续空行合并为一个空行（段落分隔）。
// 结果确定：同样的输入总是得到同样的文本，内容 hash 才能用于去重。
func PlainText(input string) string {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	var out []string
	blank := false
	for _, line := range strings.Split(input, "\n") {
		line = normalizeLine(line)
		if line == "" {
			blank = len(out) > 0
			continue
		}
		if blank {
			out = append(out, "")
			blank = false
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// normalizeLine 把制表符、全角空格等空白压成单个空格，去掉其他控制字符和零宽字符。
func normalizeLine(line string) string {
	var b strings.Builder
	space := false
	for _, r := range line {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), r == '\u200b', r == '\u200c', r == '\u200d', r == '\ufeff', r == '\u2028', r == '\u2029':
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// 这些元素的内容不是正文，整段跳过。
var skippedElements = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true, atom.Head: true,
	atom.Svg: true, atom.Math: true, atom.Iframe: true, atom.Object: true, atom.Canvas: true, atom.Nav: true,
}

// 块级元素前后换行，段落/标题/列表项之间留空行，便于按段落切块。
var paragraphElements = map[atom.Atom]bool{
	atom.P: true, atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Ul: true, atom.Ol: true, atom.Table: true, atom.Blockquote: true, atom.Pre: true, atom.Section: true,
	atom.Article: true, atom.Header: true, atom.Footer: true, atom.Dl: true, atom.Figure: true,
}

var lineElements = map[atom.Atom]bool{
	atom.Br: true, atom.Div: true, atom.Li: true, atom.Tr: true, atom.Dt: true, atom.Dd: true, atom.Hr: true,
	atom.Main: true, atom.Aside: true, atom.Form: true, atom.Figcaption: true, atom.Caption: true,
}

// HTMLToText 用 HTML 分词器提取正文：跳过脚本、样式、导航等非正文元素，块级元素换行，实体解码。
// 返回 <title> 的文本（可能为空）和正文。
func HTMLToText(input string) (title, text string) {
	z := html.NewTokenizer(strings.NewReader(input))
	var b strings.Builder
	var titleBuf strings.Builder
	skipDepth := 0
	inTitle := false
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			// io.EOF 表示读完；字符串输入不会有其他读取错误，有也只返回已解析的部分。
			return normalizeLine(titleBuf.String()), PlainText(b.String())
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			a := atom.Lookup(name)
			if a == atom.Title {
				inTitle = tt == html.StartTagToken
				continue
			}
			if skippedElements[a] && tt == html.StartTagToken {
				skipDepth++
				continue
			}
			writeBreak(&b, a)
		case html.EndTagToken:
			name, _ := z.TagName()
			a := atom.Lookup(name)
			if a == atom.Title {
				inTitle = false
				continue
			}
			if skippedElements[a] {
				if skipDepth > 0 {
					skipDepth--
				}
				continue
			}
			writeBreak(&b, a)
		case html.TextToken:
			if inTitle {
				titleBuf.Write(z.Text())
				continue
			}
			if skipDepth == 0 {
				b.Write(z.Text())
			}
		}
	}
}

func writeBreak(b *strings.Builder, a atom.Atom) {
	switch {
	case paragraphElements[a]:
		b.WriteString("\n\n")
	case lineElements[a]:
		// 相邻的块级标签（如 </li><li>）只换一行，不要变成段落分隔。
		if !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
	case a == atom.Td || a == atom.Th:
		b.WriteString(" ")
	}
}

// JSONToText 把 JSON 展开成“路径: 值”的行。对象按键名排序，结果与键的书写顺序无关；
// 顶层数组的每个元素之间留空行，切块时一条记录尽量落在同一块。不是合法 JSON 时返回错误。
func JSONToText(input string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(input))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return "", fmt.Errorf("JSON 格式不正确: %w", err)
	}
	if dec.More() {
		return "", fmt.Errorf("JSON 只能包含一个值")
	}
	var lines []string
	if items, ok := value.([]any); ok {
		for i, item := range items {
			if i > 0 && len(lines) > 0 {
				lines = append(lines, "")
			}
			flattenJSON(fmt.Sprintf("[%d]", i), item, &lines)
		}
	} else {
		flattenJSON("", value, &lines)
	}
	return PlainText(strings.Join(lines, "\n")), nil
}

func flattenJSON(prefix string, value any, lines *[]string) {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			next := k
			if prefix != "" {
				next = prefix + "." + k
			}
			flattenJSON(next, v[k], lines)
		}
	case []any:
		for i, item := range v {
			flattenJSON(fmt.Sprintf("%s[%d]", prefix, i), item, lines)
		}
	case string:
		if s := normalizeLine(v); s != "" {
			*lines = append(*lines, label(prefix)+s)
		}
	case json.Number:
		*lines = append(*lines, label(prefix)+v.String())
	case bool:
		*lines = append(*lines, label(prefix)+strconv.FormatBool(v))
	}
}

func label(prefix string) string {
	if prefix == "" {
		return ""
	}
	return prefix + ": "
}

// truncateRunes 截断到 max 个字符，返回是否发生截断。
func truncateRunes(s string, max int) (string, bool) {
	n := 0
	for i := range s {
		if n == max {
			return strings.TrimSpace(s[:i]), true
		}
		n++
	}
	return s, false
}
