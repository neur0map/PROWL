package gateway

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func dep(provider, model string, opts ...func(*Deployment)) Deployment {
	d := Deployment{
		Provider: provider, Model: model,
		BaseURL: "https://" + provider + ".example/v1",
		APIKey:  "key", Class: ClassLarge, Context: 128_000,
	}
	for _, o := range opts {
		o(&d)
	}
	return d
}

func cost(in, out float64) func(*Deployment) {
	return func(d *Deployment) { d.CostIn, d.CostOut = in, out }
}
func limits(rpm, tpm int) func(*Deployment) {
	return func(d *Deployment) { d.RPM, d.TPM = rpm, tpm }
}
func class(c ModelClass) func(*Deployment) { return func(d *Deployment) { d.Class = c } }
func priority(p int) func(*Deployment)     { return func(d *Deployment) { d.Priority = p } }
func withContext(n int) func(*Deployment)  { return func(d *Deployment) { d.Context = n } }
func noKey() func(*Deployment)             { return func(d *Deployment) { d.APIKey = "" } }

func keys(plan Plan) []string {
	out := make([]string, 0, len(plan.Candidates))
	for _, c := range plan.Candidates {
		out = append(out, c.Key())
	}
	return out
}

// TestPlanIsAnOrderedFallbackChain is the core contract: routing produces a
// chain, not a single pick, so exhausting one provider hands off without the
// caller noticing.
func TestPlanIsAnOrderedFallbackChain(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	plan, err := r.Plan(Request{Model: "auto", EstimatedInputTokens: 5000, HasTools: true}, []Deployment{
		dep("paid", "big", cost(3, 15)),
		dep("free", "llama", cost(0, 0)),
		dep("cheap", "mid", cost(0.1, 0.4)),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"free/llama", "cheap/mid", "paid/big"}, keys(plan),
		"cost strategy must order free first and keep the rest as fallbacks")
}

// TestRateLimitedProviderIsSkippedThenRecovers is the behaviour the user
// described: when one provider runs out the switch must be seamless, and the
// provider must come back on its own afterwards.
func TestRateLimitedProviderIsSkippedThenRecovers(t *testing.T) {
	t.Parallel()

	now := time.Now()
	r := NewRouter(StrategyCost)
	r.now = func() time.Time { return now }

	free := dep("free", "llama", cost(0, 0))
	backup := dep("backup", "llama", cost(0.5, 1))
	all := []Deployment{free, backup}

	// The free provider returns 429 with its own Retry-After.
	r.Failed(free, ClassifyStatus(429), 30*time.Second)

	plan, err := r.Plan(Request{Model: "auto", HasTools: true}, all)
	require.NoError(t, err)
	require.Equal(t, []string{"backup/llama"}, keys(plan), "a cooling provider must not be attempted")
	require.Contains(t, plan.Reason, "cooling down", "the log must say why it switched")

	// Still cooling one second before the window ends.
	now = now.Add(29 * time.Second)
	plan, err = r.Plan(Request{Model: "auto", HasTools: true}, all)
	require.NoError(t, err)
	require.Equal(t, []string{"backup/llama"}, keys(plan))

	// Past Retry-After it is eligible again, and cheapest, so it leads.
	now = now.Add(2 * time.Second)
	plan, err = r.Plan(Request{Model: "auto", HasTools: true}, all)
	require.NoError(t, err)
	require.Equal(t, []string{"free/llama", "backup/llama"}, keys(plan),
		"a recovered provider must be used again without operator action")
}

// TestAuthFailureCoolsDownLongerThanRateLimit pins the distinction litellm
// makes: a bad key will not fix itself in a minute, but must not be evicted
// forever either since the user may paste a working key at any time.
func TestAuthFailureCoolsDownLongerThanRateLimit(t *testing.T) {
	t.Parallel()

	now := time.Now()
	r := NewRouter(StrategyCost)
	r.now = func() time.Time { return now }

	bad := dep("bad", "m", cost(0, 0))
	good := dep("good", "m", cost(1, 1))
	all := []Deployment{bad, good}

	r.Failed(bad, ClassifyStatus(401), 0)
	now = now.Add(2 * time.Minute)
	plan, err := r.Plan(Request{Model: "auto", HasTools: true}, all)
	require.NoError(t, err)
	require.Equal(t, []string{"good/m"}, keys(plan), "a 401 must stay cooled well past a rate-limit window")

	now = now.Add(9 * time.Minute)
	plan, err = r.Plan(Request{Model: "auto", HasTools: true}, all)
	require.NoError(t, err)
	require.Contains(t, keys(plan), "bad/m", "an auth cooldown must expire so a fixed key is picked up")
}

