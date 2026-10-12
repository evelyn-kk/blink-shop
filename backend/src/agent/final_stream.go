package agent

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// finalStream 在工具循环的流式输出里识别最终回答，边生成边把 text 字段解码、过滤后推给用户。
//
// 只有输出开头确认是 {"type":"final","text":" 时才流式推送；工具调用、键顺序不同、协议错误都不推送，由调用方按整段处理。
// 过滤与 FilterFinal 一致，按句进行：每行先攒够 lineDecideRunes 个字确认不是泄露前缀再放出；去掉内部标签、围栏和商品 ID；
// 一句话里出现内部规则标记时，这句和这一行剩下的内容都丢弃；总长度不超过 maxFinalRunes。
type finalStream struct {
	evidence map[string]bool
	emit     func(string)

	raw   strings.Builder
	state int // streamUndecided / streamText / streamNone / streamDone
	dec   jsonStringDecoder

	line        []rune // 当前行还没决定能否放出的部分（行首）
	lineDecided bool
	lineLeak    bool
	sentence    strings.Builder // 当前行已放行、还没到句末的部分

	emittedRunes int
	emitted      strings.Builder
	removed      []string
	truncated    bool
}

const (
	streamUndecided = iota
	streamText
	streamNone
	streamDone
)

// lineDecideRunes 是判断一行是否以泄露前缀开头所需的字数（最长前缀 "system prompt" 13 个字符）。
const lineDecideRunes = 16

var finalPrefixPattern = regexp.MustCompile("^\\s*(?:```(?:json)?\\s*)?\\{\\s*\"type\"\\s*:\\s*\"final\"\\s*,\\s*\"text\"\\s*:\\s*\"")

func newFinalStream(evidence map[string]bool, emit func(string)) *finalStream {
	return &finalStream{evidence: evidence, emit: emit}
}

// feed 接收一段模型输出。
func (f *finalStream) feed(delta string) error {
	f.raw.WriteString(delta)
	switch f.state {
	case streamUndecided:
		raw := f.raw.String()
		if loc := finalPrefixPattern.FindStringIndex(raw); loc != nil {
			f.state = streamText
			f.decode(raw[loc[1]:])
			return nil
		}
		trimmed := strings.TrimLeft(raw, " \t\r\n`json")
		// 已经能确定不是最终回答（工具调用、键顺序不同、不是 JSON）就不再判断
		if len(trimmed) > 40 || (len(trimmed) > 0 && trimmed[0] != '{') || strings.Contains(raw, `"tool"`) {
			f.state = streamNone
		}
	case streamText:
		f.decode(delta)
	}
	return nil
}

// streaming 表示这一轮输出是流式推送的最终回答。
func (f *finalStream) streaming() bool { return f.state == streamText || f.state == streamDone }

// emittedAny 表示已经推给用户过内容。
func (f *finalStream) emittedAny() bool { return f.emittedRunes > 0 }

func (f *finalStream) decode(s string) {
	for _, r := range s {
		if f.state != streamText {
			return
		}
		out, done := f.dec.next(r)
		if done {
			f.state = streamDone
			f.endLine(false)
			return
		}
		for _, c := range out {
			f.onRune(c)
		}
	}
}

func (f *finalStream) onRune(r rune) {
	if r == '\r' {
		return
	}
	if r == '\n' {
		f.endLine(true)
		return
	}
	if f.lineLeak {
		return
	}
	if !f.lineDecided {
		f.line = append(f.line, r)
		if len(f.line) >= lineDecideRunes {
			f.decideLine()
		}
		return
	}
	f.sentence.WriteRune(r)
	if isStreamBreak(r) {
		f.flushSentence("")
	}
}

// decideLine 判断行首是否是泄露前缀；放行后行首的字进入句子缓冲。
func (f *finalStream) decideLine() {
	f.lineDecided = true
	head := string(f.line)
	f.line = nil
	lower := strings.ToLower(strings.TrimSpace(head))
	for _, p := range leakPrefixes {
		if strings.HasPrefix(lower, p) {
			f.lineLeak = true
			return
		}
	}
	if strings.HasPrefix(strings.TrimSpace(head), "```") {
		head = strings.TrimLeft(strings.TrimSpace(head), "`")
		head = strings.TrimPrefix(head, "json")
	}
	for _, r := range head {
		f.sentence.WriteRune(r)
		if isStreamBreak(r) {
			f.flushSentence("")
		}
	}
}

