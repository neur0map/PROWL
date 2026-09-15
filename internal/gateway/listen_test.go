package gateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLoopbackAnswersOnBothNames is the bug a user hit: `localhost` resolves to
// ::1 before 127.0.0.1 on a normal Arch box, so an IPv4-only bind refused every
// browser request to localhost:8787 while curl to the literal IPv4 address
// worked. The dashboard looked broken with nothing to explain it. ListenLoopback
// binds both families so all three names answer.
func TestLoopbackAnswersOnBothNames(t *testing.T) {
	if !ipv6LoopbackUsable(t) {
		t.Skip("no IPv6 loopback on this host")
	}

	port := freePort(t)
	listener, err := ListenLoopback(port)
	require.NoError(t, err)

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	for _, host := range []string{"127.0.0.1", "[::1]", "localhost"} {
		url := fmt.Sprintf("http://%s:%d/", host, port)
		require.Eventually(t, func() bool {
			resp, err := http.Get(url)
			if err != nil {
				return false
			}
			defer func() { _ = resp.Body.Close() }()
			return resp.StatusCode == http.StatusOK
		}, 3*time.Second, 50*time.Millisecond, "the gateway must answer on %s", url)
	}
}

// TestListenSurvivesWithoutIPv6 keeps the IPv6 bind optional: a host with IPv6
// disabled must still get a working listener rather than a startup failure.
func TestListenSurvivesWithoutIPv6(t *testing.T) {
	// Hold ::1 so the gateway's own IPv6 bind fails, if IPv6 exists at all.
	port := freePort(t)
	if blocker, err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", port)); err == nil {
		defer func() { _ = blocker.Close() }()
	}

	listener, err := ListenLoopback(port)
	require.NoError(t, err, "an unavailable IPv6 loopback must not stop the gateway")
	defer func() { _ = listener.Close() }()
	require.Contains(t, listener.Addr().String(), "127.0.0.1")
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func ipv6LoopbackUsable(t *testing.T) bool {
	t.Helper()
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
