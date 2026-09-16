package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// probeGateway checks that whatever holds the port is a Prowl gateway. It hits
// the ungated /api/ping liveness endpoint and matches both the 200 and the
// {"status":"ok"} body, so an unrelated server that merely holds the port is
// not mistaken for the gateway.
func probeGateway(ctx context.Context, port int, token string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	url := fmt.Sprintf("http://127.0.0.1:%d/api/ping", port)
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
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body); err != nil {
		return false
	}
	return body.Status == "ok"
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
