package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"
)

// Dashboard authentication, ported from FreeLLMAPI
// (github.com/tashfeenahmed/freellmapi, MIT, v0.9.9 — see NOTICE.md).
//
// Two credential families exist and must not be confused: this one is the
// human operator's account guarding the management surface, while the gateway
// token guards the inference plane that applications call. Merging them would
// mean a leaked application key could also reconfigure the gateway.
const (
	// scryptKeyLen, scryptSaltBytes and the cost parameters match the
	// reference, which uses Node's scryptSync defaults (lib/password.ts:5-12;
	// Node's N=16384, r=8, p=1). Keeping them identical means a password
	// hashed by either implementation verifies in the other.
	scryptKeyLen    = 64
	scryptSaltBytes = 16
	scryptN         = 16384
	scryptR         = 8
	scryptP         = 1

	// sessionTTL is how long a dashboard login lasts (auth.ts:9).
	sessionTTL = 30 * 24 * time.Hour

	// sessionTokenBytes is the raw entropy behind a session token
	// (auth.ts:56). Only its SHA-256 is persisted.
	sessionTokenBytes = 32

	// maxLoginAttempts and lockoutWindow throttle password guessing per
	// email (routes/auth.ts:48-49).
	maxLoginAttempts = 5
	lockoutWindow    = 15 * time.Minute

	// setupCodeLength is the first-run code's length in base32 characters
	// (lib/setup-code.ts). It is logged, never served over HTTP.
	setupCodeLength = 10

	// resetCodeTTL is how long a minted password-reset code stays valid
	// (lib/reset-code.ts:11). resetCodeMintInterval throttles minting so
	// forgot-password cannot spray codes into the log (routes/auth.ts:249).
	resetCodeTTL          = 15 * time.Minute
	resetCodeMintInterval = 10 * time.Second
)

// setupCodeAlphabet excludes the characters people misread when copying a code
// off a terminal: I, L, O, U, 0, 1.
const setupCodeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

// ErrSetupComplete means an account already exists, so first-run setup is
// closed.
var ErrSetupComplete = errors.New("setup already complete")

// ErrInvalidCredentials is returned for both an unknown email and a wrong
// password, so the response cannot be used to enumerate accounts.
var ErrInvalidCredentials = errors.New("invalid email or password")

// ErrLockedOut means too many failed attempts for this email.
var ErrLockedOut = errors.New("too many attempts; try again later")

// ErrSetupCodeRequired means the caller did not present the first-run code
// printed where the gateway was started.
var ErrSetupCodeRequired = errors.New("a setup code is required to create the first account")

// ErrEmailTaken means the requested address already belongs to another
// account. The route maps it to 409 with TypeEmailTaken.
var ErrEmailTaken = errors.New("an account with that email already exists")

// ErrInvalidResetCode means the presented reset code is wrong, expired, or no
// code is active. The route maps it to 403 without revealing which.
var ErrInvalidResetCode = errors.New("invalid or expired reset code")

// ErrNoAccount means a reset was attempted with no account to reset. The route
// maps it to 404 with TypeNotFound.
var ErrNoAccount = errors.New("no account found")

// Auth owns dashboard accounts and sessions.
type Auth struct {
	db  *sql.DB
	now func() time.Time

	mu       sync.Mutex
	attempts map[string]*loginAttempts

	// setupCode is minted once per boot while the dashboard is unclaimed and
	// cleared as soon as an account exists. It is not time-limited: an
	// operator reading it off a log an hour later is the normal case.
	setupCode string

	// resetCode and resetMintedAt hold the single active password-reset code
	// and the moment it was minted, guarded by mu. The code is never
	// persisted and never served over HTTP: it is logged for the operator to
	// read out of band, so a database copy cannot reset the password.
	resetCode     string
	resetMintedAt time.Time
}

type loginAttempts struct {
	count       int
	lockedUntil time.Time
}

// SessionUser identifies the account behind a session.
type SessionUser struct {
	ID    int64
	Email string
}

// NewAuth returns an Auth over the gateway database.
func NewAuth(db *sql.DB) *Auth {
	return &Auth{db: db, now: time.Now, attempts: map[string]*loginAttempts{}}
}

