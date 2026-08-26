package meeting

import "testing"

func TestDetectSpokenLanguage(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"我们开始讨论明年预算。", "zh-CN"},
		{"我哋而家喺会议室倾下预算嘅事。", "yue"},
		{"Let's start with the budget review tomorrow.", "en"},
		{"", "zh-CN"},
	}
	for _, tc := range cases {
		if got := DetectSpokenLanguage(tc.text); got != tc.want {
			t.Fatalf("DetectSpokenLanguage(%q)=%s want %s", tc.text, got, tc.want)
		}
	}
}

func TestLanguageOrDetectKeepsExplicit(t *testing.T) {
	if got := languageOrDetect("en", "我们开会"); got != "en" {
		t.Fatalf("explicit language should win, got %s", got)
	}
	if got := languageOrDetect("", "Hello everyone, thanks for joining."); got != "en" {
		t.Fatalf("empty language should be detected, got %s", got)
	}
}
