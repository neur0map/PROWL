package gateway

import (
	"regexp"
	"slices"
	"strings"

	"github.com/neur0map/prowl/internal/gateway/catalog"
)

// Deployments are derived, not configured. A user who pastes one API key has

// smallModelHints are the naming conventions vendors use for their cheap,
// fast tier. Model naming is the only signal available without a per-model
// capability table, and vendors are remarkably consistent about it.
var smallModelHints = []string{
	"flash", "mini", "small", "tiny", "nano", "lite", "haiku", "instant",
	"turbo", "fast", "8b", "7b", "4b", "3b", "1b", "0.5b",
}

// paramSize matches a parameter count in a model id, such as "70b".
var paramSize = regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)b\b`)

// modelParams reads the parameter count a model states in its name, in
// billions. Zero means the name does not say.
func modelParams(id, name string) float64 {
	if m := paramSize.FindStringSubmatch(strings.ToLower(id + " " + name)); m != nil {
		return parseFloat(m[1])
	}
	return 0
}

// classifyModel guesses whether a model belongs to the cheap or the capable
func classifyModel(id, name string) ModelClass {
	hay := strings.ToLower(id + " " + name)
	if m := paramSize.FindStringSubmatch(hay); m != nil {
		// A parameter count is the strongest signal there is: anything at or
		// under about 9B is a small model regardless of its marketing name.
		size := parseFloat(m[1])
		if size > 0 && size <= 9 {
			return ClassSmall
		}
		if size >= 30 {
			return ClassLarge
		}
	}
	for _, hint := range smallModelHints {
		if strings.Contains(hay, hint) {
			return ClassSmall
		}
	}
	return ClassLarge
}

func parseFloat(s string) float64 {
	var whole, frac float64
	var fracDigits int
	seenDot := false
	for _, r := range s {
		switch {
		case r == '.':
			seenDot = true
		case r >= '0' && r <= '9':
			if seenDot {
				frac = frac*10 + float64(r-'0')
				fracDigits++
			} else {
				whole = whole*10 + float64(r-'0')
			}
		default:
			return 0
		}
	}
	for range fracDigits {
		frac /= 10
	}
	return whole + frac
}

// reasoningHints mark models that accept a thinking budget.
var reasoningHints = []string{"reason", "think", "r1", "o1", "o3", "o4", "qwq", "glm", "deepseek"}

func supportsReasoning(p catalog.Provider, id, name string) bool {
	if slices.Contains(p.Modalities, "reasoning") {
		return true
	}
	hay := strings.ToLower(id + " " + name)
	for _, hint := range reasoningHints {
		if strings.Contains(hay, hint) {
			return true
		}
	}
	return false
}

// ProviderState is a catalog entry joined with what the user has configured.
type ProviderState struct {
	catalog.Provider

	Routable   bool   `json:"routable"`
	Configured bool   `json:"configured"`
	KeyMasked  string `json:"key_masked"`
	KeySource  string `json:"key_source"`
	Enabled    bool   `json:"enabled"`
}

// BuildDeployments turns configured providers into the routable set.
func BuildDeployments(providers []catalog.Provider, keys *KeyStore, settings *Settings) []Deployment {
	var out []Deployment
	for _, p := range providers {
		if !p.Routable() || !settings.ProviderEnabled(p.ID) {
			continue
		}
		key, _, ok := keys.Resolve(p.ID, p.Env)
		if !ok {
			continue
		}
		models := settings.ModelsFor(p)
		for i, m := range models {
			ctx := m.Context
			if ctx == 0 {
				ctx = p.MaxContext
			}
			out = append(out, Deployment{
				Provider: p.ID,
				Model:    m.ID,
				BaseURL:  strings.TrimRight(p.BaseURL, "/"),
				APIKey:   key,
				// Catalog order is the upstream's own "best first" ranking,
				// so it is a sensible default priority.
				Priority:  settings.PriorityFor(p.ID)*100 + i,
				Context:   ctx,
				Class:     classifyModel(m.ID, m.Name),
				Params:    modelParams(m.ID, m.Name),
				Reasoning: supportsReasoning(p, m.ID, m.Name),
				// Free-tier catalog entries cost nothing per token. A paid
				// provider with unknown pricing is left at zero rather than
				// guessed, and ordered by priority instead.
				CostIn:  0,
				CostOut: 0,
				RPM:     parseRPM(m.Limits),
			})
		}
	}
	return out
}

// rpmPattern pulls a requests-per-minute figure out of the free-form rate
// limit strings the catalog carries ("30 RPM, 250 RPD").
var rpmPattern = regexp.MustCompile(`(?i)(\d+)\s*(?:RPM|requests?/min|req/min)`)

// parseRPM extracts a per-minute request limit, returning 0 when the upstream
func parseRPM(limits string) int {
	m := rpmPattern.FindStringSubmatch(limits)
	if m == nil {
		return 0
	}
	n := 0
	for _, r := range m[1] {
		n = n*10 + int(r-'0')
	}
	return n
}
