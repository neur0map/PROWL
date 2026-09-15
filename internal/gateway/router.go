package gateway

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
)

// The router turns "answer this request" into an ordered list of attempts

// maxChainLength bounds how many providers one request may try. litellm uses
// the same cap (ROUTER_MAX_FALLBACKS = 5): without it, a bad day turns a
// single request into a walk across every configured endpoint.
const maxChainLength = 5

// Strategy selects how candidates are ordered.
type Strategy string

const (
	// StrategyCost prefers free and cheap endpoints. Default, because the
	// catalog is free-tier first and cost is the reason most users are here.
	StrategyCost Strategy = "cost"
	// StrategyLatency prefers endpoints that have been answering fastest.
	StrategyLatency Strategy = "latency"
	// StrategyHeadroom prefers endpoints with the most remaining rate-limit
	// budget, which spreads load before anything gets throttled.
	StrategyHeadroom Strategy = "headroom"
	// StrategyPriority follows the operator's declared order exactly.
	StrategyPriority Strategy = "priority"
)

// ModelClass is how large a model a request needs.
type ModelClass string

const (
	ClassSmall ModelClass = "small"
	ClassLarge ModelClass = "large"
)

// Effort is the reasoning budget for a request.
type Effort string

const (
	EffortNone   Effort = ""
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
)

// Deployment is one routable provider+model pair.
type Deployment struct {
	Provider string
	Model    string
	BaseURL  string
	APIKey   string

	// Priority orders candidates under StrategyPriority; lower goes first.
	Priority int

	// RPM and TPM are the endpoint's advertised limits. Zero means unknown.
	RPM int
	TPM int

	// CostIn and CostOut are USD per million tokens. Zero means free.
	CostIn  float64
	CostOut float64

	// Context is the model's window; a request larger than this is skipped
	// rather than sent to fail.
	Context int

	Class     ModelClass
	Reasoning bool

	// Params is the model's size in billions when its name states one. With
	// free models every candidate costs zero, so this is what separates a
	// capable endpoint from a tiny one when the price cannot.
	Params float64
}

// Key identifies a deployment for state tracking.
func (d Deployment) Key() string { return d.Provider + "/" + d.Model }

// Free reports whether the deployment costs nothing per token.
func (d Deployment) Free() bool { return d.CostIn == 0 && d.CostOut == 0 }

// Request describes what the caller wants, in the terms routing needs.
type Request struct {
	// Model is the requested model: a concrete "provider/model", or one of
	// the routing aliases "auto", "auto:small", "auto:large".
	Model string

	EstimatedInputTokens int
	MaxOutputTokens      int
	Messages             int
	HasTools             bool

	// LastUserText is the last user message's text — the input the lexical
	// intent signal reads. It is a leading slice, not the whole paste.
	LastUserText string

	// Effort, when set, pins the reasoning budget and disables the automatic
	// choice. An explicit caller instruction always wins.
	Effort Effort
}

// Plan is the ordered attempt list plus the derived routing decisions.
type Plan struct {
	Candidates []Deployment
	Class      ModelClass
	Effort     Effort

	// Reason explains the routing decision in one line, for the log the user
	// will read when checking whether routing actually happened.
	Reason string
}

// health is the per-deployment state the router learns at runtime.
type health struct {
	cooldownUntil time.Time
	consecutive   int
	latency       time.Duration // exponentially weighted moving average
	samples       int

	// Sliding one-minute counters for rate-limit headroom.
	windowStart time.Time
	requests    int
	tokens      int
}

// Router picks and orders deployments. It is safe for concurrent use.
type Router struct {
	mu     sync.Mutex
	state  map[string]*health
	now    func() time.Time
	strat  Strategy
	cool   time.Duration
	maxCon int
}

// NewRouter returns a router using the given strategy.
func NewRouter(strategy Strategy) *Router {
	if strategy == "" {
		strategy = StrategyCost
	}
	return &Router{
		state:  map[string]*health{},
		now:    time.Now,
		strat:  strategy,
		cool:   60 * time.Second,
		maxCon: 3,
	}
}

// Strategy returns the active ordering strategy.
func (r *Router) Strategy() Strategy { return r.strat }

// SetStrategy changes the ordering strategy. Health state is kept: what the
// router learned about a provider is still true under a different ordering.
func (r *Router) SetStrategy(s Strategy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s != "" {
		r.strat = s
	}
}