// TestBadRequestDoesNotEvictAProvider protects against one malformed call
// taking a healthy endpoint out of rotation.
func TestBadRequestDoesNotEvictAProvider(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	only := dep("only", "m", cost(0, 0))
	r.Failed(only, ClassifyStatus(400), 0)

	plan, err := r.Plan(Request{Model: "auto", HasTools: true}, []Deployment{only})
	require.NoError(t, err)
	require.Equal(t, []string{"only/m"}, keys(plan), "the request was wrong, not the provider")
}

// TestRepeatedServerErrorsBackOff covers the escalation path.
func TestRepeatedServerErrorsBackOff(t *testing.T) {
	t.Parallel()

	now := time.Now()
	r := NewRouter(StrategyCost)
	r.now = func() time.Time { return now }

	flaky := dep("flaky", "m", cost(0, 0))
	steady := dep("steady", "m", cost(1, 1))
	all := []Deployment{flaky, steady}

	// Two failures are tolerated: transient 500s happen.
	r.Failed(flaky, ClassifyStatus(500), 0)
	r.Failed(flaky, ClassifyStatus(503), 0)
	plan, err := r.Plan(Request{Model: "auto", HasTools: true}, all)
	require.NoError(t, err)
	require.Equal(t, "flaky/m", keys(plan)[0], "a couple of 500s must not evict a provider")

	// The third trips the cooldown.
	r.Failed(flaky, ClassifyStatus(500), 0)
	plan, err = r.Plan(Request{Model: "auto", HasTools: true}, all)
	require.NoError(t, err)
	require.Equal(t, []string{"steady/m"}, keys(plan))

	// A success clears the streak.
	now = now.Add(2 * time.Minute)
	r.Succeeded(flaky, 200*time.Millisecond, 100)
	plan, err = r.Plan(Request{Model: "auto", HasTools: true}, all)
	require.NoError(t, err)
	require.Equal(t, "flaky/m", keys(plan)[0])
}

// TestRateLimitHeadroomIsRespectedBeforeSending stops the gateway from
// spending a request to discover a limit it already knows about.
func TestRateLimitHeadroomIsRespectedBeforeSending(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	tight := dep("tight", "m", cost(0, 0), limits(2, 0))
	roomy := dep("roomy", "m", cost(1, 1))
	all := []Deployment{tight, roomy}

	req := Request{Model: "auto", HasTools: true}
	r.Admit(tight, 0)
	r.Admit(tight, 0)

	plan, err := r.Plan(req, all)
	require.NoError(t, err)
	require.Equal(t, []string{"roomy/m"}, keys(plan), "a provider at its RPM must be skipped, not probed")
	require.Contains(t, plan.Reason, "rate limit reached")
}

// TestContextTooSmallIsSkipped avoids a guaranteed failure.
func TestContextTooSmallIsSkipped(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	small := dep("small", "m", cost(0, 0), withContext(8_000))
	big := dep("big", "m", cost(2, 2), withContext(200_000))

	plan, err := r.Plan(Request{Model: "auto", EstimatedInputTokens: 50_000, HasTools: true},
		[]Deployment{small, big})
	require.NoError(t, err)
	require.Equal(t, []string{"big/m"}, keys(plan))
	require.Contains(t, plan.Reason, "context too small")
}

// TestUnconfiguredProviderIsNeverAttempted keeps a catalog entry without a
// key out of the chain.
func TestUnconfiguredProviderIsNeverAttempted(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	_, err := r.Plan(Request{Model: "auto", HasTools: true}, []Deployment{dep("none", "m", noKey())})
	require.ErrorIs(t, err, ErrNoCandidate)
	require.Contains(t, err.Error(), "no key")
}

