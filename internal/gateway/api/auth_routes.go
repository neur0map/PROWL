package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/neur0map/prowl/internal/gateway"
)

// The auth surface bootstraps without a session — status, setup and login must
// be reachable before one exists — and everything else is gated.
func (s *Server) registerAuthRoutes() {
	s.mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	s.mux.HandleFunc("POST /api/auth/setup", s.handleAuthSetup)
	s.mux.HandleFunc("POST /api/auth/login", s.handleAuthLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleAuthLogout)
	s.mux.HandleFunc("GET /api/auth/me", s.RequireSession(s.handleAuthMe))
	s.mux.HandleFunc("POST /api/auth/change-password", s.RequireSession(s.handleChangePassword))
	s.mux.HandleFunc("POST /api/auth/change-email", s.RequireSession(s.handleChangeEmail))
	s.mux.HandleFunc("POST /api/auth/forgot-password", s.handleForgotPassword)
	s.mux.HandleFunc("POST /api/auth/reset-password", s.handleResetPassword)
}

type authStatusResponse struct {
	// NeedsSetup tells the client to show the create-account form rather than
	// the sign-in form.
	NeedsSetup bool `json:"needsSetup"`
	// SetupCodeRequired tells a remote browser it must supply the code that
	// was printed to the server log.
	SetupCodeRequired bool   `json:"setupCodeRequired"`
	Authenticated     bool   `json:"authenticated"`
	Email             string `json:"email,omitempty"`
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	count, err := s.engine.Auth().UserCount(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not read account state")
		return
	}

	resp := authStatusResponse{NeedsSetup: count == 0}
	if resp.NeedsSetup {
		resp.SetupCodeRequired = s.engine.Auth().SetupCodeActive()
	}
	if user, ok := s.engine.Auth().ValidateSession(r.Context(), sessionToken(r)); ok {
		resp.Authenticated = true
		resp.Email = user.Email
	}
	WriteJSON(w, http.StatusOK, resp)
}

type credentialsRequest struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	SetupCode string `json:"setupCode"`
}

type sessionResponse struct {
	Token string `json:"token"`
	Email string `json:"email"`
}

func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	var req credentialsRequest
	if !DecodeJSON(w, r, &req) {
		return
	}

	token, user, err := s.engine.Auth().Setup(
		r.Context(), req.Email, req.Password, req.SetupCode)
	switch {
	case errors.Is(err, gateway.ErrSetupComplete):
		WriteError(w, http.StatusConflict, TypeSetupComplete, "setup is already complete")
		return
	case errors.Is(err, gateway.ErrSetupCodeRequired):
		WriteError(w, http.StatusForbidden, TypeSetupCodeRequired, err.Error())
		return
	case err != nil:
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusCreated, sessionResponse{Token: token, Email: user.Email})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req credentialsRequest
	if !DecodeJSON(w, r, &req) {
		return
	}

	token, user, err := s.engine.Auth().Login(r.Context(), req.Email, req.Password)
	switch {
	case errors.Is(err, gateway.ErrLockedOut):
		// A lockout is a rate limit, not a credential verdict: telling the
		// client it is an authentication_error would end its session on what
		// is really "wait a while".
		WriteError(w, http.StatusTooManyRequests, TypeRateLimit, err.Error())
		return
	case errors.Is(err, gateway.ErrInvalidCredentials):
		WriteError(w, http.StatusUnauthorized, TypeAuthentication, "invalid email or password")
		return
	case err != nil:
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not sign in")
		return
	}
	WriteJSON(w, http.StatusOK, sessionResponse{Token: token, Email: user.Email})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	// Signing out is idempotent: an already-invalid token still yields a
	// clean result, so a client with stale state can always reach a signed-out
	// state instead of being stuck.
	_ = s.engine.Auth().Logout(r.Context(), sessionToken(r))
	WriteNoContent(w)
}