// UserCount reports how many accounts exist, which is what decides whether
// the dashboard is still unclaimed.
func (a *Auth) UserCount(ctx context.Context) (int, error) {
	var n int
	err := a.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// MintSetupCode generates and returns the first-run code, for the caller to
// log. It returns "" when an account already exists, because there is nothing
// left to claim.
func (a *Auth) MintSetupCode(ctx context.Context) string {
	if n, err := a.UserCount(ctx); err != nil || n > 0 {
		return ""
	}
	buf := make([]byte, setupCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	code := make([]byte, setupCodeLength)
	for i, b := range buf {
		code[i] = setupCodeAlphabet[int(b)%len(setupCodeAlphabet)]
	}

	a.mu.Lock()
	a.setupCode = string(code)
	a.mu.Unlock()
	return string(code)
}

// SetupCodeActive reports whether a code is currently required.
func (a *Auth) SetupCodeActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.setupCode != ""
}

// Setup creates the first account. It succeeds only while the dashboard is
// unclaimed, and only for a caller presenting the first-run code.
func (a *Auth) Setup(ctx context.Context, email, password, code string) (string, SessionUser, error) {
	n, err := a.UserCount(ctx)
	if err != nil {
		return "", SessionUser{}, err
	}
	if n > 0 {
		// Nothing left to claim, so the code stops being useful.
		a.mu.Lock()
		a.setupCode = ""
		a.mu.Unlock()
		return "", SessionUser{}, ErrSetupComplete
	}

	// The code is required from every caller, including one on this machine.
	// Loopback proves the peer shares the host, not that it is the operator:
	// any local account, and anything running as one, could otherwise claim
	// an unconfigured dashboard first and inherit every provider credential
	// in it. The code is printed where the person who started the gateway is
	// already looking, so the cost to them is a copy and paste.
	if !a.setupCodeMatches(code) {
		return "", SessionUser{}, ErrSetupCodeRequired
	}
	if err := validateCredentials(email, password); err != nil {
		return "", SessionUser{}, err
	}

	user, err := a.createUser(ctx, email, password)
	if err != nil {
		return "", SessionUser{}, err
	}

	a.mu.Lock()
	a.setupCode = ""
	a.mu.Unlock()

	token, err := a.CreateSession(ctx, user.ID)
	return token, user, err
}

// setupCodeMatches compares in constant time and refuses when no code is
// active, so a caller cannot pass an empty code to match an empty secret.
func (a *Auth) setupCodeMatches(provided string) bool {
	a.mu.Lock()
	active := a.setupCode
	a.mu.Unlock()
	if active == "" || provided == "" || len(provided) != len(active) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(active)) == 1
}

// Login verifies credentials and mints a session.
func (a *Auth) Login(ctx context.Context, email, password string) (string, SessionUser, error) {
	normalised := normaliseEmail(email)
	if locked, until := a.lockedOut(normalised); locked {
		return "", SessionUser{}, fmt.Errorf("%w (%s)", ErrLockedOut,
			until.Sub(a.now()).Truncate(time.Second))
	}

	var (
		id   int64
		hash string
	)
	err := a.db.QueryRowContext(ctx,
		`SELECT id, password_hash FROM users WHERE email = ?`, normalised).Scan(&id, &hash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		a.recordFailure(normalised)
		return "", SessionUser{}, ErrInvalidCredentials
	case err != nil:
		return "", SessionUser{}, err
	}

	if !verifyPassword(password, hash) {
		a.recordFailure(normalised)
		return "", SessionUser{}, ErrInvalidCredentials
	}

	a.clearFailures(normalised)
	token, err := a.CreateSession(ctx, id)
	return token, SessionUser{ID: id, Email: normalised}, err
}

// CreateSession mints an opaque token and stores only its hash.
func (a *Auth) CreateSession(ctx context.Context, userID int64) (string, error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	now := a.now()

	_, err := a.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		hashToken(token), userID, now.Add(sessionTTL).Unix(), now.Unix())
	if err != nil {
		return "", err
	}
	return token, nil
}

// ValidateSession resolves a token to its account, or reports no session.
// Expiry is checked on read rather than swept, so a lapsed token stops
// working immediately even if nothing has pruned it.
func (a *Auth) ValidateSession(ctx context.Context, token string) (SessionUser, bool) {
	if token == "" {
		return SessionUser{}, false
	}
	var (
		user    SessionUser
		expires int64
	)
	err := a.db.QueryRowContext(ctx,
		`SELECT u.id, u.email, s.expires_at FROM sessions s
		 JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`,
		hashToken(token)).Scan(&user.ID, &user.Email, &expires)
	if err != nil || expires <= a.now().Unix() {
		return SessionUser{}, false
	}
	return user, true
}

