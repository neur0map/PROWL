package gateway

import (
	"strconv"
	"strings"
)

// Request shape (token count, tool presence, message count) is only a proxy
// for difficulty, and it is systematically wrong in two directions. A short
// hard question — "prove there is no closed form for the Collatz stopping
// time" — looks trivial by shape and would be answered on a small model. A
// huge mechanical paste — "extract the timestamps from this 40k-token log" —
// looks demanding by shape and would burn a frontier model. Reading the verbs
// in the last user message corrects both, deterministically and without a
// network round trip.

// intentScanLimit bounds how much of the message text is inspected, split
// between the start and the end. The instruction sits at one end or the
// other -- a request either opens with the ask or appends it after a paste --
// and the middle is the pasted bulk, so a bounded slice from each end keeps
// the per-request cost flat without missing the verbs.
const intentScanLimit = 4096

// intentSignal is the compact lexical read of a request. It travels as one
// struct rather than a bag of booleans threaded through the router.
type intentSignal struct {
	// reasoning is set when the words ask for analysis, derivation, or design
	// — work a small model tends to get wrong.
	reasoning bool
	// mechanical is set when the words describe a rote transformation —
	// rename, list, extract, translate — that a small model handles fine.
	mechanical bool
	// requirements counts the distinct things the message asks for:
	// enumerations plus question marks. Several asks in one turn is more to
	// hold in one head than a single ask.
	requirements int
	// fencedCode marks a fenced code block, a hint that real code is on the
	// table rather than prose.
	fencedCode bool
}

// reasoningCues name intent that a small model answers badly. Base imperative
// forms are used because that is how prompts phrase a request ("Refactor
// this", "Design a", "Debug the").
var reasoningCues = []string{
	"prove", "derive", "design", "architect", "refactor", "optimise",
	"optimize", "debug", "diagnose", "why does", "why is", "why are",
	"trade-off", "tradeoff", "trade off", "compare approaches", "root cause",
	"reason about", "explain why", "figure out why",
}

// mechanicalCues name intent a small model handles fine, so a big paste driven
// by one is bulk rather than difficulty.
var mechanicalCues = []string{
	"rename", "list", "format", "translate", "what is", "what are",
	"summarise", "summarize", "extract", "convert", "tidy",
}

// analyzeIntent derives the lexical signal from the last user message. It is on
// every request, so it lowercases at most a bounded leading slice and scans it
// once.
func analyzeIntent(text string) intentSignal {
	if text == "" {
		return intentSignal{}
	}
	low := strings.ToLower(intentWindow(text))
	return intentSignal{
		reasoning:    containsAnyWord(low, reasoningCues),
		mechanical:   containsAnyWord(low, mechanicalCues),
		requirements: countRequirements(low),
		fencedCode:   strings.Contains(low, "```"),
	}
}

// intentWindow keeps the two ends of s and drops the middle, which is the
// pasted bulk. It is the single definition of the scan window: callers that
// bound text before storing it and the analyzer itself must agree, or one
// will discard the end the other was relying on.
//
// Applying it twice is a no-op: a window is one byte over the limit, and the
// slack here lets that pass through untouched.
func intentWindow(s string) string {
	if len(s) <= intentScanLimit+1 {
		return s
	}
	half := intentScanLimit / 2
	// The newline stops a word being spliced across the cut into a false match.
	return s[:half] + "\n" + s[len(s)-half:]
}

// describe renders the signal for the routing log so a reader can see which
// lexical features fired, in the same one-line style as the rest of the
// reason. It returns "" when nothing notable fired.
func (s intentSignal) describe() string {
	var parts []string
	if s.reasoning {
		parts = append(parts, "reasoning")
	}
	if s.mechanical {
		parts = append(parts, "mechanical")
	}
	if s.fencedCode {
		parts = append(parts, "code")
	}
	if s.requirements >= 3 {
		parts = append(parts, strconv.Itoa(s.requirements)+" reqs")
	}
	if len(parts) == 0 {
		return ""
	}
	return "lexical " + strings.Join(parts, "+")
}

// containsAnyWord reports whether any needle appears in hay as a whole word.
func containsAnyWord(hay string, needles []string) bool {
	for _, n := range needles {
		if containsWord(hay, n) {
			return true
		}
	}
	return false
}

// containsWord reports whether needle occurs in hay bounded by non-letter,
// non-digit characters, so "list" does not fire on "listen" and "prove" does
// not fire on "approved". hay is assumed already lowercased. Phrases with an
// internal space match on their outer boundaries.
func containsWord(hay, needle string) bool {
	for from := 0; ; {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		if wordBoundary(hay, i-1) && wordBoundary(hay, i+len(needle)) {
			return true
		}
		from = i + 1
	}
}

// wordBoundary reports whether position i is outside the string or holds a
// character that ends a word.
func wordBoundary(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return true
	}
	c := s[i]
	return !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9')
}

// countRequirements counts question marks plus list items (bullets and
// numbered entries at a line start), a cheap stand-in for "how many distinct
// things is this asking for". It scans the bounded slice without allocating.
func countRequirements(low string) int {
	count := strings.Count(low, "?")
	atLineStart := true
	for i := range len(low) {
		c := low[i]
		if c == '\n' {
			atLineStart = true
			continue
		}
		if !atLineStart {
			continue
		}
		if c == ' ' || c == '\t' {
			continue // Skip indentation before a marker.
		}
		if enumerationMarker(low, i) {
			count++
		}
		atLineStart = false
	}
	return count
}

// enumerationMarker reports whether index i begins a bullet ("- ", "* ") or a
// numbered item ("1.", "2)").
func enumerationMarker(s string, i int) bool {
	c := s[i]
	if (c == '-' || c == '*') && i+1 < len(s) && s[i+1] == ' ' {
		return true
	}
	if c >= '0' && c <= '9' {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j < len(s) && (s[j] == '.' || s[j] == ')') {
			return true
		}
	}
	return false
}