// ErrNoCandidate means nothing could serve the request.
var ErrNoCandidate = errors.New("no provider can serve this request")

// Plan orders the deployments that can serve req. The first candidate is the
// primary; the rest are the fallback chain, already filtered for health,
// rate-limit headroom, and context size.
func (r *Router) Plan(req Request, deployments []Deployment) (Plan, error) {
	intent := analyzeIntent(req.LastUserText)
	class := classify(req, intent)
	effort := req.Effort
	autoEffort := false
	if effort == EffortNone {
		effort = autoReasoningEffort(req, class, intent)
		autoEffort = true
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()

	// A concrete "provider/model" request pins the deployment: the caller
	// asked for a specific model and silently answering with another one
	// would make results unreproducible.
	if pinned, ok := pinnedModel(req.Model); ok {
		for _, d := range deployments {
			if d.Key() == pinned || d.Model == pinned {
				return Plan{
					Candidates: []Deployment{d},
					Class:      d.Class,
					Effort:     effort,
					Reason:     fmt.Sprintf("pinned to %s", d.Key()),
				}, nil
			}
		}
		return Plan{}, fmt.Errorf("%w: model %q is not configured", ErrNoCandidate, pinned)
	}

	// The first-run case deserves its own message: with nothing configured
	// the skip list is empty, and a bare "no provider can serve this" tells
	// a new user nothing about what to do next.
	if len(deployments) == 0 {
		return Plan{}, fmt.Errorf("%w: no provider keys configured yet — run `prowl gateway` and add one in the dashboard (%s)", ErrNoCandidate, FreeStarterHint)
	}

	var (
		eligible []Deployment
		fallback []Deployment
		skipped  []string
	)
	for _, d := range deployments {
		if d.APIKey == "" {
			skipped = append(skipped, d.Provider+" (no key)")
			continue
		}
		offClass := d.Class != "" && class != "" && d.Class != class
		if d.Context > 0 && req.EstimatedInputTokens > 0 && req.EstimatedInputTokens > d.Context {
			skipped = append(skipped, d.Key()+" (context too small)")
			continue
		}
		h := r.healthLocked(d.Key(), now)
		if now.Before(h.cooldownUntil) {
			skipped = append(skipped, fmt.Sprintf("%s (cooling down %s)", d.Key(), h.cooldownUntil.Sub(now).Truncate(time.Second)))
			continue
		}
		if !r.hasHeadroomLocked(d, h, req) {
			skipped = append(skipped, d.Key()+" (rate limit reached)")
			continue
		}
		if offClass {
			// Wrong tier, but still usable. A free-only setup may have no
			// large model at all, and refusing to answer a hard question is
			// worse than answering it on a smaller one.
			fallback = append(fallback, d)
			continue
		}
		eligible = append(eligible, d)
	}

	r.orderLocked(eligible, req, class)
	r.orderLocked(fallback, req, class)

	// The preferred tier leads; the other tier trails it. Order matters more
	// than membership here, because the chain is walked in order and a
	// wrong-tier answer only happens once the right-tier ones are exhausted.
	eligible = append(eligible, fallback...)

	if len(eligible) == 0 {
		// Everything is cooling down or capped. Rather than fail, retry the
		// one that recovers soonest: a stale cooldown is a worse outcome than
		// one extra rejected request, and the caller has no other option.
		if soonest, ok := r.soonestRecoveryLocked(deployments, now); ok {
			return Plan{
				Candidates: []Deployment{soonest},
				Class:      class,
				Effort:     effort,
				Reason:     "all providers unavailable; retrying the one recovering soonest: " + strings.Join(skipped, ", "),
			}, nil
		}
		return Plan{}, fmt.Errorf("%w: %s", ErrNoCandidate, strings.Join(skipped, ", "))
	}

	// Bound the chain. litellm caps fallback depth at 5 for the same reason:
	// on a bad day a request could otherwise walk thirty endpoints, turning
	// one slow answer into a minutes-long stall the caller cannot interpret.
	if len(eligible) > maxChainLength {
		eligible = eligible[:maxChainLength]
	}

	reason := fmt.Sprintf("%s strategy, class=%s", r.strat, class)
	if autoEffort && effort != EffortNone {
		reason += ", effort=" + string(effort) + " (auto)"
	} else if effort != EffortNone {
		reason += ", effort=" + string(effort) + " (pinned)"
	}
	if lex := intent.describe(); lex != "" {
		reason += ", " + lex
	}
	if len(skipped) > 0 {
		reason += "; skipped " + strings.Join(skipped, ", ")
	}
	return Plan{Candidates: eligible, Class: class, Effort: effort, Reason: reason}, nil
}

// orderLocked sorts eligible candidates by the active strategy. Ties break on
// provider name so ordering is stable and logs are comparable between runs.
func (r *Router) orderLocked(eligible []Deployment, req Request, class ModelClass) {
	switch r.strat {
	case StrategyLatency:
		slices.SortStableFunc(eligible, func(a, b Deployment) int {
			return compareFloat(r.latencyScoreLocked(a), r.latencyScoreLocked(b), a, b)
		})
	case StrategyHeadroom:
		slices.SortStableFunc(eligible, func(a, b Deployment) int {
			// Most remaining budget first, so load spreads before throttling.
			return compareFloat(-r.headroomLocked(a), -r.headroomLocked(b), a, b)
		})
	case StrategyPriority:
		slices.SortStableFunc(eligible, func(a, b Deployment) int {
			if a.Priority != b.Priority {
				return a.Priority - b.Priority
			}
			return strings.Compare(a.Key(), b.Key())
		})
	default: // StrategyCost
		slices.SortStableFunc(eligible, func(a, b Deployment) int {
			ca, cb := estimatedCost(a, req), estimatedCost(b, req)
			if ca != cb {
				return compareFloat(ca, cb, a, b)
			}
			// Free models all price at zero, so cost alone would leave the
			// order alphabetical. Break the tie on fit instead: a hard
			// request wants the biggest model, a cheap one the smallest.
			return compareFit(a, b, class)
		})
	}
}

// compareFit orders two equally priced deployments by how well their size
// suits the request class, falling back to the key so runs stay comparable.
func compareFit(a, b Deployment, class ModelClass) int {
	if a.Params != b.Params && a.Params > 0 && b.Params > 0 {
		if class == ClassSmall {
			return compareFloat(a.Params, b.Params, a, b)
		}
		return compareFloat(-a.Params, -b.Params, a, b)
	}
	if a.Context != b.Context && class != ClassSmall {
		// Without a stated size, a larger window is the better proxy for a
		// model meant to handle demanding work.
		return compareFloat(float64(-a.Context), float64(-b.Context), a, b)
	}
	return strings.Compare(a.Key(), b.Key())
}

func compareFloat(a, b float64, da, db Deployment) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return strings.Compare(da.Key(), db.Key())
	}
}

