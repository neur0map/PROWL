package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestChangeEmailRequiresPasswordAndRejectsTaken covers the three verdicts the
// dashboard branches on: a wrong current password (403 invalid_password, never
// an authentication_error that would sign the session out), a colliding
// address (409 email_taken), and a clean, normalised change.
func TestChangeEmailRequiresPasswordAndRejectsTaken(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	h := settingsSession(t, s)
	h["Content-Type"] = "application/json"

	resp, body := do(t, s, http.MethodPost, "/api/auth/change-email",
		`{"currentPassword":"nope","newEmail":"new@example.com"}`, h)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Equal(t, "invalid_password", errorType(t, body))

	// A second account already holds an address.
	_, err := s.engine.DB().Exec(
		`INSERT INTO users(email, password_hash, created_at) VALUES(?, ?, ?)`,
		"taken@example.com", "scrypt$00$00", time.Now().Unix())
	require.NoError(t, err)

	resp, body = do(t, s, http.MethodPost, "/api/auth/change-email",
		`{"currentPassword":"correct horse battery","newEmail":"Taken@Example.com"}`, h)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Equal(t, "email_taken", errorType(t, body))

	resp, body = do(t, s, http.MethodPost, "/api/auth/change-email",
		`{"currentPassword":"correct horse battery","newEmail":"Fresh@Example.com"}`, h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var r struct {
		Success bool   `json:"success"`
		Email   string `json:"email"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &r))
	require.True(t, r.Success)
	require.Equal(t, "fresh@example.com", r.Email, "the stored address is normalised")
}

// TestChangeEmailIgnoresUnexpectedFields is the field-level authorization
// property CVE-2026-47102 was missing: the endpoint edits only the email, only
// for the session's own account. Extra privileged-looking fields change
// nothing.
func TestChangeEmailIgnoresUnexpectedFields(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	h := settingsSession(t, s)
	h["Content-Type"] = "application/json"

	resp, _ := do(t, s, http.MethodPost, "/api/auth/change-email",
		`{"currentPassword":"correct horse battery","newEmail":"cve@example.com","id":999,"userId":999,"role":"admin","isAdmin":true,"email":"attacker@evil.com","password_hash":"x"}`, h)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, body := do(t, s, http.MethodGet, "/api/auth/me", "", h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var me struct {
		Email string `json:"email"`
		ID    int64  `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &me))
	require.Equal(t, "cve@example.com", me.Email, "only the email changes")
	require.Equal(t, int64(1), me.ID, "the account id is taken from the session, not the body")

	var count int
	require.NoError(t, s.engine.DB().QueryRow(`SELECT count(*) FROM users`).Scan(&count))
	require.Equal(t, 1, count, "an injected userId must not create an account")
	var attacker int
	require.NoError(t, s.engine.DB().QueryRow(`SELECT count(*) FROM users WHERE email = ?`, "attacker@evil.com").Scan(&attacker))
	require.Equal(t, 0, attacker, "the injected email field must be ignored")
}

// TestForgotAndResetPasswordSurface covers the public reset flow's HTTP
// contract: forgot-password always 200 and throttles, and reset-password's
// error dialect refuses a bad code without ending a session and rejects a short
// password before consulting the code.
func TestForgotAndResetPasswordSurface(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	_ = settingsSession(t, s) // claim the dashboard so an account exists
	jsonH := map[string]string{"Content-Type": "application/json"}

	resp, _ := do(t, s, http.MethodPost, "/api/auth/forgot-password", `{}`, jsonH)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, body := do(t, s, http.MethodPost, "/api/auth/forgot-password", `{}`, jsonH)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.Equal(t, "rate_limit_error", errorType(t, body),
		"a throttle is a rate limit, not an authentication verdict")

	resp, body = do(t, s, http.MethodPost, "/api/auth/reset-password",
		`{"resetCode":"WRONGCODE9","newPassword":"a valid new password"}`, jsonH)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Equal(t, "authentication_error", errorType(t, body))

	resp, _ = do(t, s, http.MethodPost, "/api/auth/reset-password",
		`{"resetCode":"ANYCODE123","newPassword":"short"}`, jsonH)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
