package rag

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// checkChunks 验证通用不变量：编号连续、不超长、不为空、标题非空。
func checkChunks(t *testing.T, chunks []Chunk) {
	t.Helper()
	for i, c := range chunks {
		if c.Index != i {
			t.Fatalf("chunk %d has index %d", i, c.Index)
		}
		if n := utf8.RuneCountInString(c.Content); n == 0 || n > MaxChunkRunes {
			t.Fatalf("chunk %d has %d runes", i, n)
		}
		if c.Title == "" {
			t.Fatalf("chunk %d has no title", i)
		}
	}
}

// covered 验证原文每一段都出现在某个块里（切块不丢内容）。
func covered(t *testing.T, content string, chunks []Chunk) {
	t.Helper()
	all := ""
	for _, c := range chunks {
		all += c.Content + "\n"
	}
	for _, para := range strings.Split(content, "\n\n") {
		for _, sentence := range cutAfter(para, "。！？；!?;\n") {
			if s := strings.TrimSpace(sentence); s != "" && !strings.Contains(all, s) && utf8.RuneCountInString(s) <= MaxChunkRunes {
				t.Fatalf("sentence lost: %q", s)
			}
		}
	}
}

func TestSplitShortParagraphsMerged(t *testing.T) {
	content := "第一段。\n\n第二段。\n\n第三段。"
	chunks := Split("说明", "policy", content)
	checkChunks(t, chunks)
	if len(chunks) != 1 || chunks[0].Content != content || chunks[0].Title != "说明" {
		t.Fatalf("chunks = %+v", chunks)
	}
	if Split("空", "policy", " \n\n ") != nil {
		t.Fatal("blank content should have no chunks")
	}
}

func TestSplitParagraphBoundaries(t *testing.T) {
	a := strings.Repeat("甲", 500)
	b := strings.Repeat("乙", 299)
	c := strings.Repeat("丙", 400)
	// a+b 正好 800（含段落分隔的 2 个字符为 801，超出）→ a 单独一块；b+c = 701 → 一块。
	chunks := Split("t", "policy", a+"\n\n"+b+"\n\n"+c)
	checkChunks(t, chunks)
	if len(chunks) != 2 || chunks[0].Content != a || chunks[1].Content != b+"\n\n"+c {
		t.Fatalf("got %d chunks: %v", len(chunks), lens(chunks))
	}
	// 恰好 798 + 2 = 800 时合并。
	chunks = Split("t", "policy", strings.Repeat("丁", 398)+"\n\n"+strings.Repeat("戊", 400))
	if len(chunks) != 1 || utf8.RuneCountInString(chunks[0].Content) != 800 {
		t.Fatalf("exact boundary: %v", lens(chunks))
	}
}

func lens(chunks []Chunk) []int {
	var out []int
	for _, c := range chunks {
		out = append(out, utf8.RuneCountInString(c.Content))
	}
	return out
}

func TestSplitLongParagraphBySentence(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		b.WriteString("这是第" + strings.Repeat("一", 20) + "句话，用来测试长段落按句子切开。")
	}
	content := b.String()
	chunks := Split("长段落", "product_detail", content)
	checkChunks(t, chunks)
	covered(t, content, chunks)
	if len(chunks) < 3 {
		t.Fatalf("expected several chunks, got %v", lens(chunks))
	}
	for i := 1; i < len(chunks); i++ {
		// 相邻块有重叠：后一块以前一块末尾的文字开头。
		prevTail := lastRunes(chunks[i-1].Content, OverlapRunes)
		if !strings.HasPrefix(chunks[i].Content, prevTail) {
			t.Fatalf("chunk %d does not start with overlap of chunk %d", i, i-1)
		}
		// 除重叠外，块在句末标点处断开。
		if !strings.HasSuffix(chunks[i-1].Content, "。") {
			t.Fatalf("chunk %d does not end at sentence boundary: %q", i-1, lastRunes(chunks[i-1].Content, 10))
		}
	}
}

func TestSplitHardSplitsSentenceWithoutPunctuation(t *testing.T) {
	content := strings.Repeat("无", 2000)
	chunks := Split("t", "policy", content)
	checkChunks(t, chunks)
	if len(chunks) != 3 || lens(chunks)[0] != 800 || lens(chunks)[1] != 800 {
		t.Fatalf("hard split = %v", lens(chunks))
	}
	// 去掉重叠后拼回原文。
	rebuilt := chunks[0].Content
	for _, c := range chunks[1:] {
		rebuilt += string([]rune(c.Content)[OverlapRunes:])
	}
	if rebuilt != content {
		t.Fatal("hard split lost or duplicated content")
	}
}

func TestSplitFAQ(t *testing.T) {
	content := "常见问题汇总\n\n问：保修多久？\n答：一年。\n人为损坏不保。\n\nQ: 能退货吗？\nA: 七天无理由。\n问题：没有答案的问题"
	chunks := Split("售后 FAQ", "faq", content)
	checkChunks(t, chunks)
	want := []Chunk{
		{0, "售后 FAQ", "常见问题汇总"},
		{1, "保修多久？", "问：保修多久？\n答：一年。\n人为损坏不保。"},
		{2, "能退货吗？", "Q: 能退货吗？\nA: 七天无理由。"},
		{3, "没有答案的问题", "问题：没有答案的问题"},
	}
	if len(chunks) != len(want) {
		t.Fatalf("faq chunks = %+v", chunks)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Fatalf("chunk %d = %+v, want %+v", i, chunks[i], want[i])
		}
	}
	// 不是问答格式的 faq 文档按普通段落切。
	if c := Split("t", "faq", "只是普通说明"); len(c) != 1 || c[0].Content != "只是普通说明" {
		t.Fatalf("plain faq = %+v", c)
	}
	// 非 faq 类型不识别问答。
	if c := Split("t", "policy", "问：a\n答：b"); len(c) != 1 {
		t.Fatalf("policy with Q/A = %+v", c)
	}
}

func TestSnippet(t *testing.T) {
	content := strings.Repeat("前", 300) + "关键词在这里" + strings.Repeat("后", 300)
	s := Snippet(content, []string{"关键", "关键词在这里"}, 40)
	if !strings.Contains(s, "关键词在这里") || !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") || utf8.RuneCountInString(s) != 42 {
		t.Fatalf("snippet = %q", s)
	}
	if s := Snippet("短文本", []string{"x"}, 40); s != "短文本" {
		t.Fatalf("short = %q", s)
	}
	if s := Snippet(strings.Repeat("a", 100), []string{"zz"}, 10); s != strings.Repeat("a", 10)+"…" {
		t.Fatalf("no match = %q", s)
	}
}
