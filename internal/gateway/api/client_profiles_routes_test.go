package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type profileResponse struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	MaskedKey    string  `json:"maskedKey"`
	SystemPrompt *string `json:"systemPrompt"`
	Enabled      bool    `json:"enabled"`
	CreatedAt    string  `json:"createdAt"`
	UpdatedAt    string  `json:"updatedAt"`
	Key          string  `json:"key"`
}

// createProfile mints a profile through the API and returns the parsed
// create response, whose Key is the one and only disclosure of the full key.
func createProfile(t *testing.T, s *Server, auth map[string]string, body string) profileResponse {
	t.Helper()
	resp, respBody := do(t, s, http.MethodPost, "/api/client-profiles", body, auth)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "body was %q", respBody)
	var p profileResponse
	require.NoError(t, json.Unmarshal([]byte(respBody), &p))
	require.True(t, strings.HasPrefix(p.Key, "sk-cp-"), "minted key %q", p.Key)
	return p
}

// TestClientProfileKeyIsNeverListedWhole is the credential contract: the full
// key is shown once, at create, and a listing only ever carries the mask. A
// leak here would put a working inference credential into a screen that any
// later reader of the response could scrape.
func TestClientProfileKeyIsNeverListedWhole(t *testing.T) {
	s := testServer(t, Options{})
	auth := dashSession(t, s)

	created := createProfile(t, s, auth, `{"name":"CI bot","systemPrompt":"be terse"}`)
	require.NotEqual(t, created.Key, created.MaskedKey)
	require.Contains(t, created.MaskedKey, "...")
	require.NotContains(t, created.MaskedKey, created.Key)
	// The mask reproduces the reference maskKey: first four, last four.
	require.Equal(t, created.Key[:4]+"..."+created.Key[len(created.Key)-4:], created.MaskedKey)

	_, listBody := do(t, s, http.MethodGet, "/api/client-profiles", "", auth)
	require.NotContains(t, listBody, created.Key, "the full key must never appear in a listing")

	var list []profileResponse
	require.NoError(t, json.Unmarshal([]byte(listBody), &list))
	require.Len(t, list, 1)
	require.Empty(t, list[0].Key, "a listed profile carries no key field")
	require.Equal(t, created.MaskedKey, list[0].MaskedKey)
}

