package risk

import (
	"context"
	"testing"
)

func TestWordList(t *testing.T) {
	ctx := context.Background()
	c := Static("假货", "绕过风控", "Scam")
	for _, tc := range []struct {
		text string
		want string
	}{
		{"有没有假货", "假货"},
		{"怎么 绕过  风控", "绕过风控"},
		{"this is a SCAM", "Scam"},
		{"推荐一款耳机", ""},
		{"", ""},
	} {
		m, hit := c.Check(ctx, tc.text)
		if hit != (tc.want != "") || m.Word != tc.want {
			t.Fatalf("%q: got %+v %v, want %q", tc.text, m, hit, tc.want)
		}
	}
	// 词表为空或没有来源时不拦截
	if _, hit := Static().Check(ctx, "假货"); hit {
		t.Fatal("empty list should not match")
	}
	if _, hit := (WordList{}).Check(ctx, "假货"); hit {
		t.Fatal("nil source should not match")
	}
	if got := Split(" 违法, ,假货 ,"); len(got) != 2 || got[0] != "违法" || got[1] != "假货" {
		t.Fatalf("split %v", got)
	}
}