func (s *Server) handleAuthMe(w http.ResponseWriter, _ *http.Request, user gateway.SessionUser) {
	WriteJSON(w, http.StatusOK, map[string]any{"email": user.Email, "id": user.ID})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, user gateway.SessionUser) {
	var req changePasswordRequest
	if !DecodeJSON(w, r, &req) {
		return
	}

	err := s.engine.Auth().ChangePassword(r.Context(), user.ID, req.CurrentPassword, req.NewPassword)
	switch {
	case errors.Is(err, gateway.ErrInvalidCredentials):
		// Distinct from a session failure: the session is fine, the supplied
		// current password is not.
		WriteError(w, http.StatusForbidden, TypeInvalidPassword, "current password is incorrect")
		return
	case err != nil:
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, err.Error())
		return
	}
	// Every session was revoked, including this one, so the client must sign
	// in again with the new password.
	WriteNoContent(w)
}

type changeEmailRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewEmail        string `json:"newEmail"`
}

// handleChangeEmail changes only the email, and only for the session's own
// account: userID comes from the validated session, never the body, and the
// request struct is an explicit allow-list of the two fields the operation
// touches. A body carrying extra privileged-looking fields is decoded into
// nothing and changes nothing (the shape CVE-2026-47102 was missing).
func (s *Server) handleChangeEmail(w http.ResponseWriter, r *http.Request, user gateway.SessionUser) {
	var req changeEmailRequest
	if !DecodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.CurrentPassword) == "" {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "current password is required")
		return
	}
	if !strings.Contains(req.NewEmail, "@") || strings.TrimSpace(req.NewEmail) == "" {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "a valid email is required")
		return
	}
	err := s.engine.Auth().ChangeEmail(r.Context(), user.ID, req.CurrentPassword, req.NewEmail)
	switch {
	case errors.Is(err, gateway.ErrInvalidCredentials):
		// The session is fine; the supplied current password is not. Distinct
		// from a session failure, so it must not carry TypeAuthentication.
		WriteError(w, http.StatusForbidden, TypeInvalidPassword, "current password is incorrect")
		return
	case errors.Is(err, gateway.ErrEmailTaken):
		WriteError(w, http.StatusConflict, TypeEmailTaken, "an account with that email already exists")
		return
	case err != nil:
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not change the email")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"email":   strings.ToLower(strings.TrimSpace(req.NewEmail)),
	})
}

// handleForgotPassword mints a one-time reset code and LOGS it, always
// answering 200 so the response cannot be used to learn whether an account
// exists. The code is never returned over HTTP: the operator reads it from the
// server log, which is what makes this an out-of-band reset a database copy
// cannot perform.
func (s *Server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	code, throttled, err := s.engine.Auth().MintResetCode(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not process the request")
		return
	}
	if throttled {
		WriteError(w, http.StatusTooManyRequests, TypeRateLimit, "too many reset-code requests; try again shortly")
		return
	}
	if code != "" {
		slog.Info("Gateway password-reset code", "code", code,
			"note", "enter this code on the reset form to set a new password")
	}
	WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

type resetPasswordRequest struct {
	ResetCode   string `json:"resetCode"`
	NewPassword string `json:"newPassword"`
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req resetPasswordRequest
	if !DecodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ResetCode) == "" {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "reset code is required")
		return
	}
	if len(req.NewPassword) < 8 {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "password must be at least 8 characters")
		return
	}
	err := s.engine.Auth().ResetPassword(r.Context(), req.ResetCode, req.NewPassword)
	switch {
	case errors.Is(err, gateway.ErrInvalidResetCode):
		// A bad code is not a dashboard-session failure, so it must not carry
		// TypeAuthentication and sign the operator out of a working session.
		WriteError(w, http.StatusForbidden, TypeAuthentication, "invalid or expired reset code")
		return
	case errors.Is(err, gateway.ErrNoAccount):
		// The reference emits the bare "not_found" type here, distinct from the
		// "not_found_error" its other routes use; matched for client parity.
		WriteError(w, http.StatusNotFound, ErrorType("not_found"), "no account found")
		return
	case err != nil:
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not reset the password")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}
