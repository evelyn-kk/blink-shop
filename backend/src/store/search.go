package store

import (
	"strings"
	"unicode"
)

const maxSearchTerms = 10

// SearchTerms 把关键词拆成检索词（与上游一致）：完整关键词，加上按“汉字 / 字母数字”切分后的片段；
// 汉字片段再展开为 2–4 字的 n-gram。全部转小写、去重、至少 2 个字符，最多 10 个。
// 商品只要名称、品牌、分类名、标签或卖点包含任一检索词即命中。
func SearchTerms(keyword string) []string {
	var terms []string
	seen := map[string]bool{}
	add := func(term string) {
		term = strings.ToLower(strings.TrimSpace(term))
		if len([]rune(term)) < 2 || seen[term] || len(terms) >= maxSearchTerms {
			return
		}
		seen[term] = true
		terms = append(terms, term)
	}
	add(keyword)

	var run []rune
	runIsHan := false
	flush := func() {
		if len(run) == 0 {
			return
		}
		if runIsHan {
			if len(run) <= 4 {
				add(string(run))
			}
			for size := 2; size <= 4; size++ {
				for i := 0; i+size <= len(run); i++ {
					add(string(run[i : i+size]))
				}
			}
		} else {
			add(string(run))
		}
		run = nil
	}
	for _, r := range keyword {
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
	return terms
}
