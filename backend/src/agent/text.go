package agent

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/evelyn-kk/blink-shop/backend/src/domain"
)

// 中文用户输入的规则解析：检索词、预算、排除词、序号、数量和中文数字。没有模型时规划完全依赖这些规则。

// fillers 是对检索没有意义的口头语和功能词；按长度从长到短替换，避免“一款”先把“一”吃掉。
var fillers = []string{
	"帮我推荐", "给我推荐", "能不能推荐", "可以推荐", "求推荐", "有没有", "有什么", "有哪些", "哪一款", "哪一个", "想要买", "我想买", "我要买", "想买个", "想买一个",
	"性价比高", "性价比", "推荐一下", "推荐几款", "推荐一款", "推荐个", "推荐", "介绍一下", "介绍", "看一下", "看看", "找一下", "找一款", "找个", "帮我找", "找",
	"帮我", "给我", "我想", "我要", "想要", "请问", "麻烦", "能不能", "可以", "需要", "想", "要", "买个", "买一个", "买一台", "买一款", "买", "来个", "来一个",
	"一款", "一个", "一台", "一副", "一只", "一套", "一些", "几款", "几个", "哪款", "哪个", "哪些", "什么样的", "什么", "怎么样", "怎样", "如何", "多少钱", "价格",
	"便宜点的", "便宜的", "便宜", "实惠", "好用的", "好用", "好点的", "好一点的", "不错的", "好的", "好", "靓", "适合", "合适", "用的", "用来", "送给", "给",
	"一下", "一点", "左右", "大概", "大约", "差不多", "以内", "以下", "之内", "不超过", "预算", "元", "块钱", "块",
	"的", "吗", "呢", "吧", "啊", "呀", "哦", "啦", "嘛", "了", "请", "谢谢", "你好", "您好", "和", "或者", "或", "还是", "以及", "跟", "与", "我", "你", "他", "她", "它",
	"这", "那", "这个", "那个", "有", "是", "在", "也", "都", "就", "又", "再", "最", "很", "太", "比较", "点", "些", "款", "个", "台", "件", "下", "上", "里", "中",
}

var (
	// 用于把句子切成“汉字串 / 字母数字串”。
	numberPattern = `(\d+(?:\.\d+)?|[一二两三四五六七八九十百千万零〇]+)`
	// 预算：数字 + 可选单位 + 范围词，或“预算/不超过/最多/低于”+ 数字。
	budgetTail = regexp.MustCompile(numberPattern + `\s*(元|块钱|块|k|K|千)?\s*(以内|以下|之内|内|左右|上下|不超过|预算|的预算|元内|块内)`)
	budgetHead = regexp.MustCompile(`(预算|不超过|最多|低于|少于|控制在|价位)\s*(大概|大约|在|是|为)?\s*` + numberPattern + `\s*(元|块钱|块|k|K|千)?`)
	// 排除：不要 X / 不买 X / 别推荐 X / 排除 X / 除了 X / 不想要 X。
	excludePattern = regexp.MustCompile(`(?:不要|不买|别推荐|不推荐|排除|除了|不想要|不考虑|不选)\s*([^，,。；;！!？?\s]{1,12}?)(?:的|牌|品牌)?(?:[，,。；;！!？?\s]|$)`)
	// 序号：第 N 个/款/件/条/项。
	ordinalPattern = regexp.MustCompile(`第\s*([一二两三四五六七八九十\d]+)\s*(个|款|件|条|项|台|张|单|笔)?`)
	// 数量：N 件/个/台/副/只/份/套/张。
	quantityPattern = regexp.MustCompile(`([一二两三四五六七八九十\d]+)\s*(件|个|台|副|只|份|套|张|支|瓶|盒)`)
	// 评分：N 星 / N 分 / 打 N。
	ratingPattern = regexp.MustCompile(`([一二三四五12345])\s*(星|分|颗星)`)
	// 订单号：BS + 时间 + 6 位，或订单 ID。
	orderNoPattern = regexp.MustCompile(`\b(BS\d{10,24}|o_[A-Za-z0-9_]{4,})\b`)
	// 商品 ID。
	productIDPattern = regexp.MustCompile(`\b(p_[A-Za-z0-9_]{4,})\b`)
)

// ChineseNumber 把中文数字（三千、两千五、一万二、十五）或阿拉伯数字转成整数；无法解析返回 false。
// 支持“两千五”这类省略单位的写法（按上一级单位的十分之一补齐）。
func ChineseNumber(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int(f + 0.5), true
	}
	digits := map[rune]int{'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	units := map[rune]int{'十': 10, '百': 100, '千': 1000, '万': 10000}
	total, section, digit, lastUnit := 0, 0, 0, 0
	seenDigit := false
	for _, r := range s {
		if d, ok := digits[r]; ok {
			digit, seenDigit = d, true
			continue
		}
		u, ok := units[r]
		if !ok {
			return 0, false
		}
		if u == 10000 {
			total = (total + section + digit) * u
			section, digit, lastUnit = 0, 0, u
			continue
		}
		if digit == 0 && !seenDigit && u == 10 {
			digit = 1 // “十五”
		}
		if digit == 0 && seenDigit {
			digit = 1 // “千”单独出现按一千
		}
		if digit == 0 {
			digit = 1
		}
		section += digit * u
		digit, lastUnit = 0, u
	}
	if digit > 0 && lastUnit > 0 {
		digit *= lastUnit / 10 // “两千五” → 2500
	}
	return total + section + digit, true
}

