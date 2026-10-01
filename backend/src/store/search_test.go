package store

import (
	"reflect"
	"testing"
)

func TestSearchTerms(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", nil},
		{"  Mouse ", []string{"mouse"}},
		{"降噪耳机", []string{"降噪耳机", "降噪", "噪耳", "耳机", "降噪耳", "噪耳机"}},
		{"Blink耳机", []string{"blink耳机", "blink", "耳机"}},
		{"静音 鼠标", []string{"静音 鼠标", "静音", "鼠标"}},
		{"100%_off", []string{"100%_off", "100", "off"}},
	}
	for _, c := range cases {
		if got := SearchTerms(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("SearchTerms(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := SearchTerms("一二三四五六七八九十甲乙丙丁"); len(got) != maxSearchTerms {
		t.Errorf("terms not capped: %d", len(got))
	}
}