// TestAllCoolingRetriesSoonestRecovery is the degraded case: refusing to
// answer is worse than one more attempt against the endpoint closest to
// recovery, since the caller has no alternative.
func TestAllCoolingRetriesSoonestRecovery(t *testing.T) {
	t.Parallel()

	now := time.Now()
	r := NewRouter(StrategyCost)
	r.now = func() time.Time { return now }

	a := dep("a", "m", cost(0, 0))
	b := dep("b", "m", cost(0, 0))
	r.Failed(a, ClassifyStatus(429), 10*time.Minute)
	r.Failed(b, ClassifyStatus(429), 30*time.Second)

	plan, err := r.Plan(Request{Model: "auto", HasTools: true}, []Deployment{a, b})
	require.NoError(t, err)
	require.Equal(t, []string{"b/m"}, keys(plan), "the endpoint recovering soonest is the best remaining bet")
	require.Contains(t, plan.Reason, "all providers unavailable")
}

// TestLatencyStrategyPrefersProvenFastEndpoints covers the second strategy,
// including the cold-start rule that stops every request stampeding onto an
// unmeasured endpoint.
func TestLatencyStrategyPrefersProvenFastEndpoints(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyLatency)
	fast := dep("fast", "m")
	slow := dep("slow", "m")
	cold := dep("cold", "m")

	for range 3 {
		r.Succeeded(fast, 120*time.Millisecond, 10)
		r.Succeeded(slow, 4*time.Second, 10)
	}

	plan, err := r.Plan(Request{Model: "auto", HasTools: true}, []Deployment{slow, cold, fast})
	require.NoError(t, err)
	require.Equal(t, []string{"fast/m", "cold/m", "slow/m"}, keys(plan),
		"proven fast first, unmeasured next, proven slow last")
}

// TestPriorityStrategyFollowsOperatorOrder covers the explicit-control case.
func TestPriorityStrategyFollowsOperatorOrder(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyPriority)
	plan, err := r.Plan(Request{Model: "auto", HasTools: true}, []Deployment{
		dep("third", "m", priority(30), cost(0, 0)),
		dep("first", "m", priority(10), cost(9, 9)),
		dep("second", "m", priority(20)),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"first/m", "second/m", "third/m"}, keys(plan),
		"declared order must win over cost when the operator asked for it")
}

// TestSmartModelRoutingPicksTheCheapClass is the model-routing half:
// mechanical work must not reach for a frontier model. The off-tier endpoint
// stays in the chain behind it, because a free-only setup may own just one
// tier and refusing to answer is worse than answering on the wrong size.
func TestSmartModelRoutingPicksTheCheapClass(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	small := dep("s", "mini", class(ClassSmall), cost(0, 0))
	large := dep("l", "frontier", class(ClassLarge), cost(3, 15))
	all := []Deployment{small, large}

	// A short, tool-free, single-message request: a title or a summary.
	plan, err := r.Plan(Request{Model: "auto", Messages: 1, EstimatedInputTokens: 300}, all)
	require.NoError(t, err)
	require.Equal(t, ClassSmall, plan.Class)
	require.Equal(t, "s/mini", keys(plan)[0], "cheap work leads with the cheap model")

	// Real work: tools in play.
	plan, err = r.Plan(Request{Model: "auto", Messages: 8, HasTools: true, EstimatedInputTokens: 9000}, all)
	require.NoError(t, err)
	require.Equal(t, ClassLarge, plan.Class)
	require.Equal(t, "l/frontier", keys(plan)[0], "hard work leads with the capable model")

	// An explicit alias overrides the heuristic.
	plan, err = r.Plan(Request{Model: "auto:large", Messages: 1, EstimatedInputTokens: 10}, all)
	require.NoError(t, err)
	require.Equal(t, ClassLarge, plan.Class)
}

// TestOnlyOneTierStillAnswers is the free-only case: a user whose providers
// ship nothing but small models must still get a reply to a hard question.
func TestOnlyOneTierStillAnswers(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	onlySmall := []Deployment{dep("s", "mini", class(ClassSmall), cost(0, 0))}

	plan, err := r.Plan(Request{Model: "auto", Messages: 12, HasTools: true, EstimatedInputTokens: 40000}, onlySmall)
	require.NoError(t, err, "a hard request must not fail for want of a large model")
	require.Equal(t, ClassLarge, plan.Class, "the classification stays honest about the work")
	require.Equal(t, []string{"s/mini"}, keys(plan))
}