// estimatedCost prices this request on a deployment, in USD.
func estimatedCost(d Deployment, req Request) float64 {
	out := req.MaxOutputTokens
	if out == 0 {
		// Assume a modest completion so a cheap-input/expensive-output
		// endpoint does not look free.
		out = 1000
	}
	return (float64(req.EstimatedInputTokens)*d.CostIn + float64(out)*d.CostOut) / 1e6
}

// latencyScoreLocked returns the ordering score for latency routing. A
// deployment with no samples is placed optimistically but not first, so a
// cold endpoint gets tried without every concurrent request stampeding to it.
func (r *Router) latencyScoreLocked(d Deployment) float64 {
	h := r.state[d.Key()]
	if h == nil || h.samples == 0 {
		return 1.5 // seconds: worse than a fast proven endpoint, better than a slow one
	}
	return h.latency.Seconds()
}

// headroomLocked returns the fraction of rate-limit budget still available,
// 1 meaning "no known limit".
func (r *Router) headroomLocked(d Deployment) float64 {
	h := r.state[d.Key()]
	if h == nil {
		return 1
	}
	free := 1.0
	if d.RPM > 0 {
		free = math.Min(free, 1-float64(h.requests)/float64(d.RPM))
	}
	if d.TPM > 0 {
		free = math.Min(free, 1-float64(h.tokens)/float64(d.TPM))
	}
	return math.Max(free, 0)
}

// hasHeadroomLocked reports whether the deployment can admit this request
// without exceeding a known limit.
func (r *Router) hasHeadroomLocked(d Deployment, h *health, req Request) bool {
	if d.RPM > 0 && h.requests >= d.RPM {
		return false
	}
	if d.TPM > 0 && req.EstimatedInputTokens > 0 && h.tokens+req.EstimatedInputTokens > d.TPM {
		return false
	}
	return true
}