// TestRotateInvalidatesTheOldKey proves rotation is a real revocation: the new
// key resolves and the previous one stops resolving immediately, because auth
// resolves by the stored hash and rotate overwrites it.
func TestRotateInvalidatesTheOldKey(t *testing.T) {
	s := testServer(t, Options{})
	auth := dashSession(t, s)
	ctx := context.Background()

	created := createProfile(t, s, auth, `{"name":"rotate me","systemPrompt":"stay on topic"}`)
	first, ok, err := resolveClientProfile(ctx, s.engine.DB(), created.Key)
	require.NoError(t, err)
	require.True(t, ok, "the fresh key must resolve")
	require.Equal(t, created.ID, first.ID)

	resp, rotBody := do(t, s, http.MethodPost,
		fmt.Sprintf("/api/client-profiles/%d/rotate", created.ID), "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var rotated profileResponse
	require.NoError(t, json.Unmarshal([]byte(rotBody), &rotated))
	require.NotEqual(t, created.Key, rotated.Key, "rotate must mint a different key")

	_, ok, err = resolveClientProfile(ctx, s.engine.DB(), created.Key)
	require.NoError(t, err)
	require.False(t, ok, "the old key must stop resolving the instant it is rotated")

	after, ok, err := resolveClientProfile(ctx, s.engine.DB(), rotated.Key)
	require.NoError(t, err)
	require.True(t, ok, "the new key must resolve")
	require.Equal(t, created.ID, after.ID)
}

// TestDeleteLeavesUnrelatedProfiles keeps a delete surgical: only the named
// profile goes, others keep working, and deleting a gone profile is a 404.
func TestDeleteLeavesUnrelatedProfiles(t *testing.T) {
	s := testServer(t, Options{})
	auth := dashSession(t, s)
	ctx := context.Background()

	a := createProfile(t, s, auth, `{"name":"alpha"}`)
	b := createProfile(t, s, auth, `{"name":"beta"}`)

	resp, delBody := do(t, s, http.MethodDelete,
		fmt.Sprintf("/api/client-profiles/%d", a.ID), "", auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.JSONEq(t, `{"success":true}`, delBody)

	_, ok, err := resolveClientProfile(ctx, s.engine.DB(), a.Key)
	require.NoError(t, err)
	require.False(t, ok, "the deleted profile's key must stop resolving")

	_, ok, err = resolveClientProfile(ctx, s.engine.DB(), b.Key)
	require.NoError(t, err)
	require.True(t, ok, "an unrelated profile must be untouched by the delete")

	var list []profileResponse
	_, listBody := do(t, s, http.MethodGet, "/api/client-profiles", "", auth)
	require.NoError(t, json.Unmarshal([]byte(listBody), &list))
	require.Len(t, list, 1)
	require.Equal(t, b.ID, list[0].ID)

	gone, _ := do(t, s, http.MethodDelete,
		fmt.Sprintf("/api/client-profiles/%d", a.ID), "", auth)
	require.Equal(t, http.StatusNotFound, gone.StatusCode)
}

// TestEnforcedPromptRoundTripsByteForByte is the security-relevant property:
// the prompt is prepended to every request made with the key, so the stored
// value must be exactly what was written — no trimming, escaping or unicode
// normalisation. Only an all-whitespace value degrades to "no prompt".
func TestEnforcedPromptRoundTripsByteForByte(t *testing.T) {
	s := testServer(t, Options{})
	auth := dashSession(t, s)
	ctx := context.Background()

	// Leading/trailing whitespace, an embedded newline and tab, quotes, angle
	// brackets and multibyte runes — every class a naive normaliser would touch.
	prompt := "  You are STRICT.\n\t\"Quote\" <b>tag</b> café 日本語  "
	reqBody, err := json.Marshal(map[string]any{"name": "verbatim", "systemPrompt": prompt})
	require.NoError(t, err)

	created := createProfile(t, s, auth, string(reqBody))
	require.NotNil(t, created.SystemPrompt)
	require.Equal(t, prompt, *created.SystemPrompt, "create must not normalise the prompt")

	// Survives the auth-path read (what the lead prepends) byte-for-byte.
	resolved, ok, err := resolveClientProfile(ctx, s.engine.DB(), created.Key)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, resolved.SystemPrompt)
	require.Equal(t, prompt, *resolved.SystemPrompt)

	// And survives a listing read.
	var list []profileResponse
	_, listBody := do(t, s, http.MethodGet, "/api/client-profiles", "", auth)
	require.NoError(t, json.Unmarshal([]byte(listBody), &list))
	require.Len(t, list, 1)
	require.NotNil(t, list[0].SystemPrompt)
	require.Equal(t, prompt, *list[0].SystemPrompt)

	// An all-whitespace prompt is the one degradation: it means "no prompt".
	blank := createProfile(t, s, auth, `{"name":"blank","systemPrompt":"   "}`)
	require.Nil(t, blank.SystemPrompt)

	// PATCH with explicit null clears a set prompt.
	resp, _ := do(t, s, http.MethodPatch,
		fmt.Sprintf("/api/client-profiles/%d", created.ID), `{"systemPrompt":null}`, auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cleared, ok, err := resolveClientProfile(ctx, s.engine.DB(), created.Key)
	require.NoError(t, err)
	require.True(t, ok)
	require.Nil(t, cleared.SystemPrompt)
}

// TestDisabledProfileIsRejectedLikeUnknown keeps a revoked client from telling
// "disabled" apart from "deleted": a disabled profile does not resolve at all.
func TestDisabledProfileIsRejectedLikeUnknown(t *testing.T) {
	s := testServer(t, Options{})
	auth := dashSession(t, s)
	ctx := context.Background()

	created := createProfile(t, s, auth, `{"name":"toggle"}`)

	resp, _ := do(t, s, http.MethodPatch,
		fmt.Sprintf("/api/client-profiles/%d", created.ID), `{"enabled":false}`, auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	_, ok, err := resolveClientProfile(ctx, s.engine.DB(), created.Key)
	require.NoError(t, err)
	require.False(t, ok, "a disabled profile must be rejected exactly like an unknown key")

	resp, _ = do(t, s, http.MethodPatch,
		fmt.Sprintf("/api/client-profiles/%d", created.ID), `{"enabled":true}`, auth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_, ok, err = resolveClientProfile(ctx, s.engine.DB(), created.Key)
	require.NoError(t, err)
	require.True(t, ok, "re-enabling restores resolution")
}

// TestAuthSurvivesABackupRestore is the property the reference bought with a
// recoverable ciphertext and this port keeps with only a hash: mint a key, copy
// the row the way a policy-filtered dump would (no plaintext), restore it into a
// fresh database, and the original key still authenticates.
func TestAuthSurvivesABackupRestore(t *testing.T) {
	origin := testServer(t, Options{})
	auth := dashSession(t, origin)
	ctx := context.Background()

	created := createProfile(t, origin, auth, `{"name":"portable","systemPrompt":"stay on brief"}`)

	// Read exactly the columns a dump carries — never the plaintext key.
	var (
		name, tokenHash, maskedKey string
		systemPrompt               *string
		enabled                    int
		createdAt, updatedAt       int64
	)
	require.NoError(t, origin.engine.DB().QueryRowContext(ctx, `
		SELECT name, token_hash, masked_key, system_prompt, enabled, created_at, updated_at
		  FROM client_profiles WHERE id = ?`, created.ID).
		Scan(&name, &tokenHash, &maskedKey, &systemPrompt, &enabled, &createdAt, &updatedAt))
	require.NotContains(t, tokenHash, created.Key, "a dump must not carry the plaintext key")

	// Restore into a brand-new database that has never seen the key.
	restored := testServer(t, Options{})
	_, err := restored.engine.DB().ExecContext(ctx, `
		INSERT INTO client_profiles
			(name, token_hash, masked_key, system_prompt, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		name, tokenHash, maskedKey, systemPrompt, enabled, createdAt, updatedAt)
	require.NoError(t, err)

	// The original key authenticates against the restored row from the hash
	// alone, with its enforced prompt intact.
	auth2, ok, err := resolveClientProfile(ctx, restored.engine.DB(), created.Key)
	require.NoError(t, err)
	require.True(t, ok, "the original key must still authenticate after a restore")
	require.NotNil(t, auth2.SystemPrompt)
	require.Equal(t, "stay on brief", *auth2.SystemPrompt)
}