// TestFreeModelsOrderByCapability covers the tie every free setup hits: when
// every candidate prices at zero, size decides, not alphabetical order.
func TestFreeModelsOrderByCapability(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	// Named so alphabetical order is the opposite of capability order.
	tiny := dep("p", "alpha-3b", class(ClassLarge), cost(0, 0))
	tiny.Params = 3
	big := dep("p", "zeta-70b", class(ClassLarge), cost(0, 0))
	big.Params = 70
	all := []Deployment{tiny, big}

	plan, err := r.Plan(Request{Model: "auto:large", Messages: 8, HasTools: true, EstimatedInputTokens: 9000}, all)
	require.NoError(t, err)
	require.Equal(t, "p/zeta-70b", keys(plan)[0], "a hard request must try the bigger free model first")

	plan, err = r.Plan(Request{Model: "auto:small", Messages: 1, EstimatedInputTokens: 100}, all)
	require.NoError(t, err)
	require.Equal(t, "p/alpha-3b", keys(plan)[0], "cheap work must not burn the bigger free model")
}

// TestAutoReasoningEffortScalesWithWork is the third routing signal.
func TestAutoReasoningEffortScalesWithWork(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		req  Request
		want Effort
	}{
		"mechanical one-shot": {Request{Model: "auto", Messages: 1, EstimatedInputTokens: 200}, EffortNone},
		"plain large request": {Request{Model: "auto:large", Messages: 2, EstimatedInputTokens: 500}, EffortLow},
		"tool-driven turn":    {Request{Model: "auto", HasTools: true, EstimatedInputTokens: 3000}, EffortMedium},
		"long context":        {Request{Model: "auto", HasTools: true, EstimatedInputTokens: 40000}, EffortHigh},
		"long conversation":   {Request{Model: "auto", Messages: 30, EstimatedInputTokens: 5000}, EffortHigh},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := NewRouter(StrategyCost)
			plan, err := r.Plan(tc.req, []Deployment{
				dep("s", "mini", class(ClassSmall), cost(0, 0)),
				dep("l", "big", class(ClassLarge), cost(0, 0)),
			})
			require.NoError(t, err)
			require.Equal(t, tc.want, plan.Effort)
		})
	}
}

// TestPinnedEffortWins keeps an explicit caller instruction authoritative.
func TestPinnedEffortWins(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	plan, err := r.Plan(
		Request{Model: "auto", HasTools: true, EstimatedInputTokens: 40000, Effort: EffortLow},
		[]Deployment{dep("l", "big")},
	)
	require.NoError(t, err)
	require.Equal(t, EffortLow, plan.Effort)
	require.Contains(t, plan.Reason, "pinned")
}

// TestPinnedModelBypassesRouting: asking for a specific model must return
// that model, or an error - never a silent substitution, which would make
// results unreproducible.
func TestPinnedModelBypassesRouting(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	all := []Deployment{
		dep("free", "cheap", cost(0, 0)),
		dep("paid", "exact-model", cost(9, 9)),
	}

	plan, err := r.Plan(Request{Model: "paid/exact-model"}, all)
	require.NoError(t, err)
	require.Equal(t, []string{"paid/exact-model"}, keys(plan))

	_, err = r.Plan(Request{Model: "nope/missing"}, all)
	require.ErrorIs(t, err, ErrNoCandidate)
}

// TestHealthSnapshotFeedsTheDashboard checks the data the user will look at
// to confirm routing is real.
func TestHealthSnapshotFeedsTheDashboard(t *testing.T) {
	t.Parallel()

	now := time.Now()
	r := NewRouter(StrategyCost)
	r.now = func() time.Time { return now }

	good := dep("good", "m", limits(10, 0))
	cooled := dep("cooled", "m")
	r.Succeeded(good, 250*time.Millisecond, 500)
	r.Admit(good, 100)
	r.Failed(cooled, ClassifyStatus(429), time.Minute)

	snaps := r.Health([]Deployment{good, cooled})
	require.Len(t, snaps, 2)

	byKey := map[string]Snapshot{}
	for _, s := range snaps {
		byKey[s.Key] = s
	}
	require.False(t, byKey["good/m"].CoolingDown)
	require.Equal(t, int64(250), byKey["good/m"].AvgLatencyMs)
	require.Positive(t, byKey["good/m"].RequestsInMin)
	require.True(t, byKey["cooled/m"].CoolingDown)
	require.False(t, byKey["cooled/m"].CooldownEnds.IsZero())
}