// healthLocked returns per-deployment state, rolling the rate-limit window.
func (r *Router) healthLocked(key string, now time.Time) *health {
	h := r.state[key]
	if h == nil {
		h = &health{windowStart: now}
		r.state[key] = h
	}
	if now.Sub(h.windowStart) >= time.Minute {
		h.windowStart = now
		h.requests = 0
		h.tokens = 0
	}
	return h
}

// soonestRecoveryLocked returns the deployment whose cooldown ends first.
func (r *Router) soonestRecoveryLocked(deployments []Deployment, now time.Time) (Deployment, bool) {
	var best Deployment
	var bestAt time.Time
	found := false
	for _, d := range deployments {
		if d.APIKey == "" {
			continue
		}
		h := r.state[d.Key()]
		at := now
		if h != nil {
			at = h.cooldownUntil
		}
		if !found || at.Before(bestAt) {
			best, bestAt, found = d, at, true
		}
	}
	return best, found
}

// Admit records that a request was sent to a deployment, so concurrent
// requests see the consumed budget instead of all picking the same endpoint.
func (r *Router) Admit(d Deployment, estimatedTokens int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.healthLocked(d.Key(), r.now())
	h.requests++
	h.tokens += estimatedTokens
}

// Succeeded records a successful call, clearing any failure streak.
func (r *Router) Succeeded(d Deployment, latency time.Duration, tokens int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.healthLocked(d.Key(), r.now())
	h.consecutive = 0
	h.cooldownUntil = time.Time{}
	h.tokens += tokens
	// EWMA: recent requests dominate without a single spike evicting history.
	if h.samples == 0 {
		h.latency = latency
	} else {
		h.latency = (h.latency*7 + latency*3) / 10
	}
	h.samples++
}

// Failed records a failed call and cools the deployment down when warranted.
// retryAfter, when non-zero, is the provider's own Retry-After: honouring it
// is the difference between backing off and being banned.
func (r *Router) Failed(d Deployment, kind FailureKind, retryAfter time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	h := r.healthLocked(d.Key(), now)
	h.consecutive++

	switch kind {
	case FailureAuth:
		// A bad key will not fix itself. Cool down long so the endpoint stops
		// being tried every request, but not permanently: the user may paste
		// a working key into the dashboard at any moment.
		h.cooldownUntil = now.Add(10 * time.Minute)
	case FailureRateLimit:
		wait := retryAfter
		if wait <= 0 {
			wait = r.cool
		}
		h.cooldownUntil = now.Add(wait)
		// A rate-limited provider has consumed its window by definition.
		if d.RPM > 0 {
			h.requests = d.RPM
		}
	case FailureQuota:
		// Out of credit for this model. Nothing retries it into working, and
		// it is model-specific on some providers, so the cooldown is long and
		// scoped to this deployment rather than the whole provider.
		h.cooldownUntil = now.Add(30 * time.Minute)
	case FailureServer, FailureNetwork:
		if h.consecutive >= r.maxCon {
			// Exponential, capped: three strikes then back off, doubling for
			// a provider that keeps failing.
			backoff := r.cool * time.Duration(1<<min(h.consecutive-r.maxCon, 4))
			h.cooldownUntil = now.Add(min(backoff, 15*time.Minute))
		}
	case FailureRequest:
		// The request itself was wrong; the provider is fine. Do not cool it
		// down or one malformed call would evict a healthy endpoint.
	}
}

// FailureKind classifies why a call failed, which decides the cooldown.
type FailureKind string

const (
	FailureAuth      FailureKind = "auth"
	FailureRateLimit FailureKind = "rate_limit"
	FailureQuota     FailureKind = "quota"
	FailureServer    FailureKind = "server"
	FailureNetwork   FailureKind = "network"
	FailureRequest   FailureKind = "request"
)

// ClassifyStatus maps an HTTP status to a failure kind.
func ClassifyStatus(status int) FailureKind {
	switch {
	case status == 401 || status == 403:
		return FailureAuth
	// 402 is how a provider says "this model needs credit you do not have".
	// Ollama Cloud returns it for its larger models on a free account. It is
	// the canonical out-of-credit signal, so it must move to another provider
	// rather than come back to the caller as if the request were malformed.
	case status == 402:
		return FailureQuota
	case status == 429:
		return FailureRateLimit
	case status == 404, status == 408, status == 409:
		return FailureServer
	case status >= 500:
		return FailureServer
	default:
		return FailureRequest
	}
}

