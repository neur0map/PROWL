package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// probeGateway checks that whatever holds the port answers the gateway's own
// authorised endpoint. The token is shared through the state directory, so a
// sibling Prowl answers while an unrelated server does not.
func probeGateway(ctx context.Context, port int, token string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	url := fmt.Sprintf("http://127.0.0.1:%d/api/state", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set(tokenHeader, token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// Running reports the dashboard URL when a Prowl gateway already holds the
// port, so a second invocation can point at it instead of failing to bind.
func Running(ctx context.Context, port int, token string) (string, bool) {
	if port == 0 {
		port = DefaultPort
	}
	if !probeGateway(ctx, port, token) {
		return "", false
	}
	return fmt.Sprintf("http://127.0.0.1:%d/", port), true
}

// DashboardURL is the page a user opens to manage keys, on the default port.
// It is a package function so a caller with no server handle -- the TUI, for
// instance -- can still name the address.
func DashboardURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/", DefaultPort)
}

// EntryURL is DashboardURL carrying the token that authorises the first load.
// The server exchanges it for a session cookie and redirects, so the token is
// not left in the address bar or in browser history.
func EntryURL(dir string) string {
	token, err := loadOrCreateToken(dir)
	if err != nil || token == "" {
		return DashboardURL()
	}
	return DashboardURL() + "?token=" + url.QueryEscape(token)
}
