package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"syscall"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
)

// Embedded is a gateway running inside the harness process.
type Embedded struct {
	baseURL string
	token   string
	shared  bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// BaseURL is the OpenAI-compatible endpoint clients should use.
func (e *Embedded) BaseURL() string { return e.baseURL }

// Token authorises the first dashboard load.
func (e *Embedded) Token() string { return e.token }

// Shared reports that another process already owned the port, so this handle
// points at that gateway rather than one we started.
func (e *Embedded) Shared() bool { return e.shared }

// Stop shuts the gateway down and waits for it. It is safe on a shared handle,
// where it does nothing, and on a nil receiver, so a caller that failed to
// start one can still defer it.
func (e *Embedded) Stop() {
	if e == nil || e.cancel == nil {
		return
	}
	e.cancel()
	select {
	case <-e.done:
	case <-time.After(2 * time.Second):
	}
}

// StartEmbedded serves the gateway for as long as the harness runs, so the
// dashboard is reachable without a second command.
//
// Several Prowl windows on one machine share a single gateway: the first to
// bind the port owns it, the rest attach. That keeps one usage ledger, one set
// of cooldowns and one reliability history — per-window engines would each
// have to rediscover that a provider is out of credit, and would each learn a
// different answer.
func StartEmbedded(parent context.Context, dir string, logins gateway.CredentialSource) (*Embedded, error) {
	svc, err := Open(parent, Options{Dir: dir})
	if err != nil {
		return nil, err
	}
	// The harness's own provider logins become pool candidates, which is what
	// lets a subscription serve gateway traffic without its token being
	// copied anywhere.
	if logins != nil {
		svc.Engine().SetCredentialSource(logins)
	}

	listener, err := svc.Listen(gateway.DefaultPort)
	if err != nil {
		_ = svc.Close()
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
		// Someone else is serving. Only adopt it if it is a Prowl gateway; an
		// unrelated process on the port must not receive our keys.
		token, tokenErr := gateway.EnsureToken(dir)
		if tokenErr != nil {
			return nil, tokenErr
		}
		if url, ok := gateway.Running(parent, gateway.DefaultPort, token); ok {
			slog.Debug("Attaching to the gateway already running on this machine", "url", url)
			return &Embedded{
				baseURL: fmt.Sprintf("http://127.0.0.1:%d/v1", gateway.DefaultPort),
				token:   token,
				shared:  true,
			}, nil
		}
		return nil, fmt.Errorf("port %d is busy and not a Prowl gateway", gateway.DefaultPort)
	}

	ctx, cancel := context.WithCancel(parent)
	e := &Embedded{
		baseURL: svc.BaseURL(),
		token:   svc.Token(),
		cancel:  cancel,
		done:    make(chan struct{}),
	}

	go func() {
		defer close(e.done)
		defer func() { _ = svc.Close() }()
		if err := svc.Serve(ctx, listener); err != nil && ctx.Err() == nil {
			slog.Error("Gateway stopped", "error", err)
		}
	}()

	return e, nil
}