// Snapshot is the per-deployment health the dashboard shows.
type Snapshot struct {
	Key           string        `json:"key"`
	Provider      string        `json:"provider"`
	Model         string        `json:"model"`
	CoolingDown   bool          `json:"cooling_down"`
	CooldownEnds  time.Time     `json:"cooldown_ends,omitempty"`
	Consecutive   int           `json:"consecutive_failures"`
	AvgLatencyMs  int64         `json:"avg_latency_ms"`
	RequestsInMin int           `json:"requests_in_window"`
	TokensInMin   int           `json:"tokens_in_window"`
	Headroom      float64       `json:"headroom"`
	avgLatency    time.Duration `json:"-"`
}

// Health returns the router's live view of every deployment.
func (r *Router) Health(deployments []Deployment) []Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	out := make([]Snapshot, 0, len(deployments))
	for _, d := range deployments {
		h := r.state[d.Key()]
		snap := Snapshot{Key: d.Key(), Provider: d.Provider, Model: d.Model, Headroom: 1}
		if h != nil {
			snap.CoolingDown = now.Before(h.cooldownUntil)
			if snap.CoolingDown {
				snap.CooldownEnds = h.cooldownUntil
			}
			snap.Consecutive = h.consecutive
			snap.AvgLatencyMs = h.latency.Milliseconds()
			snap.RequestsInMin = h.requests
			snap.TokensInMin = h.tokens
			snap.Headroom = r.headroomLocked(d)
		}
		out = append(out, snap)
	}
	return out
}

// pinnedModel reports whether the request names a concrete model rather than
// asking the router to choose.
func pinnedModel(model string) (string, bool) {
	model = strings.TrimSpace(model)
	switch model {
	case "", "auto", "auto:small", "auto:large", "prowl-gateway":
		return "", false
	}
	return model, true
}

// classify decides which model class a request needs.
func classify(req Request, intent intentSignal) ModelClass {
	switch strings.TrimSpace(req.Model) {
	case "auto:small":
		return ClassSmall
	case "auto:large":
		return ClassLarge
	}

	// Short-hard case: the words ask for analysis or design even though the
	// request is small in shape. Shape alone would class it small and return a
	// wrong answer, so trust the words.
	if intent.reasoning {
		return ClassLarge
	}
	// Several distinct asks in one turn is real work — unless the asks are
	// plainly mechanical (translate these five lines).
	if intent.requirements >= 3 && !intent.mechanical {
		return ClassLarge
	}

	if req.HasTools {
		return ClassLarge
	}
	if req.Messages > 2 {
		return ClassLarge
	}
	if req.EstimatedInputTokens > 2000 {
		// Long-easy case: a big paste whose intent is plainly mechanical
		// (extract, list, convert) is bulk, not difficulty, so keep it cheap.
		if intent.mechanical && !intent.reasoning {
			return ClassSmall
		}
		return ClassLarge
	}
	return ClassSmall
}

// autoReasoningEffort derives a thinking budget from request shape corrected
// by lexical intent.
func autoReasoningEffort(req Request, class ModelClass, intent intentSignal) Effort {
	if class == ClassSmall {
		return EffortNone
	}

	// The bias is deliberately asymmetric: sending hard work to too small a
	// budget costs a wrong answer plus a retry, while over-spending on easy
	// work costs pennies. So ambiguity resolves upward, and only a clearly
	// mechanical intent is allowed to pull the budget back down.
	switch {
	case req.EstimatedInputTokens > 32000 || req.Messages > 20:
		if intent.mechanical && !intent.reasoning {
			// A huge but rote paste does not need the top budget just for
			// being long — that is the long-easy case.
			return EffortMedium
		}
		return EffortHigh
	case intent.reasoning:
		// Short but genuinely hard: give it a real budget, not the shape floor.
		return EffortHigh
	case req.HasTools || req.EstimatedInputTokens > 8000 || intent.requirements >= 3 || intent.fencedCode:
		return EffortMedium
	default:
		return EffortLow
	}
}
