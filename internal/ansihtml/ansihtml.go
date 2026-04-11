// Package ansihtml converts text containing ANSI SGR escape sequences into
// HTML where styling is applied via <span class="ansi-..."> wrappers.
//
// Only a minimal set of SGR parameters is recognized (reset, bold, dim,
// 22 normal intensity, 30-37/39 foreground colors). Other CSI sequences
// are stripped. All non-escape content is HTML-escaped.
package ansihtml

import (
	"html"
	"slices"
	"strconv"
	"strings"
)

// Convert returns s rewritten so that recognized ANSI SGR sequences become
// nested <span class="..."> wrappers. Unrecognized CSI sequences are
// dropped and all literal text is HTML-escaped. The returned string is
// safe to inline inside a <pre> element.
func Convert(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 32)

	var open []string
	closeSpan := func() {
		if len(open) > 0 {
			b.WriteString("</span>")
		}
	}
	openSpan := func() {
		if len(open) > 0 {
			b.WriteString(`<span class="`)
			b.WriteString(strings.Join(open, " "))
			b.WriteString(`">`)
		}
	}

	i := 0
	for i < len(s) {
		if s[i] != 0x1b {
			j := i
			for j < len(s) && s[j] != 0x1b {
				j++
			}
			b.WriteString(html.EscapeString(s[i:j]))
			i = j
			continue
		}
		if i+1 >= len(s) {
			i++
			continue
		}
		if s[i+1] != '[' {
			i += 2
			continue
		}
		end := i + 2
		for end < len(s) {
			c := s[end]
			if c >= 0x40 && c <= 0x7e {
				break
			}
			end++
		}
		if end >= len(s) {
			i = end
			continue
		}
		term := s[end]
		params := s[i+2 : end]
		i = end + 1
		if term != 'm' {
			continue
		}
		closeSpan()
		if params == "" {
			open = nil
			openSpan()
			continue
		}
		for p := range strings.SplitSeq(params, ";") {
			n, err := strconv.Atoi(p)
			if err != nil {
				continue
			}
			open = applySGR(open, n)
		}
		openSpan()
	}
	closeSpan()
	return b.String()
}

var fgNames = [...]string{
	"ansi-fg-black", "ansi-fg-red", "ansi-fg-green", "ansi-fg-yellow",
	"ansi-fg-blue", "ansi-fg-magenta", "ansi-fg-cyan", "ansi-fg-white",
}

func applySGR(open []string, n int) []string {
	switch {
	case n == 0:
		return nil
	case n == 1:
		return addClass(open, "ansi-bold")
	case n == 2:
		return addClass(open, "ansi-dim")
	case n == 22:
		return removeClasses(open, "ansi-bold", "ansi-dim")
	case n == 39:
		return removeClasses(open, fgNames[:]...)
	case n >= 30 && n <= 37:
		open = removeClasses(open, fgNames[:]...)
		return addClass(open, fgNames[n-30])
	}
	return open
}

func addClass(open []string, class string) []string {
	if slices.Contains(open, class) {
		return open
	}
	return append(open, class)
}

func removeClasses(open []string, classes ...string) []string {
	out := open[:0]
	for _, c := range open {
		if !slices.Contains(classes, c) {
			out = append(out, c)
		}
	}
	return out
}
