package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/gateway/store"
)

func testAuth(t *testing.T) (*Auth, *time.Time) {
	t.Helper()
	s, err := store.OpenMemory(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	a := NewAuth(s.DB())
	a.now = func() time.Time { return now }
	return a, &now
}

// TestFirstRunIsClaimedOnceAndOnlyOnce is the property the setup code exists
// to protect: an unconfigured dashboard is claimable, a configured one is not.
func TestFirstRunIsClaimedOnceAndOnlyOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _ := testAuth(t)

	token, user, err := a.Setup(ctx, "Operator@Example.com ", "correct horse battery", a.MintSetupCode(ctx))
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.Equal(t, "operator@example.com", user.Email, "the email must be normalised")

	_, _, err = a.Setup(ctx, "attacker@example.com", "another password", a.MintSetupCode(ctx))
	require.ErrorIs(t, err, ErrSetupComplete, "a claimed dashboard must not be re-claimable")
}

// TestSetupNeedsTheCodeFromAnywhere is the property that replaced "loopback
// is trusted". Sharing a host proves the peer is on this machine, not that it
// is the operator, so any local account — or anything running as one — could
// otherwise claim an unconfigured dashboard first and inherit every provider
// credential in it.
func TestSetupNeedsTheCodeFromAnywhere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _ := testAuth(t)

	code := a.MintSetupCode(ctx)
	require.Len(t, code, setupCodeLength)

	_, _, err := a.Setup(ctx, "someone@example.com", "correct horse battery", "")
	require.ErrorIs(t, err, ErrSetupCodeRequired, "a claim without the code must be refused")

	_, _, err = a.Setup(ctx, "someone@example.com", "correct horse battery", "WRONGCODE1")
	require.ErrorIs(t, err, ErrSetupCodeRequired, "a wrong code must be refused")

	_, _, err = a.Setup(ctx, "someone@example.com", "correct horse battery", code)
	require.NoError(t, err, "the real code must work")
	require.False(t, a.SetupCodeActive(), "the code must be spent once the dashboard is claimed")
}

// TestEmptyCodeCannotMatchAnInactiveCode closes the obvious bypass: with no
// code minted, an empty submission must not compare equal to an empty secret.
func TestEmptyCodeCannotMatchAnInactiveCode(t *testing.T) {
	t.Parallel()
	a, _ := testAuth(t)

	require.False(t, a.setupCodeMatches(""), "no active code must match nothing")
	require.False(t, a.setupCodeMatches("ABCDEFGHJK"))
}

// TestLoginRejectsBothWaysIdentically keeps the response from enumerating
// accounts: an unknown email and a wrong password must be indistinguishable.
func TestLoginRejectsBothWaysIdentically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _ := testAuth(t)

	_, _, err := a.Setup(ctx, "op@example.com", "correct horse battery", a.MintSetupCode(ctx))
	require.NoError(t, err)

	_, _, unknown := a.Login(ctx, "nobody@example.com", "correct horse battery")
	_, _, wrong := a.Login(ctx, "op@example.com", "wrong password")
	require.ErrorIs(t, unknown, ErrInvalidCredentials)
	require.ErrorIs(t, wrong, ErrInvalidCredentials)
	require.Equal(t, unknown.Error(), wrong.Error(), "the two failures must be indistinguishable")
}

// TestLockoutStopsGuessing pins the throttle. The fifth failure locks the
// email; a correct password during the lockout must still be refused, or the
// throttle is decorative.
func TestLockoutStopsGuessing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, now := testAuth(t)

	_, _, err := a.Setup(ctx, "op@example.com", "correct horse battery", a.MintSetupCode(ctx))
	require.NoError(t, err)

	for i := range maxLoginAttempts {
		_, _, err := a.Login(ctx, "op@example.com", "wrong")
		require.ErrorIs(t, err, ErrInvalidCredentials, "attempt %d must report a credential failure", i+1)
	}

	_, _, err = a.Login(ctx, "op@example.com", "correct horse battery")
	require.ErrorIs(t, err, ErrLockedOut, "the right password must not bypass an active lockout")

	*now = now.Add(lockoutWindow + time.Second)
	_, _, err = a.Login(ctx, "op@example.com", "correct horse battery")
	require.NoError(t, err, "the lockout must lapse")
}