// ParseBudget 从句子里找价格上限（元）。“左右/上下”放宽 15%；单位 k/千 乘 1000。找不到返回 0。
func ParseBudget(text string) domain.Money {
	num, unit, rangeWord := "", "", ""
	if m := budgetTail.FindStringSubmatch(text); m != nil {
		num, unit, rangeWord = m[1], m[2], m[3]
	} else if m := budgetHead.FindStringSubmatch(text); m != nil {
		num, unit = m[3], m[4]
	}
	if num == "" {
		return 0
	}
	n, ok := ChineseNumber(num)
	if !ok || n <= 0 {
		return 0
	}
	if unit == "k" || unit == "K" || unit == "千" {
		n *= 1000
	}
	m := domain.Money(n * 100)
	if rangeWord == "左右" || rangeWord == "上下" {
		m = m * 115 / 100
	}
	return m
}

// ParseExclusions 找出用户明确排除的品牌、属性或商品词。
func ParseExclusions(text string) []string {
	var out []string
	for _, m := range excludePattern.FindAllStringSubmatch(text, -1) {
		term := strings.TrimSpace(m[1])
		if term != "" && !containsStr(out, term) {
			out = append(out, term)
		}
	}
	return out
}

// stripExclusions 把排除短语从句子中去掉，避免被当作正向需求。
func stripExclusions(text string) string {
	return excludePattern.ReplaceAllString(text, " ")
}

// stripBudget 去掉预算短语。
func stripBudget(text string) string {
	text = budgetTail.ReplaceAllString(text, " ")
	return budgetHead.ReplaceAllString(text, " ")
}

// ParseOrdinal 解析“第 N 个”“最后一个”“第一款”：返回 1 起的序号，“最后”为 -1，没有返回 0。
func ParseOrdinal(text string) int {
	if strings.Contains(text, "最后") {
		return -1
	}
	if m := ordinalPattern.FindStringSubmatch(text); m != nil {
		if n, ok := ChineseNumber(m[1]); ok && n > 0 {
			return n
		}
	}
	switch {
	case strings.Contains(text, "首个"), strings.Contains(text, "头一个"):
		return 1
	}
	return 0
}

// ParseQuantity 解析“两件”“3 个”，没有返回 0。“第 N 个”里的数字不算数量。
func ParseQuantity(text string) int {
	text = ordinalPattern.ReplaceAllString(text, " ")
	for _, m := range quantityPattern.FindAllStringSubmatch(text, -1) {
		if n, ok := ChineseNumber(m[1]); ok && n > 0 {
			return n
		}
	}
	return 0
}

// ParseRating 解析“五星”“4 分”“打 5 星”，没有返回 0。
func ParseRating(text string) int {
	if m := ratingPattern.FindStringSubmatch(text); m != nil {
		if n, ok := ChineseNumber(m[1]); ok && n >= 1 && n <= 5 {
			return n
		}
	}
	return 0
}

// ParseOrderRef 找出句子里的订单号或订单 ID。
func ParseOrderRef(text string) string {
	if m := orderNoPattern.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// ParseProductIDs 找出句子里显式写出的商品 ID。
func ParseProductIDs(text string) []string {
	var out []string
	for _, m := range productIDPattern.FindAllStringSubmatch(text, -1) {
		if !containsStr(out, m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// QueryTerms 把一句话变成商品检索词：去掉预算、排除短语和口头语，按“汉字串 / 字母数字串”切开，
// 保留 ≥2 个字符的片段（型号等字母数字串允许 1 个字符以上的组合），去重，最多 8 个。
func QueryTerms(text string) []string {
	text = stripExclusions(stripBudget(text))
	lower := strings.ToLower(text)
	for _, f := range fillers {
		lower = strings.ReplaceAll(lower, strings.ToLower(f), " ")
	}
	var terms []string
	seen := map[string]bool{}
	add := func(run []rune, han bool) {
		if len(run) == 0 {
			return
		}
		s := string(run)
		if (han && len(run) < 2) || (!han && len(run) < 2) || seen[s] {
			return
		}
		seen[s] = true
		terms = append(terms, s)
	}
	var run []rune
	runHan := false
	for _, r := range lower {
		isHan := unicode.Is(unicode.Han, r)
		if !isHan && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			add(run, runHan)
			run = nil
			continue
		}
		if len(run) > 0 && isHan != runHan {
			add(run, runHan)
			run = nil
		}
		runHan = isHan
		run = append(run, r)
	}
	add(run, runHan)
	if len(terms) > 8 {
		terms = terms[:8]
	}
	return terms
}

// containsAny 判断文本是否包含任一关键词。
func containsAny(text string, words ...string) bool {
	for _, w := range words {
		if w != "" && strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// firstMatch 返回文本中出现的第一个关键词（按给定顺序）。
func firstMatch(text string, words ...string) string {
	for _, w := range words {
		if strings.Contains(text, w) {
			return w
		}
	}
	return ""
}

// compact 去掉空白和标点，用于判断整句是否就是一个固定短语（打招呼等）。
func compact(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