// Logout revokes one session.
func (a *Auth) Logout(ctx context.Context, token string) error {
	_, err := a.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// ChangePassword updates the password and revokes every session, including
// the caller's: a password change usually means the old one is suspect, so
// leaving other sessions alive would defeat the point.
func (a *Auth) ChangePassword(ctx context.Context, userID int64, current, next string) error {
	var hash string
	if err := a.db.QueryRowContext(ctx,
		`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash); err != nil {
		return err
	}
	if !verifyPassword(current, hash) {
		return ErrInvalidCredentials
	}
	if err := validatePassword(next); err != nil {
		return err
	}
	newHash, err := hashPassword(next)
	if err != nil {
		return err
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, newHash, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *Auth) createUser(ctx context.Context, email, password string) (SessionUser, error) {
	hash, err := hashPassword(password)
	if err != nil {
		return SessionUser{}, err
	}
	normalised := normaliseEmail(email)
	res, err := a.db.ExecContext(ctx,
		`INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)`,
		normalised, hash, a.now().Unix())
	if err != nil {
		return SessionUser{}, err
	}
	id, err := res.LastInsertId()
	return SessionUser{ID: id, Email: normalised}, err
}

func (a *Auth) lockedOut(email string) (bool, time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry, ok := a.attempts[email]
	if !ok || entry.lockedUntil.IsZero() {
		return false, time.Time{}
	}
	if entry.lockedUntil.After(a.now()) {
		return true, entry.lockedUntil
	}
	// The window lapsed, so the slate is clean.
	delete(a.attempts, email)
	return false, time.Time{}
}

func (a *Auth) recordFailure(email string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry, ok := a.attempts[email]
	if !ok {
		entry = &loginAttempts{}
		a.attempts[email] = entry
	}
	entry.count++
	if entry.count >= maxLoginAttempts {
		entry.lockedUntil = a.now().Add(lockoutWindow)
	}
}

func (a *Auth) clearFailures(email string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.attempts, email)
}

// hashPassword produces `scrypt$<saltHex>$<hashHex>`, the reference's format,
// so a hash is portable between the two implementations.
func hashPassword(password string) (string, error) {
	salt := make([]byte, scryptSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return "", err
	}
	return "scrypt$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(key), nil
}

// verifyPassword compares in constant time. A malformed stored value is a
// failure rather than an error, so a corrupted row cannot authenticate.
func verifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 || parts[0] != "scrypt" {
		return false
	}
	salt, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(parts[2])
	if err != nil || len(expected) == 0 {
		return false
	}
	actual, err := scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func normaliseEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validateCredentials(email, password string) error {
	if !strings.Contains(email, "@") || strings.TrimSpace(email) == "" {
		return errors.New("a valid email is required")
	}
	return validatePassword(password)
}

func validatePassword(password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	return nil
}

// ChangeEmail verifies the current password and moves the account to a new,
// normalised address. Sessions are deliberately kept alive: an email change is
// not the credential compromise a password change is, so signing every device
// out would be gratuitous (auth.ts:85-102). The address is normalised the same
// way accounts are created and looked up, so differing only in case cannot
// create a second account or slip past the uniqueness check.
func (a *Auth) ChangeEmail(ctx context.Context, userID int64, currentPassword, newEmail string) error {
	var hash string
	if err := a.db.QueryRowContext(ctx,
		`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash); err != nil {
		return err
	}
	if !verifyPassword(currentPassword, hash) {
		return ErrInvalidCredentials
	}
	normalised := normaliseEmail(newEmail)
	var other int64
	err := a.db.QueryRowContext(ctx,
		`SELECT id FROM users WHERE email = ? AND id != ?`, normalised, userID).Scan(&other)
	switch {
	case err == nil:
		return ErrEmailTaken
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	_, err = a.db.ExecContext(ctx, `UPDATE users SET email = ? WHERE id = ?`, normalised, userID)
	return err
}

// MintResetCode mints a one-time password-reset code and returns it for the
// caller to LOG, never to serve over HTTP: the operator reads it out of band
// from the server log, which is the whole point. It returns ("", false, nil)
// when there is no account to reset — the caller still answers 200 so a probe
// cannot learn whether an account exists — and ("", true, nil) when a code was
// minted within the throttle window (routes/auth.ts:251-265, lib/reset-code.ts).
func (a *Auth) MintResetCode(ctx context.Context) (code string, throttled bool, err error) {
	n, err := a.UserCount(ctx)
	if err != nil {
		return "", false, err
	}
	if n == 0 {
		return "", false, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.resetMintedAt.IsZero() && a.now().Sub(a.resetMintedAt) < resetCodeMintInterval {
		return "", true, nil
	}
	buf := make([]byte, setupCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", false, err
	}
	out := make([]byte, setupCodeLength)
	for i, b := range buf {
		out[i] = setupCodeAlphabet[int(b)%len(setupCodeAlphabet)]
	}
	a.resetCode = string(out)
	a.resetMintedAt = a.now()
	return a.resetCode, false, nil
}

// resetCodeMatches compares in constant time and refuses when no code is active
// or the code has expired, clearing an expired code so a later guess cannot
// race the clock.
func (a *Auth) resetCodeMatches(provided string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.resetCode == "" {
		return false
	}
	if a.now().Sub(a.resetMintedAt) > resetCodeTTL {
		a.resetCode = ""
		return false
	}
	if provided == "" || len(provided) != len(a.resetCode) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(a.resetCode)) == 1
}

// ResetPassword consumes a reset code to set a new password on the single
// account, then revokes every session for it — the same reasoning as
// ChangePassword: if the password had to be reset, any session opened under the
// old one is suspect. The code is single-use, cleared on success so it cannot
// be replayed (routes/auth.ts:273-289, auth.ts:121-128).
func (a *Auth) ResetPassword(ctx context.Context, code, newPassword string) error {
	if !a.resetCodeMatches(code) {
		return ErrInvalidResetCode
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	var id int64
	err := a.db.QueryRowContext(ctx, `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNoAccount
	case err != nil:
		return err
	}
	newHash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, newHash, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.mu.Lock()
	a.resetCode = ""
	a.resetMintedAt = time.Time{}
	a.mu.Unlock()
	return nil
}
