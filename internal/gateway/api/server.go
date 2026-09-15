package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
	"github.com/neur0map/prowl/internal/gateway/webui"
)

// sessionHeader is the dashboard's own header, accepted alongside a bearer so
// a session token never has to be confused with the inference-plane key.
const sessionHeader = "X-Dashboard-Token"

// Server owns the gateway's HTTP surface.
type Server struct {
	engine *gateway.Engine
	mux    *http.ServeMux

	// settings owns the unified key that authenticates the inference plane.
	// It is a different credential from a dashboard session on purpose: an
	// application key that leaks must not also be able to reconfigure the
	// gateway.
	settings *settingsStore

	// machineKey, when set, overrides the stored key. It exists for tests and
	// for an operator pinning a key by configuration; normal operation reads
	// the stored value so a regenerate takes effect at once.
	machineKey string

	// localToken is the machine-local bootstrap credential; see Options.
	localToken string

	limiter *rateLimiter

	// stats caches the aggregated request trail the scorer reads. It is on
	// the server rather than per request so a week of rows is re-read once a
	// minute instead of once a request.
	stats statsCache
}

// Options configures the surface.
type Options struct {
	// MachineKey pins the credential guarding /v1. Leave it empty in normal
	// operation: the key is then read from settings on every check, which is
	// what makes a regenerate invalidate the old one immediately instead of
	// at the next restart.
	MachineKey string

	// LocalToken is the machine-local bootstrap credential, and it is the
	// second credential /v1 accepts.
	//
	// It exists because Prowl registers the gateway as its own provider using
	// this token, while the unified key is what external applications hold. A
	// stored copy of the unified key would go stale the moment the operator
	// regenerates it — and an attached window is a separate process that
	// cannot be told to refresh — whereas this token is never regenerated, so
	// first-party access cannot drift.
	//
	// It grants inference only, never a dashboard session, and it lives at
	// mode 0600 beside master.key and gateway.db: anyone who can read it can
	// already decrypt every provider credential, so it concedes nothing that
	// trust boundary does not already imply. Rotating it is the separate
	// action that revokes first-party access; regenerating the unified key
	// does not.
	LocalToken string
}

// NewServer builds the surface over an assembled engine.
func NewServer(engine *gateway.Engine, opts Options) (*Server, error) {
	s := &Server{
		engine:     engine,
		mux:        http.NewServeMux(),
		machineKey: opts.MachineKey,
		localToken: opts.LocalToken,
		limiter:    newRateLimiter(),
	}
	if err := s.routes(); err != nil {
		return nil, err
	}
	return s, nil
}

// Handler returns the composed handler. The cross-site guard wraps everything
// rather than individual routes, because the surface it protects is precisely
// the one that has no per-route gate to hang it on.
func (s *Server) Handler() http.Handler { return guardCrossSite(s.mux) }

func (s *Server) routes() error {
	// Liveness is deliberately ungated: a health check that needs a
	// credential is a health check nobody wires up.
	s.mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "timestamp": time.Now().UTC().Format(time.RFC3339),
		})
	})

	s.registerAuthRoutes()
	s.registerKeysRoutes()
	s.registerKeysCustomRoutes()
	s.registerInferenceRoutes()
	s.registerSettingsRoutes()
	s.registerRoutingRoutes()
	s.registerAnalyticsRoutes()
	s.registerStatusRoutes()
	s.registerEmbeddingsRoutes()
	s.registerEmbeddingsInferenceRoutes()
	s.registerMediaRoutes()
	s.registerMediaInferenceRoutes()
	s.registerConversationsRoutes()
	s.registerClientProfilesRoutes()
	s.registerSettingsExtraRoutes()
	s.registerBackupsRoutes()
	s.registerDirectoryRoutes()
	s.registerLoginRoutes()
	s.registerKeyActivityRoutes()
	s.registerStatusV1Routes()
	s.registerDocsRoutes()
	s.registerChainPresetRoutes()
	s.registerCatalogBrowseRoutes()

	// An unmatched API or inference path must answer in JSON, not with the
	// app shell. The SPA fallback below would otherwise return HTML with a
	// 200 for a typo'd endpoint, and the client turns a non-JSON 200 into
	// "the API isn't reachable at this origin" -- a misleading diagnosis of
	// what is really a missing route.
	s.mux.HandleFunc("/api/", s.handleAPINotFound)
	s.mux.HandleFunc("/v1/", s.handleAPINotFound)
	s.mux.HandleFunc("/v1beta/", s.handleAPINotFound)

	// The SPA is last so it only catches what no API route claimed, and so
	// its streaming responses are never buffered behind a middleware.
	ui, err := webui.Handler()
	if err != nil {
		return err
	}
	s.mux.Handle("/", ui)
	return nil
}

func (s *Server) handleAPINotFound(w http.ResponseWriter, r *http.Request) {
	WriteError(w, http.StatusNotFound, TypeNotFound, "no such endpoint: "+r.URL.Path)
}

// RequireSession wraps a dashboard handler with the session gate.
//
// Only this gate may answer with TypeAuthentication: the client ends its
// session on exactly that combination, so using it anywhere else — a relayed
// upstream 401 from probing a provider key, for instance — would sign the
// operator out for someone else's failure.
func (s *Server) RequireSession(next func(http.ResponseWriter, *http.Request, gateway.SessionUser)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allow(adminBucket, clientIP(r)) {
			WriteError(w, http.StatusTooManyRequests, TypeRateLimit, "too many requests")
			return
		}
		user, ok := s.engine.Auth().ValidateSession(r.Context(), sessionToken(r))
		if !ok {
			WriteError(w, http.StatusUnauthorized, TypeAuthentication, "not signed in")
			return
		}
		next(w, r, user)
	}
}