// TestSuccessfulLoginClearsTheCounter stops a user who mistypes a few times
// and then succeeds from being locked out by the next single mistake.
func TestSuccessfulLoginClearsTheCounter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _ := testAuth(t)

	_, _, err := a.Setup(ctx, "op@example.com", "correct horse battery", a.MintSetupCode(ctx))
	require.NoError(t, err)

	for range maxLoginAttempts - 1 {
		_, _, _ = a.Login(ctx, "op@example.com", "wrong")
	}
	_, _, err = a.Login(ctx, "op@example.com", "correct horse battery")
	require.NoError(t, err)

	_, _, err = a.Login(ctx, "op@example.com", "wrong")
	require.ErrorIs(t, err, ErrInvalidCredentials, "the counter must have been reset, not merely paused")
}

// TestSessionsAreStoredHashedOnly means a copy of the database does not hand
// over live sessions.
func TestSessionsAreStoredHashedOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _ := testAuth(t)

	token, _, err := a.Setup(ctx, "op@example.com", "correct horse battery", a.MintSetupCode(ctx))
	require.NoError(t, err)

	var stored string
	require.NoError(t, a.db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored))
	require.NotEqual(t, token, stored, "the raw token must never be persisted")
	require.Equal(t, hashToken(token), stored)

	user, ok := a.ValidateSession(ctx, token)
	require.True(t, ok)
	require.Equal(t, "op@example.com", user.Email)

	_, ok = a.ValidateSession(ctx, stored)
	require.False(t, ok, "the stored hash must not itself work as a token")
}

// TestSessionExpiryIsCheckedOnRead means a lapsed token stops working the
// moment it expires, not whenever a sweep next runs.
func TestSessionExpiryIsCheckedOnRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, now := testAuth(t)

	token, _, err := a.Setup(ctx, "op@example.com", "correct horse battery", a.MintSetupCode(ctx))
	require.NoError(t, err)

	*now = now.Add(sessionTTL - time.Minute)
	_, ok := a.ValidateSession(ctx, token)
	require.True(t, ok, "the session must hold for its full term")

	*now = now.Add(2 * time.Minute)
	_, ok = a.ValidateSession(ctx, token)
	require.False(t, ok, "an expired session must stop working without waiting for a sweep")
}

// TestPasswordChangeRevokesEverySession is the point of a password change: if
// the old password is suspect, sessions opened with it are too.
func TestPasswordChangeRevokesEverySession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _ := testAuth(t)

	first, user, err := a.Setup(ctx, "op@example.com", "correct horse battery", a.MintSetupCode(ctx))
	require.NoError(t, err)
	second, err := a.CreateSession(ctx, user.ID)
	require.NoError(t, err)

	require.ErrorIs(t, a.ChangePassword(ctx, user.ID, "wrong", "a new long password"),
		ErrInvalidCredentials, "the current password must be proved")
	require.NoError(t, a.ChangePassword(ctx, user.ID, "correct horse battery", "a new long password"))

	for name, token := range map[string]string{"the caller's": first, "another device's": second} {
		_, ok := a.ValidateSession(ctx, token)
		require.False(t, ok, "%s session must be revoked", name)
	}

	_, _, err = a.Login(ctx, "op@example.com", "a new long password")
	require.NoError(t, err)
}

// TestPasswordHashFormatIsPortable keeps the stored hash interchangeable with
// the reference implementation's, and rejects a corrupted row rather than
// letting it authenticate.
func TestPasswordHashFormatIsPortable(t *testing.T) {
	t.Parallel()

	hash, err := hashPassword("correct horse battery")
	require.NoError(t, err)
	require.True(t, len(hash) > 0)
	require.Contains(t, hash, "scrypt$")
	require.True(t, verifyPassword("correct horse battery", hash))
	require.False(t, verifyPassword("wrong", hash))

	// Two hashes of the same password must differ, or the salt is not random.
	other, err := hashPassword("correct horse battery")
	require.NoError(t, err)
	require.NotEqual(t, hash, other)

	for _, corrupt := range []string{
		"", "scrypt$", "scrypt$zz$zz", "bcrypt$aa$bb", "scrypt$aa", hash + "ff",
	} {
		require.False(t, verifyPassword("correct horse battery", corrupt),
			"a malformed stored hash must never authenticate: %q", corrupt)
	}
}

// TestLogoutRevokesOnlyThatSession keeps one device signing out from taking
// the others with it.
func TestLogoutRevokesOnlyThatSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _ := testAuth(t)

	first, user, err := a.Setup(ctx, "op@example.com", "correct horse battery", a.MintSetupCode(ctx))
	require.NoError(t, err)
	second, err := a.CreateSession(ctx, user.ID)
	require.NoError(t, err)

	require.NoError(t, a.Logout(ctx, first))
	_, ok := a.ValidateSession(ctx, first)
	require.False(t, ok)
	_, ok = a.ValidateSession(ctx, second)
	require.True(t, ok, "signing out one device must not sign out the rest")
}
