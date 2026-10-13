package proxy

import "strings"

// Only withhold a suffix that could become a split thinking tag.
func streamTextEnd(text, tag string, flush bool) int {
	if !flush {
		for n := min(len(text), len(tag)-1); n > 0; n-- {
			if strings.HasSuffix(text, tag[:n]) {
				return len(text) - n
			}
		}
	}
	return len(text)
}