// RequireMachineKey wraps an inference-plane handler.
//
// A failure here is an authentication failure for an application, not for the
// dashboard, so it must NOT carry TypeAuthentication — otherwise a client
// calling /v1 with a stale key would log the human operator out of a browser
// tab they are not even looking at.
func (s *Server) RequireMachineKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allow(proxyBucket, clientIP(r)) {
			WriteError(w, http.StatusTooManyRequests, TypeRateLimit, "too many requests")
			return
		}
		if s.machineKeyAuthenticates(r) {
			next(w, r)
			return
		}
		// A client profile is the second inference credential: its own
		// sk-cp- token, carrying a system prompt the profile enforces on
		// every request made with it.
		profile, ok, err := resolveClientProfile(r.Context(), s.engine.DB(), bearerToken(r))
		if err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer,
				"could not verify the api key")
			return
		}
		if !ok {
			WriteError(w, http.StatusUnauthorized, TypeInvalidRequest, "invalid api key")
			return
		}
		next(w, r.WithContext(withClientProfile(r.Context(), profile)))
	}
}

// clientProfileKey types the context slot holding the authenticated profile,
// so no other package can collide with or read it by guessing a string.
type clientProfileKey struct{}

func withClientProfile(ctx context.Context, p ClientProfileAuth) context.Context {
	return context.WithValue(ctx, clientProfileKey{}, p)
}

// clientProfileFrom reports the profile a request authenticated as, if it used
// a profile token rather than the machine key.
func clientProfileFrom(ctx context.Context) (ClientProfileAuth, bool) {
	p, ok := ctx.Value(clientProfileKey{}).(ClientProfileAuth)
	return p, ok
}

// machineKeyAuthenticates checks the inference-plane credential against live
// state, so regenerating the key stops the old one working on the very next
// request rather than after a restart.
func (s *Server) machineKeyAuthenticates(r *http.Request) bool {
	presented := bearerToken(r)
	if presented == "" {
		return false
	}
	// Prowl's own agent registers the gateway with the machine-local token, so
	// it is a first-party inference credential. secureEqual refuses an empty
	// value on either side, which keeps an unset token from matching an absent
	// one.
	if secureEqual(presented, s.localToken) {
		return true
	}
	if s.machineKey != "" {
		return secureEqual(presented, s.machineKey)
	}
	if s.settings == nil {
		return false
	}
	ok, err := s.settings.authenticateMachineKey(r.Context(), presented)
	return err == nil && ok
}

func sessionToken(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get(sessionHeader)); v != "" {
		return v
	}
	return bearerToken(r)
}

func bearerToken(r *http.Request) string {
	if v := r.Header.Get("X-Api-Key"); v != "" {
		return strings.TrimSpace(v)
	}
	auth := r.Header.Get("Authorization")
	if after, ok := cutPrefixFold(auth, "bearer "); ok {
		return strings.TrimSpace(after)
	}
	return strings.TrimSpace(auth)
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

// bucket names the two independent per-IP budgets. The inference plane is far
// busier than the dashboard, so one shared budget would let normal traffic
// throttle the operator out of their own settings page.
type bucket string

const (
	adminBucket bucket = "admin"
	proxyBucket bucket = "proxy"
)

// Per-minute allowances, matching the reference's limiters.
var bucketLimits = map[bucket]int{
	adminBucket: 600,
	proxyBucket: 120,
}

// maxTrackedIPs bounds the limiter's memory so a spray of forged source
// addresses cannot grow it without limit.
const maxTrackedIPs = 10000

type rateLimiter struct {
	mu      sync.Mutex
	windows map[bucket]map[string]*fixedWindow
	now     func() time.Time
}

type fixedWindow struct {
	start time.Time
	count int
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{
		windows: map[bucket]map[string]*fixedWindow{
			adminBucket: {}, proxyBucket: {},
		},
		now: time.Now,
	}
}

func (l *rateLimiter) allow(b bucket, ip string) bool {
	limit := bucketLimits[b]
	if limit <= 0 || ip == "" {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	per := l.windows[b]

	if len(per) > maxTrackedIPs {
		// Drop the table rather than scan it: the budget is per minute, so
		// the cost of forgetting is at most one minute of leniency, and the
		// alternative is unbounded growth under a spoofed-source flood.
		l.windows[b] = map[string]*fixedWindow{}
		per = l.windows[b]
	}

	win, ok := per[ip]
	if !ok || now.Sub(win.start) >= time.Minute {
		per[ip] = &fixedWindow{start: now, count: 1}
		return true
	}
	if win.count >= limit {
		return false
	}
	win.count++
	return true
}

func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

// secureEqual compares credentials in constant time.
func secureEqual(got, want string) bool {
	if got == "" || want == "" || len(got) != len(want) {
		return false
	}
	var diff byte
	for i := range len(got) {
		diff |= got[i] ^ want[i]
	}
	return diff == 0
}

// Shutdown is a placeholder for symmetry with the engine's lifecycle; the
// surface itself holds no resources beyond the engine.
func (s *Server) Shutdown(context.Context) error { return nil }
