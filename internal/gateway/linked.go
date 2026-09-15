package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// The pool serves a provider Prowl is already signed in to by storing a
// reference, never a copy: a subscription token is refreshed by the login that
// owns it, so a copy would rot within the hour, and revoking the login must
// revoke the pool's access with it.

// linkedPrefix marks a vault row as a reference rather than ciphertext.
const linkedPrefix = "link:"

// LinkedRef is the provider a linked row points at.
func LinkedRef(stored string) (string, bool) {
	if !strings.HasPrefix(stored, linkedPrefix) {
		return "", false
	}
	ref := strings.TrimSpace(strings.TrimPrefix(stored, linkedPrefix))
	if ref == "" {
		return "", false
	}
	return ref, true
}

// MarkLinked builds the stored form for a linked provider.
func MarkLinked(provider string) string {
	return linkedPrefix + strings.ToLower(strings.TrimSpace(provider))
}

// CredentialSource hands out live credentials for providers Prowl is logged in
// to. The gateway holds the seam; the harness supplies it, so the engine never
// reaches into config or the OAuth flows itself.
type CredentialSource interface {
	// Credential returns a usable bearer token for the provider, refreshing
	// it when the login supports that. The bool reports whether this source
	// knows the provider at all.
	Credential(ctx context.Context, provider string) (string, bool, error)

	// Linkable lists the providers this source can currently serve, which is
	// what the dashboard offers to enroll.
	Linkable(ctx context.Context) []LinkableProvider

	// Models lists what the provider serves, so enrolling it puts real
	// models in the catalogue. Without this an enrolled login is inert: the
	// router has a credential and nothing to route to it.
	Models(ctx context.Context, provider string) []LinkedModel
}

// LinkedModel is one model an enrolled login can serve, carried from the
// provider catalogue the harness already maintains.
type LinkedModel struct {
	ID            string
	Name          string
	ContextWindow int64
	MaxTokens     int64

	// Published prices per million tokens. Zero means the provider lists no
	// price, which is how a subscription or free tier reads.
	InputPerM  float64
	OutputPerM float64

	CanReason   bool
	Attachments bool
	Tools       bool

	// Flagship and Small mark the provider's own default choices for its
	// large and small model, which is the vendor stating which end of its
	// range a model sits at.
	Flagship bool
	Small    bool
}

// LinkableProvider describes a login the pool can borrow.
type LinkableProvider struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`     // "oauth" or "api_key"
	Detail   string `json:"detail"`   // account or plan, when known
	Enrolled bool   `json:"enrolled"` // already has a pool row
}

// SetCredentialSource installs the harness's credential source. The vault
// shares it: a linked row is resolved where the secret is read.
func (e *Engine) SetCredentialSource(src CredentialSource) {
	e.credentials = src
	if e.vault != nil {
		e.vault.credentials = src
	}
	// Logins enrolled before models were seeded own a credential and nothing
	// else. Backfilling here means they start working on the next start
	// instead of requiring the operator to withdraw and re-enrol.
	if src != nil {
		e.backfillLoginModels(context.Background(), src)
	}
}

// backfillLoginModels seeds models for any linked key that owns none.
func (e *Engine) backfillLoginModels(ctx context.Context, src CredentialSource) {
	rows, err := e.store.DB().QueryContext(ctx,
		"SELECT id, platform, encrypted_key FROM api_keys")
	if err != nil {
		return
	}
	type linked struct {
		id       int64
		platform string
	}
	var pending, relabel []linked
	for rows.Next() {
		var (
			id       int64
			platform string
			stored   string
		)
		if err := rows.Scan(&id, &platform, &stored); err != nil {
			break
		}
		if _, isLinked := LinkedRef(stored); !isLinked {
			continue
		}
		relabel = append(relabel, linked{id: id, platform: platform})
		if e.LoginModelCount(ctx, id) == 0 {
			pending = append(pending, linked{id: id, platform: platform})
		}
	}
	_ = rows.Close()

	// Rows enrolled before linked keys carried a provider name are relabelled
	// here: the label is what every surface shows beside the model, and the
	// old placeholder hid which service a request actually spends.
	linkable := src.Linkable(ctx)
	for _, entry := range relabel {
		for _, p := range linkable {
			if !strings.EqualFold(p.ID, entry.platform) || strings.TrimSpace(p.Name) == "" {
				continue
			}
			if _, err := e.store.DB().ExecContext(ctx,
				"UPDATE api_keys SET label = ? WHERE id = ? AND label <> ?",
				p.Name, entry.id, p.Name); err != nil {
				slog.Debug("Could not relabel a linked key", "platform", entry.platform, "error", err)
			}
			break
		}
	}

	for _, entry := range pending {
		models := src.Models(ctx, entry.platform)
		if len(models) == 0 {
			continue
		}
		if _, err := e.SeedLoginModels(ctx, entry.id, entry.platform, models); err != nil {
			slog.Warn("Could not backfill an enrolled login's models",
				"platform", entry.platform, "error", err)
		}
	}
}

// CredentialSource returns the installed source, or nil.
func (e *Engine) CredentialSource() CredentialSource {
	return e.credentials
}

// resolveLinked turns a stored reference into a live credential.
func (v *KeyVault) resolveLinked(ctx context.Context, ref string) (string, error) {
	if v.credentials == nil {
		return "", fmt.Errorf("provider %q is linked to a Prowl login, but no login source is wired", ref)
	}
	secret, known, err := v.credentials.Credential(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("resolving the %s login: %w", ref, err)
	}
	if !known {
		return "", fmt.Errorf("Prowl is not logged in to %q any more; sign in again or remove it from the pool", ref)
	}
	if secret == "" {
		return "", fmt.Errorf("the %s login returned no credential", ref)
	}
	return secret, nil
}
