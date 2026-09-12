// Prose matching is adapted from Oh My Pi. See NOTICE.md for its MIT notice.

package reasoning

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Span identifies a half-open byte range in the original prompt.
type Span struct {
	Start int
	End   int
}

// HasUltrathink reports whether the exact lowercase keyword occurs in prose.
// Code, markup, paths and identifiers never request extra reasoning.
func HasUltrathink(text string) bool {
	if !strings.Contains(text, Ultrathink) {
		return false
	}
	return nextUltrathink(text, 0, nonProse(text)) >= 0
}

// UltrathinkSpans returns the same matches used for execution, for highlighting
// without inserting control bytes into the user's actual prompt.
func UltrathinkSpans(text string) []Span {
	if !strings.Contains(text, Ultrathink) {
		return nil
	}
	masked := nonProse(text)
	var spans []Span
	for from := 0; from < len(text); {
		start := nextUltrathink(text, from, masked)
		if start < 0 {
			break
		}
		end := start + len(Ultrathink)
		spans = append(spans, Span{Start: start, End: end})
		from = end
	}
	return spans
}

func nextUltrathink(text string, from int, masked []bool) int {
	for from < len(text) {
		i := strings.Index(text[from:], Ultrathink)
		if i < 0 {
			return -1
		}
		i += from
		end := i + len(Ultrathink)
		from = end
		if masked != nil && masked[i] {
			continue
		}
		if i > 0 {
			r, _ := utf8.DecodeLastRuneInString(text[:i])
			if wordRune(r) || strings.ContainsRune("./\\-", r) || strings.HasSuffix(text[:i], "::") {
				continue
			}
		}
		if end < len(text) {
			r, size := utf8.DecodeRuneInString(text[end:])
			if wordRune(r) || strings.ContainsRune("/\\-(", r) {
				continue
			}
			if r == '.' {
				next, _ := utf8.DecodeRuneInString(text[end+size:])
				if wordRune(next) || next == '-' {
					continue
				}
			}
		}
		return i
	}
	return -1
}

func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_'
}

// nonProse masks byte positions only when the prompt contains markup syntax.
// Fences are masked first so their backticks cannot close an inline code span.
func nonProse(text string) []bool {
	if !strings.ContainsAny(text, "`<") && !strings.Contains(text, "~~~") {
		return nil
	}
	masked := make([]bool, len(text))
	var fence byte
	var fenceLen int
	for start := 0; start < len(text); {
		end := strings.IndexByte(text[start:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += start
		}
		line := text[start:end]
		indent := 0
		for indent < len(line) && line[indent] == ' ' {
			indent++
		}
		var marker byte
		var count int
		if indent <= 3 && indent < len(line) && (line[indent] == '`' || line[indent] == '~') {
			marker = line[indent]
			for indent+count < len(line) && line[indent+count] == marker {
				count++
			}
		}
		if fence != 0 {
			maskRange(masked, start, end)
			if marker == fence && count >= fenceLen && strings.TrimSpace(line[indent+count:]) == "" {
				fence = 0
			}
		} else if count >= 3 && !(marker == '`' && strings.ContainsRune(line[indent+count:], '`')) {
			fence, fenceLen = marker, count
			maskRange(masked, start, end)
		}
		start = end + 1
	}
	for i := 0; i < len(text); {
		if masked[i] {
			i++
			continue
		}
		switch text[i] {
		case '`':
			end := tickEnd(text, i)
			close := codeClose(text, end, end-i, masked)
			if close >= 0 {
				maskRange(masked, i, close)
				i = close
			} else {
				i = end
			}
		case '<':
			end := markupEnd(text, i, masked)
			maskRange(masked, i, end)
			i = max(i+1, end)
		default:
			i++
		}
	}
	return masked
}

func maskRange(masked []bool, start, end int) {
	for i := start; i < end; i++ {
		masked[i] = true
	}
}

func tickEnd(text string, start int) int {
	for start < len(text) && text[start] == '`' {
		start++
	}
	return start
}

func codeClose(text string, from, count int, masked []bool) int {
	for i := from; i < len(text); {
		if masked[i] || text[i] != '`' {
			i++
			continue
		}
		end := tickEnd(text, i)
		if end-i == count {
			return end
		}
		i = end
	}
	return -1
}

// tagAt parses a single tag, respecting quoted attributes. An unmatched opening
// tag masks only itself, so a literal less-than cannot hide the remaining prose.
func tagAt(text string, start int) (name string, end int, closing, selfClosing bool) {
	i := start + 1
	if i < len(text) && text[i] == '/' {
		closing = true
		i++
	}
	nameStart := i
	if i >= len(text) || !asciiLetter(text[i]) {
		return "", start, false, false
	}
	for i < len(text) && (asciiLetter(text[i]) || text[i] >= '0' && text[i] <= '9' || text[i] == '-') {
		i++
	}
	name = text[nameStart:i]
	var quote byte
	for ; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '<':
			return "", start, false, false
		case '>':
			return name, i + 1, closing, text[i-1] == '/'
		}
	}
	return "", start, false, false
}

func asciiLetter(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

func markupEnd(text string, start int, masked []bool) int {
	if strings.HasPrefix(text[start:], "<!--") {
		end := strings.Index(text[start+4:], "-->")
		if end < 0 {
			return len(text)
		}
		return start + 4 + end + 3
	}
	name, end, closing, selfClosing := tagAt(text, start)
	if name == "" || closing || selfClosing {
		return end
	}
	depth := 1
	for i := end; i < len(text); {
		if masked[i] || text[i] != '<' {
			i++
			continue
		}
		nextName, nextEnd, nextClosing, nextSelfClosing := tagAt(text, i)
		if nextName == "" {
			i++
			continue
		}
		if strings.EqualFold(name, nextName) {
			if nextClosing {
				depth--
				if depth == 0 {
					return nextEnd
				}
			} else if !nextSelfClosing {
				depth++
			}
		}
		i = nextEnd
	}
	return end
}