// endLine 结束当前行；newline 为 true 时行尾补换行（只在这一行放出过内容时）。
func (f *finalStream) endLine(newline bool) {
	if !f.lineDecided && len(f.line) > 0 {
		f.decideLine()
	}
	suffix := ""
	if newline && !f.lineLeak && (f.emittedRunes > 0 || strings.TrimSpace(f.sentence.String()) != "") {
		suffix = "\n" // 开始推送后，空行也保留（与 FilterFinal 一致）
	}
	if !f.lineLeak {
		f.flushSentence(suffix)
	}
	f.sentence.Reset()
	f.line, f.lineDecided, f.lineLeak = nil, false, false
}

// flushSentence 过滤并推送当前句子（suffix 追加在句末，用于换行）。
func (f *finalStream) flushSentence(suffix string) {
	seg := f.sentence.String()
	f.sentence.Reset()
	if strings.Contains(seg, internalRuleMarker) {
		f.lineLeak = true
		return
	}
	seg = strings.ReplaceAll(seg, "```", "")
	seg = tagPattern.ReplaceAllString(seg, "")
	seg = productIDPattern.ReplaceAllStringFunc(seg, func(id string) string {
		if !f.evidence[id] && !containsStr(f.removed, id) {
			f.removed = append(f.removed, id)
		}
		return ""
	})
	seg = strings.NewReplacer("（）", "", "()", "", "[]", "", "「」", "", "  ", " ").Replace(seg)
	if strings.TrimSpace(seg) == "" {
		seg = ""
	}
	if f.emittedRunes == 0 {
		seg = strings.TrimLeft(seg, " \t")
		if seg == "" {
			return // 回答开头不推空白和空行
		}
	}
	seg += suffix
	if seg == "" || f.truncated {
		return
	}
	n := utf8.RuneCountInString(seg)
	if f.emittedRunes+n > maxFinalRunes {
		seg = string([]rune(seg)[:maxFinalRunes-f.emittedRunes]) + "…"
		f.truncated = true
	}
	f.emittedRunes += utf8.RuneCountInString(seg)
	f.emitted.WriteString(seg)
	f.emit(seg)
}

// finish 在输出结束后推送剩余内容，返回推送的全文和删掉的商品 ID。
func (f *finalStream) finish() (string, []string) {
	if f.state == streamText {
		f.state = streamDone
		f.endLine(false)
	}
	return strings.TrimSpace(f.emitted.String()), f.removed
}

// jsonStringDecoder 逐字解码 JSON 字符串内容（引号之后的部分），遇到未转义的结束引号时返回 done。
type jsonStringDecoder struct {
	esc bool
	hex []rune // \u 之后收集的十六进制位
	hi  rune   // 代理对的高位
}

func (d *jsonStringDecoder) next(r rune) (string, bool) {
	switch {
	case d.hex != nil:
		d.hex = append(d.hex, r)
		if len(d.hex) < 4 {
			return "", false
		}
		v, err := strconv.ParseUint(string(d.hex), 16, 32)
		d.hex = nil
		if err != nil {
			return "", false
		}
		c := rune(v)
		if c >= 0xD800 && c < 0xDC00 {
			d.hi = c
			return "", false
		}
		if d.hi != 0 && c >= 0xDC00 && c < 0xE000 {
			c = (d.hi-0xD800)<<10 + (c - 0xDC00) + 0x10000
		}
		d.hi = 0
		return string(c), false
	case d.esc:
		d.esc = false
		switch r {
		case 'n':
			return "\n", false
		case 't':
			return "\t", false
		case 'r':
			return "", false
		case 'b', 'f':
			return "", false
		case 'u':
			d.hex = []rune{}
			return "", false
		default: // \" \\ \/
			return string(r), false
		}
	case r == '\\':
		d.esc = true
		return "", false
	case r == '"':
		return "", true
	}
	return string(r), false
}

// isStreamBreak 是流式推送的切分点：句末和逗号、顿号（商品 ID、内部标签、内部规则标记里都不含这些标点，按段过滤不会漏）。
func isStreamBreak(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '，', '、':
		return true
	}
	return false
}
