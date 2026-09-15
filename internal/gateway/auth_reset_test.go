package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestResetCodeThrottleAndExpiry pins the two clocks a reset code lives under:
// a 10-second mint throttle and a 15-minute time-to-live, both driven by the
// injectable now so the property is tested rather than waited out.
func TestResetCodeThrottleAndExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, now := testAuth(t)
	_, _, err := a.Setup(ctx, "op@example.com", "correct horse battery", a.MintSetupCode(ctx))
	require.NoError(t, err)

	code1, throttled, err := a.MintResetCode(ctx)
	require.NoError(t, err)
	require.False(t, throttled)
	require.Len(t, code1, setupCodeLength)

	// A second mint inside the throttle window is refused and leaves the live
	// code untouched.
	_, throttled, err = a.MintResetCode(ctx)
	require.NoError(t, err)
	require.True(t, throttled)
	require.True(t, a.resetCodeMatches(code1), "a throttled mint must not clear the live code")

	// Past the throttle window a fresh code mints and invalidates the old one.
	*now = now.Add(11 * time.Second)
	code2, throttled, err := a.MintResetCode(ctx)
	require.NoError(t, err)
	require.False(t, throttled)
	require.NotEqual(t, code1, code2)
	require.False(t, a.resetCodeMatches(code1), "a new code invalidates the previous one")
	require.True(t, a.resetCodeMatches(code2))

	// The code expires 15 minutes after its own mint.
	*now = now.Add(16 * time.Minute)
	require.False(t, a.resetCodeMatches(code2), "an expired code must not match")
}

// TestResetPasswordConsumesCodeAndRevokesSessions covers the credential
// properties: a reset revokes every session opened under the old password, the
// code is single-use, and the new password is the one that now works.
func TestResetPasswordConsumesCodeAndRevokesSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _ := testAuth(t)
	token, _, err := a.Setup(ctx, "op@example.com", "correct horse battery", a.MintSetupCode(ctx))
	require.NoError(t, err)

	_, ok := a.ValidateSession(ctx, token)
	require.True(t, ok)

	code, _, err := a.MintResetCode(ctx)
	require.NoError(t, err)
	require.NoError(t, a.ResetPassword(ctx, code, "a brand new password"))

	_, ok = a.ValidateSession(ctx, token)
	require.False(t, ok, "a reset must revoke sessions opened under the old password")

	require.ErrorIs(t, a.ResetPassword(ctx, code, "yet another password"), ErrInvalidResetCode,
		"a consumed code must not be replayable")

	_, _, err = a.Login(ctx, "op@example.com", "a brand new password")
	require.NoError(t, err, "the new password must log in")
	_, _, err = a.Login(ctx, "op@example.com", "correct horse battery")
	require.ErrorIs(t, err, ErrInvalidCredentials, "the old password must no longer work")
}

// TestMintResetCodeWithNoAccount is the enumeration-safe degradation: with no
// account there is nothing to reset, so no code is minted and the caller still
// answers success without leaking that fact.
func TestMintResetCodeWithNoAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _ := testAuth(t)
	code, throttled, err := a.MintResetCode(ctx)
	require.NoError(t, err)
	require.False(t, throttled)
	require.Empty(t, code)
}
