// Package service composes the gateway: the ported routing engine, the HTTP
// surface the vendored dashboard talks to, and their shared lifetime.
//
// It exists because the composition cannot live in either half. The HTTP
// surface imports the engine, so the engine cannot import the surface, and
// something above both has to own "open the state, mount the routes, serve
// them, shut down cleanly". That something is this package, and it is the only
// place that knows the whole shape.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
	"github.com/neur0map/prowl/internal/gateway/api"
)

// Service is a running gateway.
type Service struct {
	engine *gateway.Engine
	server *api.Server
	http   *http.Server

	dir   string
	addr  string
	token string

	// setupCode is the first-run code, minted at Listen and cleared by the
	// engine once an account exists.
	setupCode string

	stopBackground func()
}

// Options configures a service.
type Options struct {
	// Dir is the gateway's state directory. Empty uses the default.
	Dir string

	// Port is the loopback port to bind. Zero uses the default.
	Port int

	// MachineKey pins the inference-plane credential. Leave empty so the key
	// is read from settings on every request, which is what makes a
	// regenerate take effect immediately.
	MachineKey string
}

// Open assembles the gateway without binding a port, so a caller can inspect
// state (provider lists, keys) without starting a server.
func Open(ctx context.Context, opts Options) (*Service, error) {
	dir := opts.Dir
	if dir == "" {
		dir = gateway.Dir()
	}

	engine, err := gateway.OpenEngine(ctx, dir, gateway.EngineOptions{})
	if err != nil {
		return nil, fmt.Errorf("open gateway engine: %w", err)
	}

	token, err := gateway.EnsureToken(dir)
	if err != nil {
		_ = engine.Close()
		return nil, err
	}

	server, err := api.NewServer(engine, api.Options{
		MachineKey: opts.MachineKey,
		LocalToken: token,
	})
	if err != nil {
		_ = engine.Close()
		return nil, fmt.Errorf("build gateway surface: %w", err)
	}

	return &Service{engine: engine, server: server, dir: dir, token: token}, nil
}

// Engine exposes the routing engine, for callers that need provider or key
// state without serving.
func (s *Service) Engine() *gateway.Engine { return s.engine }

// Token is the machine-local bootstrap credential. It is distinct from a
// dashboard session and from the unified inference key.
func (s *Service) Token() string { return s.token }

// Addr is the bound address, empty until Listen.
func (s *Service) Addr() string { return s.addr }

// Close releases the engine and stops background work.
func (s *Service) Close() error {
	if s.stopBackground != nil {
		s.stopBackground()
	}
	return s.engine.Close()
}

// Listen binds loopback. Both address families are bound: on a host where
// `localhost` resolves to ::1 first, an IPv4-only bind means a browser typing
// localhost is refused while 127.0.0.1 works, and the dashboard looks broken
// for no visible reason.
func (s *Service) Listen(port int) (net.Listener, error) {
	if port == 0 {
		port = gateway.DefaultPort
	}
	listener, err := gateway.ListenLoopback(port)
	if err != nil {
		return nil, err
	}
	s.addr = listener.Addr().String()

	// The first-run code is minted here rather than in Serve so the caller
	// can print it alongside the dashboard address. Serve blocks, and a code
	// the operator only learns after the process is in the foreground is a
	// code they have to go hunting for.
	s.setupCode = s.engine.Auth().MintSetupCode(context.Background())
	return listener, nil
}

// SetupCode is the first-run code, empty once the dashboard has an account.
// The caller prints it: serving it would defeat it, since the surface that
// needs it is the one no credential guards yet.
func (s *Service) SetupCode() string { return s.setupCode }

// Serve runs until the context is cancelled.
func (s *Service) Serve(ctx context.Context, listener net.Listener) error {
	s.stopBackground = s.engine.StartBackground(ctx)

	// Logged as well as printed, so a gateway started in the background
	// still records the code somewhere the operator can find it. It is never
	// served: a code an unauthenticated caller could fetch protects nothing.
	if s.setupCode != "" {
		slog.Info("Gateway first-run setup code", "code", s.setupCode,
			"note", "required to create the dashboard account, from this machine or any other")
	}

	s.http = &http.Server{
		Handler:           s.server.Handler(),
		ReadHeaderTimeout: 20 * time.Second,
	}

	slog.Info("Gateway listening", "addr", s.addr, "dashboard", "http://"+s.addr+"/")

	errs := make(chan error, 1)
	go func() { errs <- s.http.Serve(listener) }()

	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.http.Shutdown(shutdown)
		return nil
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// BaseURL is the OpenAI-compatible endpoint applications point at.
func (s *Service) BaseURL() string {
	addr := s.addr
	if addr == "" {
		addr = fmt.Sprintf("127.0.0.1:%d", gateway.DefaultPort)
	}
	return "http://" + addr + "/v1"
}
