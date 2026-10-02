package ingest

import (
	"context"
	"fmt"
	"strings"
)

const (
	// MaxTextRunes 是单篇资料清洗后保留的最大字符数（与上游一致），超出部分截断。
	MaxTextRunes = 120_000
	// MaxTitleRunes 是标题长度上限；推断出的标题截到 80 字。
	MaxTitleRunes      = 120
	inferredTitleRunes = 80
)

// 来源类型与对应的文档类型（与上游一致）。
const (
	SourceText = "text"
	SourceHTML = "html"
	SourceJSON = "json"
)

var docTypeBySource = map[string]string{
	SourceText: "unstructured_note",
	SourceHTML: "web_article",
	SourceJSON: "structured_note",
}

// InputError 是输入本身的问题，Field 指出字段，HTTP 层转成 400。
type InputError struct {
	Field   string
	Message string
}

func (e *InputError) Error() string { return e.Message }

// Source 是一次采集的原始输入：content / html / json_text / source_url 四选一。
type Source struct {
	Title      string
	SourceType string // text / html / json；为空时按字段或抓取到的内容类型判断
	Content    string
	HTML       string
	JSONText   string
	SourceURL  string
}

// Parsed 是清洗后的资料。Content 为空时不会返回（以 InputError 报告）。
type Parsed struct {
	Title      string
	DocType    string
	SourceType string
	Content    string
	SourceURL  string // 抓取时为最终地址（跟随重定向后）
	TextRunes  int
	Truncated  bool
}

// URLFetcher 由 *Fetcher 实现；测试可替换。
type URLFetcher interface {
	Fetch(ctx context.Context, raw string) (Fetched, error)
}

// Parse 取得原始内容（必要时抓取 URL），按来源类型清洗为纯文本并推断标题。
// 错误：*InputError（输入不合法）、ErrURLNotAllowed（SSRF 拦截）、ErrFetchFailed（抓取失败）。
func Parse(ctx context.Context, fetcher URLFetcher, src Source) (Parsed, error) {
	sourceType, err := normalizeSourceType(src.SourceType)
	if err != nil {
		return Parsed{}, err
	}
	title := strings.TrimSpace(src.Title)
	if err := checkTitle(title); err != nil {
		return Parsed{}, err
	}

	var raw, sourceURL string
	given := 0
	for _, v := range []string{src.Content, src.HTML, src.JSONText, src.SourceURL} {
		if strings.TrimSpace(v) != "" {
			given++
		}
	}
	if given != 1 {
		return Parsed{}, &InputError{Field: "content", Message: "content、html、json_text、source_url 必须且只能提供一个"}
	}
	switch {
	case strings.TrimSpace(src.HTML) != "":
		raw, sourceType = src.HTML, pick(sourceType, SourceHTML)
	case strings.TrimSpace(src.JSONText) != "":
		raw, sourceType = src.JSONText, pick(sourceType, SourceJSON)
	case strings.TrimSpace(src.Content) != "":
		raw, sourceType = src.Content, pick(sourceType, SourceText)
	default:
		if _, err := CheckURL(src.SourceURL); err != nil {
			return Parsed{}, err
		}
		if fetcher == nil {
			return Parsed{}, fmt.Errorf("%w: 未启用网页采集", ErrFetchFailed)
		}
		fetched, err := fetcher.Fetch(ctx, src.SourceURL)
		if err != nil {
			return Parsed{}, err
		}
		raw, sourceURL = fetched.Body, fetched.FinalURL
		if sourceURL == "" {
			sourceURL = strings.TrimSpace(src.SourceURL)
		}
		sourceType = pick(sourceType, sourceTypeForMedia(fetched.MediaType))
	}

	var text, htmlTitle string
	switch sourceType {
	case SourceHTML:
		htmlTitle, text = HTMLToText(raw)
	case SourceJSON:
		if text, err = JSONToText(raw); err != nil {
			return Parsed{}, &InputError{Field: fieldFor(src, "json_text"), Message: err.Error()}
		}
	default:
		text = PlainText(raw)
	}
	text, truncated := truncateRunes(text, MaxTextRunes)
	if text == "" {
		return Parsed{}, &InputError{Field: fieldFor(src, "content"), Message: "清洗后没有可用的正文内容"}
	}
	if title == "" {
		title = inferTitle(htmlTitle, text, sourceURL)
	}
	return Parsed{
		Title: title, DocType: docTypeBySource[sourceType], SourceType: sourceType, Content: text,
		SourceURL: sourceURL, TextRunes: len([]rune(text)), Truncated: truncated,
	}, nil
}

func normalizeSourceType(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return "", nil
	case "text", "txt", "plain":
		return SourceText, nil
	case "html", "web", "page":
		return SourceHTML, nil
	case "json":
		return SourceJSON, nil
	}
	return "", &InputError{Field: "source_type", Message: "source_type 只能是 text、html 或 json"}
}

func sourceTypeForMedia(mediaType string) string {
	switch mediaType {
	case "text/html", "application/xhtml+xml":
		return SourceHTML
	case "application/json", "text/json":
		return SourceJSON
	}
	return SourceText
}

func pick(explicit, fallback string) string {
	if explicit != "" {
		return explicit
	}
	return fallback
}

// fieldFor 返回实际提供内容的字段名，错误据此定位。
func fieldFor(src Source, fallback string) string {
	switch {
	case strings.TrimSpace(src.SourceURL) != "":
		return "source_url"
	case strings.TrimSpace(src.HTML) != "":
		return "html"
	case strings.TrimSpace(src.JSONText) != "":
		return "json_text"
	case strings.TrimSpace(src.Content) != "":
		return "content"
	}
	return fallback
}

// checkTitle：标题是单行展示字段，不能含控制字符或换行，不超过 120 字。
func checkTitle(title string) error {
	if len([]rune(title)) > MaxTitleRunes {
		return &InputError{Field: "title", Message: "标题不能超过 120 个字"}
	}
	for _, r := range title {
		if r < 0x20 || r == 0x7f || r == ' ' || r == ' ' {
			return &InputError{Field: "title", Message: "标题不能包含换行或控制字符"}
		}
	}
	return nil
}

func inferTitle(htmlTitle, text, sourceURL string) string {
	if htmlTitle != "" {
		t, _ := truncateRunes(htmlTitle, inferredTitleRunes)
		return t
	}
	first, _, _ := strings.Cut(text, "\n")
	if first = strings.TrimSpace(first); first != "" {
		t, _ := truncateRunes(first, inferredTitleRunes)
		return t
	}
	if sourceURL != "" {
		t, _ := truncateRunes(sourceURL, inferredTitleRunes)
		return t
	}
	return "非结构化资料"
}