// TestShortReasoningPromptClassesLarge is the short-hard fix: a reasoning verb
// promotes a tool-free, few-token prompt to the capable tier that shape alone
// would have starved on a small model.
func TestShortReasoningPromptClassesLarge(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	small := dep("s", "mini", class(ClassSmall), cost(0, 0))
	large := dep("l", "frontier", class(ClassLarge), cost(3, 15))
	plan, err := r.Plan(Request{
		Model: "auto", Messages: 1, EstimatedInputTokens: 12,
		LastUserText: "Prove there is no closed form for the Collatz stopping time",
	}, []Deployment{small, large})
	require.NoError(t, err)
	require.Equal(t, ClassLarge, plan.Class, "a short hard question must reach the capable tier")
	require.Equal(t, "l/frontier", keys(plan)[0])
	require.Equal(t, EffortHigh, plan.Effort, "and get a real reasoning budget, not the shape floor")
	require.Contains(t, plan.Reason, "lexical reasoning", "the log must show the signal fired")
}

// TestLongMechanicalPromptStaysCheap is the long-easy fix: a big paste driven
// by a mechanical verb is bulk, not difficulty, so it must not burn the
// frontier model or claim the top effort tier.
func TestLongMechanicalPromptStaysCheap(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	small := dep("s", "mini", class(ClassSmall), cost(0, 0))
	large := dep("l", "frontier", class(ClassLarge), cost(3, 15))
	plan, err := r.Plan(Request{
		Model: "auto", Messages: 1, EstimatedInputTokens: 40000,
		LastUserText: "Extract the timestamps from this log.",
	}, []Deployment{small, large})
	require.NoError(t, err)
	require.Equal(t, ClassSmall, plan.Class, "a big mechanical paste stays on the cheap tier")
	require.Equal(t, "s/mini", keys(plan)[0])
	require.NotEqual(t, EffortHigh, plan.Effort, "and must not reach the top effort tier")
}

// TestMechanicalBulkCapsEffortOnLargeTier proves the effort cap works even when
// the class is fixed large by an alias: mechanical bulk does not deserve the
// top budget just for being long.
func TestMechanicalBulkCapsEffortOnLargeTier(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	plan, err := r.Plan(Request{
		Model: "auto:large", Messages: 1, EstimatedInputTokens: 40000,
		LastUserText: "Extract and convert every row of this log.",
	}, []Deployment{dep("l", "big", class(ClassLarge))})
	require.NoError(t, err)
	require.Equal(t, ClassLarge, plan.Class)
	require.Equal(t, EffortMedium, plan.Effort, "mechanical bulk caps below the top budget")
}

// TestExplicitAliasBeatsLexicalSignal keeps the non-negotiable precedence: an
// explicit auto:small/auto:large alias wins over the lexical read.
func TestExplicitAliasBeatsLexicalSignal(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	deps := []Deployment{
		dep("s", "mini", class(ClassSmall), cost(0, 0)),
		dep("l", "frontier", class(ClassLarge), cost(3, 15)),
	}

	plan, err := r.Plan(Request{
		Model: "auto:small", LastUserText: "Prove and derive and design this",
	}, deps)
	require.NoError(t, err)
	require.Equal(t, ClassSmall, plan.Class, "auto:small wins over a reasoning signal")

	plan, err = r.Plan(Request{
		Model: "auto:large", LastUserText: "list and translate these lines",
	}, deps)
	require.NoError(t, err)
	require.Equal(t, ClassLarge, plan.Class, "auto:large wins over a mechanical signal")
}

// TestPinnedEffortBeatsLexicalSignal keeps the other non-negotiable: a caller
// who pins the reasoning budget is authoritative, even when the words would
// otherwise raise it.
func TestPinnedEffortBeatsLexicalSignal(t *testing.T) {
	t.Parallel()

	r := NewRouter(StrategyCost)
	plan, err := r.Plan(Request{
		Model: "auto", Messages: 1, EstimatedInputTokens: 12,
		LastUserText: "Prove there is no closed form for the Collatz stopping time",
		Effort:       EffortLow,
	}, []Deployment{dep("l", "big", class(ClassLarge))})
	require.NoError(t, err)
	require.Equal(t, ClassLarge, plan.Class, "the lexical signal still promotes the class")
	require.Equal(t, EffortLow, plan.Effort, "but a pinned effort is authoritative")
	require.Contains(t, plan.Reason, "pinned")
}
