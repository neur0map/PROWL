package context

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	indexquery "github.com/neur0map/prowl/internal/paengine/internal/query"
	"github.com/neur0map/prowl/internal/paengine/internal/store"
)

func symbolCandidates(target *store.Store, root, question string) ([]Candidate, error) {
	if target == nil {
		return nil, nil
	}
	var out []Candidate
	querier := indexquery.New(target)
	terms := queryTerms(question)
	seen := map[int64]bool{}
	for _, name := range preciseNames(question) {
		hits, err := target.SymbolsByName(name, 12)
		if err != nil {
			return nil, err
		}
		for _, hit := range hits {
			if seen[hit.ID] {
				continue
			}
			seen[hit.ID] = true
			definition, err := querier.Definition(root, strconv.FormatInt(hit.ID, 10))
			if err != nil {
				return nil, err
			}
			out = append(out, definitionCandidate(hit.ID, definition, terms))
			if len(out) == 12 {
				return out, nil
			}
		}
	}
	return out, nil
}

func preciseNames(question string) []string {
	var names []string
	add := func(name string) {
		name = strings.Trim(name, "`\"'.,:;!?()[]{}")
		for i, r := range name {
			if !unicode.IsLetter(r) && r != '_' && (i == 0 || !unicode.IsDigit(r)) {
				return
			}
		}
		if name != "" {
			names = append(names, name)
		}
	}
	words := strings.Fields(question)
	if len(words) == 1 {
		add(words[0])
	}
	for i, quoted := range strings.Split(question, "`") {
		if i%2 == 1 {
			add(quoted)
		}
	}
	for _, word := range words {
		for i, r := range word {
			if r == '_' || i > 0 && unicode.IsUpper(r) {
				add(word)
				break
			}
		}
	}
	return uniqueStrings(names)
}

func definitionCandidate(id int64, definition indexquery.Definition, terms []string) Candidate {
	candidate := sourceCandidate(definition.File, definition.LineStart, definition.LineEnd, definition.Signature, definition.Code, terms, "exact named symbol")
	digest := sha256.Sum256([]byte(definition.File + ":" + strconv.Itoa(definition.LineStart) + "\n" + definition.Code))
	candidate.ID = fmt.Sprintf("symbol:%d:%x", id, digest[:16])
	candidate.Title += "#" + definition.Name
	candidate.DirectMatch = true
	if definition.Truncated {
		last := definition.LineStart + strings.Count(definition.Code, "\n")
		candidate.WhySelected = append(candidate.WhySelected, fmt.Sprintf("Source excerpt ends at line %d; recover remaining lines with peek %s:%d-%d", last, definition.File, last+1, definition.LineEnd))
	}
	return candidate
}

func (service *Service) symbolByID(id string) (Candidate, bool, error) {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) != 3 || service.Store == nil {
		return Candidate{}, false, nil
	}
	number, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || number <= 0 {
		return Candidate{}, false, nil
	}
	definition, err := indexquery.New(service.Store).Definition(service.Root, parts[1])
	if err != nil {
		return Candidate{}, false, err
	}
	candidate := definitionCandidate(number, definition, nil)
	return candidate, candidate.ID == id, nil
}
