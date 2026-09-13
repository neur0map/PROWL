// Package githubref resolves explicit GitHub PR and issue references.
package githubref

import (
	"regexp"
	"strconv"
	"strings"
)

var mention = regexp.MustCompile("(?i)(?:^|[\\s\"'`(<=])(?:(pr|pull|issue)(\\s+))?#([1-9][0-9]*)$")

type Candidate struct {
	Label string
	URI   string
}

// Complete recognizes a standalone #number ending at the cursor. It makes no
// network requests. A qualifier selects one kind; otherwise PR is first.
// Start is a byte offset into the supplied editor prefix.
func Complete(prefix string) (start int, candidates []Candidate) {
	m := mention.FindStringSubmatchIndex(prefix)
	if m == nil {
		return 0, nil
	}
	number := prefix[m[6]:m[7]]
	if _, err := strconv.ParseInt(number, 10, 64); err != nil {
		return 0, nil
	}
	start = m[6] - 1
	qualifier := ""
	if m[2] >= 0 {
		start = m[2]
		qualifier = strings.ToLower(prefix[m[2]:m[3]])
	}
	if qualifier != "issue" {
		candidates = append(candidates, Candidate{Label: "PR #" + number, URI: "pr://" + number})
	}
	if qualifier != "pr" && qualifier != "pull" {
		candidates = append(candidates, Candidate{Label: "Issue #" + number, URI: "issue://" + number})
	}
	return start, candidates
}
