package links

import (
	"reflect"
	"testing"
)

func TestExtractCanonicalURLs(t *testing.T) {
	tests := []struct {
		name, input string
		want        []string
	}{
		{"plain", "https://x.com/alice/status/123", []string{"https://fxtwitter.com/alice/status/123"}},
		{"twitter tracking", "https://twitter.com/Alice_123/status/123?s=20&t=abc#fragment", []string{"https://fxtwitter.com/Alice_123/status/123"}},
		{"www and http", "HTTP://WWW.X.COM/u/status/234", []string{"https://fxtwitter.com/u/status/234"}},
		{"mobile photo", "https://mobile.twitter.com/u/status/345/photo/2", []string{"https://fxtwitter.com/u/status/345"}},
		{"mobile video", "https://mobile.x.com/u/status/345/video/1/", []string{"https://fxtwitter.com/u/status/345"}},
		{"web status", "https://x.com/i/web/status/456", []string{"https://fxtwitter.com/i/status/456"}},
		{"i status", "https://x.com/i/status/567", []string{"https://fxtwitter.com/i/status/567"}},
		{"markdown", "[post](https://x.com/u/status/678).", []string{"https://fxtwitter.com/u/status/678"}},
		{"Japanese punctuation", "これ（https://x.com/u/status/789）。", []string{"https://fxtwitter.com/u/status/789"}},
		{"max ID", "https://x.com/u/status/18446744073709551615", []string{"https://fxtwitter.com/u/status/18446744073709551615"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Extract(tc.input, 5); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Extract() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestExtractRejectsUnsupportedAndSpoofedURLs(t *testing.T) {
	inputs := []string{
		"https://x.com/u", "https://x.com/u/status", "https://x.com/u/status/0",
		"https://x.com/u/status/0123", "https://x.com/u/status/18446744073709551616",
		"https://x.com/u/status/123abc", "https://x.com/u/status/123/other",
		"https://x.com/u/status/123/photo/0", "https://x.com/abcdefghijklmnop/status/123",
		"https://x.com.evil.test/u/status/123", "https://evilx.com/u/status/123",
		"https://x.com@evil.test/u/status/123", "https://evil@x.com/u/status/123",
		"https://x.com:443/u/status/123", "https://x.com./u/status/123",
		"https://x.com/u%2Fstatus/123", "https://x.com/u/status/%31%32%33",
		"https://fxtwitter.com/u/status/123", "ftp://x.com/u/status/123",
		"https://x.com/u/status/123\\@evil.test", "https://x.com/u/status/123%0A@everyone",
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			if got := Extract(input, 5); len(got) != 0 {
				t.Fatalf("unexpected supported URL: %#v", got)
			}
		})
	}
}

func TestExtractHonorsMarkdownOptOut(t *testing.T) {
	u := "https://x.com/u/status/123"
	inputs := []string{
		"`" + u + "`", "``" + u + "``", "```text\n" + u + "\n```",
		"`unclosed " + u, "````\n" + u + "\n````", "||" + u + "||",
		"||unclosed " + u, "<" + u + ">", "<unclosed " + u,
		"||[hidden](" + u + ")||", "``literal ` " + u + "``",
	}
	for _, input := range inputs {
		if got := Extract(input, 5); len(got) != 0 {
			t.Errorf("opt-out exposed %q: %#v", input, got)
		}
	}
	input := "`" + u + "` ||" + u + "|| <" + u + "> https://x.com/v/status/456"
	want := []string{"https://fxtwitter.com/v/status/456"}
	if got := Extract(input, 5); !reflect.DeepEqual(got, want) {
		t.Fatalf("visible URL after opt-outs = %#v", got)
	}
}

func TestExtractDeduplicatesStatusIDAndLimits(t *testing.T) {
	input := "https://x.com/u/status/123 https://twitter.com/v/status/123?x=1 https://x.com/v/status/234 https://x.com/w/status/345"
	want := []string{"https://fxtwitter.com/u/status/123", "https://fxtwitter.com/v/status/234"}
	if got := Extract(input, 2); !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract() = %#v", got)
	}
	if got := Extract(input, 0); len(got) != 0 {
		t.Fatalf("zero limit = %#v", got)
	}
}
