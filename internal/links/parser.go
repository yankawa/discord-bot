// Package links contains the platform-independent FxTwitter bot policy.
package links

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	urlPattern    = regexp.MustCompile(`(?i)https?://[^\s<>"` + "`" + `]+`)
	statusPath    = regexp.MustCompile(`^/([A-Za-z0-9_]{1,15})/status/([0-9]{1,20})(?:/(?:photo|video)/[1-9][0-9]*)?/?$`)
	webStatusPath = regexp.MustCompile(`^/i/web/status/([0-9]{1,20})/?$`)
)

// Extract returns at most maxLinks unique, canonical FxTwitter status links.
// Markdown code, spoilers and angle-bracket links are intentional opt-outs.
// Tracking queries and photo/video suffixes are removed; deduplication uses the
// numeric status ID, so aliases of the same post produce only one preview.
func Extract(content string, maxLinks int) []string {
	if maxLinks <= 0 {
		return nil
	}
	visible := visibleText(content)
	seen := make(map[string]bool)
	var result []string
	for _, raw := range urlPattern.FindAllString(visible, -1) {
		// These characters can follow a URL in prose or Markdown. Valid status
		// paths never need them, so trimming cannot alter a supported post ID.
		raw = strings.TrimRight(raw, ".,!?;:)]}*~。､，、！？\"'”’）】」』〉》")
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || u.Port() != "" || u.Opaque != "" {
			continue
		}
		if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
			continue
		}
		switch strings.ToLower(u.Host) {
		case "x.com", "www.x.com", "mobile.x.com", "twitter.com", "www.twitter.com", "mobile.twitter.com":
		default:
			continue
		}
		// Reject encoded path tricks rather than interpreting a broader URL
		// grammar than the six supported status URL forms.
		if strings.Contains(u.EscapedPath(), "%") {
			continue
		}
		var user, id string
		if match := statusPath.FindStringSubmatch(u.Path); match != nil {
			user, id = match[1], match[2]
		} else if match := webStatusPath.FindStringSubmatch(u.Path); match != nil {
			user, id = "i", match[1]
		} else {
			continue
		}
		if !validID(id) || seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, "https://fxtwitter.com/"+user+"/status/"+id)
		if len(result) == maxLinks {
			break
		}
	}
	return result
}

func validID(id string) bool {
	if id == "" || id[0] == '0' {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n != 0
}

// Replace excluded Markdown regions with spaces without changing byte offsets.
// A deliberately unclosed code/spoiler/angle region opts out through end-of-text.
// Conservative exclusion avoids revealing content the author meant to conceal.
func visibleText(content string) string {
	b := []byte(content)
	for i := 0; i < len(content); {
		if content[i] == '`' {
			start := i
			for i < len(content) && content[i] == '`' {
				i++
			}
			run := i - start
			end := len(content)
			for j := i; j < len(content); {
				if content[j] != '`' {
					j++
					continue
				}
				k := j
				for k < len(content) && content[k] == '`' {
					k++
				}
				if k-j == run {
					end = k
					break
				}
				j = k
			}
			blank(b, start, end)
			i = end
			continue
		}
		if content[i] == '<' {
			end := len(content)
			if n := strings.IndexByte(content[i+1:], '>'); n >= 0 {
				end = i + 1 + n + 1
			}
			blank(b, i, end)
			i = end
			continue
		}
		if strings.HasPrefix(content[i:], "||") {
			end := len(content)
			if n := strings.Index(content[i+2:], "||"); n >= 0 {
				end = i + 2 + n + 2
			}
			blank(b, i, end)
			i = end
			continue
		}
		i++
	}
	return string(b)
}

func blank(b []byte, start, end int) {
	for i := start; i < end; i++ {
		b[i] = ' '
	}
}
