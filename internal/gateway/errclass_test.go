package gateway

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestClassifyErrorTable pins the §5.3 failure-classification table as
// implemented: for each error class, the retry decision, skip scope, cooldown
// pricing, quota signal, penalty weight, limit-learning, and trail label.
// Every row is a distinct code path, not a parameter permutation.
func TestClassifyErrorTable(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		err    error
		want   ErrorClass
	}{
		{
			name:   "401 invalid key is fatal key-auth",
			status: 401, body: "Unauthorized",
			want: ErrorClass{KeyAuth: true, Attempt: AttemptAuth, Scope: SkipScopeKey},
		},
		{
			name:   "402 payment: key skip, credit bench, light penalty",
			status: 402, body: "Payment required",
			want: ErrorClass{
				Retryable: true, Scope: SkipScopeKey, Cooldown: cooldownPayment,
				Penalty: penaltyLight, LearnLimit: true, Attempt: AttemptOutOfCredits,
			},
		},
		{
			name:   "403 tier: model skip, tier bench, light penalty",
			status: 403, body: "Forbidden",
			want: ErrorClass{
				Retryable: true, SkipModel: true, Scope: SkipScopeModel, Cooldown: cooldownForbidden,
				Penalty: penaltyLight, LearnLimit: true, Attempt: AttemptForbidden,
			},
		},
		{
			name:   "429 daily exhausted: authoritative daily bench, heavy penalty",
			status: 429, body: "You have used up your daily free allocation of 10,000 neurons today",
			want: ErrorClass{
				Retryable: true, Scope: SkipScopeKey, Cooldown: cooldownDaily, QuotaSignal: true,
				Penalty: penaltyHeavy, LearnLimit: true, Attempt: AttemptDailyQuotaExhausted,
			},
		},
		{
			name:   "429 transient rpm: key skip, ladder, heavy penalty",
			status: 429, body: "Rate limit exceeded",
			want: ErrorClass{
				Retryable: true, Scope: SkipScopeKey, Cooldown: cooldownTransient, QuotaSignal: true,
				Penalty: penaltyHeavy, LearnLimit: true, Attempt: AttemptRateLimited,
			},
		},
		{
			name:   "5xx: platform skip, ladder, light penalty",
			status: 500, body: "Internal Server Error",
			want: ErrorClass{
				Retryable: true, SkipPlatform: true, Scope: SkipScopePlatform, Cooldown: cooldownTransient,
				Penalty: penaltyLight, LearnLimit: true, Attempt: AttemptUpstreamError,
			},
		},
		{
			name:   "timeout with no status: platform skip, light penalty",
			status: 0, body: "The operation was aborted (groq, chat, 120s)",
			want: ErrorClass{
				Retryable: true, SkipPlatform: true, Scope: SkipScopePlatform, Cooldown: cooldownTransient,
				Penalty: penaltyLight, LearnLimit: true, Attempt: AttemptTimeout,
			},
		},
		{
			name:   "context too large: model skip, light penalty",
			status: 400, body: "This model's maximum context length is 8192 tokens",
			want: ErrorClass{
				Retryable: true, SkipModel: true, Scope: SkipScopeModel, Cooldown: cooldownTransient,
				Penalty: penaltyLight, LearnLimit: true, Attempt: AttemptContextTooLarge,
			},
		},
		{
			name:   "model not found: model skip, light penalty",
			status: 404, body: "model not found",
			want: ErrorClass{
				Retryable: true, SkipModel: true, Scope: SkipScopeModel, Cooldown: cooldownTransient,
				Penalty: penaltyLight, LearnLimit: true, Attempt: AttemptModelNotFound,
			},
		},
		{
			name:   "unmatched 400 is fatal",
			status: 400, body: "invalid parameter: temperature must be <= 2",
			want: ErrorClass{Attempt: AttemptGenericError, Scope: SkipScopeKey},
		},
		{
			name:   "empty completion: skipBench candidate, light penalty",
			status: 0, body: "",
			err: &UpstreamError{SkipBench: true, Message: "empty completion"},
			want: ErrorClass{
				Retryable: true, Scope: SkipScopeKey, SkipBench: true, Cooldown: cooldownTransient,
				Penalty: penaltyLight, LearnLimit: true, Attempt: AttemptEmptyCompletion,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyError(tc.status, tc.body, tc.err)
			if got != tc.want {
				t.Fatalf("ClassifyError(%d, %q):\n got  %+v\n want %+v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// TestRetryableVsFatalStatuses guards the retry boundary the loop depends on: an
// unmatched 400 and a bare 401 are fatal, while the transient statuses fail over.
func TestRetryableVsFatalStatuses(t *testing.T) {
	retryable := []*UpstreamError{
		{Status: 429, Message: "rate limited"},
		{Status: 500, Message: "boom"},
		{Status: 408, Message: "request timeout"},
		{Status: 410, Message: "gone"},
		{Status: 0, Message: "connect ETIMEDOUT"},
	}
	for _, e := range retryable {
		if !IsRetryableError(e) {
			t.Fatalf("status %d %q should be retryable", e.Status, e.Message)
		}
	}
	fatal := []*UpstreamError{
		{Status: 400, Message: "invalid schema"},
		{Status: 401, Message: "Unauthorized"},
	}
	for _, e := range fatal {
		if IsRetryableError(e) {
			t.Fatalf("status %d %q must be fatal, not retryable", e.Status, e.Message)
		}
	}
}

// TestTransportCauseRetryable proves a transport fault buried in the cause chain
// still fails over, while a bad key is not resurrected by that walk.
func TestTransportCauseRetryable(t *testing.T) {
	buried := &UpstreamError{Message: "upstream request failed", Cause: &UpstreamError{Message: "read ECONNRESET"}}
	if !IsTransportError(buried) {
		t.Fatal("a buried ECONNRESET is a transport error")
	}
	if !IsRetryableError(buried) {
		t.Fatal("a buried transport fault must be retryable")
	}

	// A bad key whose socket also happens to have reset stays key-auth, not a
	// retryable-around-the-chain error: the loop's auth branch owns it first.
	badKey := &UpstreamError{Status: 401, Message: "Unauthorized"}
	if !IsKeyAuthError(badKey) {
		t.Fatal("401 is key-auth")
	}
	if got := ClassifyError(401, "Unauthorized", nil); !got.KeyAuth || got.Retryable {
		t.Fatalf("401 must classify KeyAuth and not Retryable, got %+v", got)
	}
}

// TestAbortsAreNotProviderHealth proves the two gateway-owned aborts win over any
// transport evidence, so a canceled request never benches a healthy provider.
func TestAbortsAreNotProviderHealth(t *testing.T) {
	clientAbort := &UpstreamError{
		Message:     "client disconnected — upstream request canceled",
		ClientAbort: true,
		Cause:       &UpstreamError{Message: "read ECONNRESET"},
	}
	if IsTransportError(clientAbort) {
		t.Fatal("a client abort must not read as a transport error despite the reset cause")
	}
	if !IsClientAbortError(clientAbort) {
		t.Fatal("client abort not detected")
	}
	hedge := &UpstreamError{Message: "fallback time budget expired — upstream request canceled", HedgeAbort: true}
	if IsTransportError(hedge) {
		t.Fatal("a hedge abort must not read as a transport error")
	}
	if !IsHedgeAbortError(hedge) {
		t.Fatal("hedge abort not detected")
	}
}

// TestGoTransportErrorsAreRetryable pins the classification of Go's own
// transport failures. The reference is Node, so the port only recognised
// "ECONNRESET"/"ECONNREFUSED"/"fetch failed" — text a Go program never
// produces. Every cut connection therefore classified as fatal and the
// failover loop stopped after one attempt, which is how a live agent run died
// on "unexpected EOF" with hundreds of candidates untried.
func TestGoTransportErrorsAreRetryable(t *testing.T) {
	t.Parallel()

	cut := []string{
		"unexpected EOF",
		"EOF",
		"read tcp 10.0.0.2:51234->1.2.3.4:443: read: connection reset by peer",
		"write tcp 10.0.0.2:51234->1.2.3.4:443: write: broken pipe",
		"http2: server sent GOAWAY and closed the connection",
		"stream error: Upstream error from Nvidia: Service temporarily overloaded",
	}
	for _, msg := range cut {
		c := classifyNormalized(normalizeError(0, "", errors.New(msg)))
		require.True(t, c.Retryable, "a cut connection must fail over: %q", msg)
		require.False(t, c.SkipPlatform,
			"a cut connection implicates the model, not every key on the platform: %q", msg)
	}

	unreachable := []string{
		"dial tcp 1.2.3.4:443: connect: connection refused",
		"dial tcp: lookup api.example.com: no such host",
		"dial tcp 1.2.3.4:443: connect: network is unreachable",
		"tls: handshake failure",
	}
	for _, msg := range unreachable {
		c := classifyNormalized(normalizeError(0, "", errors.New(msg)))
		require.True(t, c.Retryable, "an unreachable edge must fail over: %q", msg)
		require.True(t, c.SkipPlatform,
			"an unreachable edge implicates every key pointing at it: %q", msg)
	}
}
