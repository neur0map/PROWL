package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/neur0map/prowl/internal/gateway"
)

func insertBackupPayload(t *testing.T, s *Server, payload string) int64 {
	t.Helper()
	res, err := s.engine.DB().Exec(
		`INSERT INTO backups(filename, filesize, is_full, source, created_at, tables_json, payload)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		"crafted.json", len(payload), 0, "manual", time.Now().Unix(), "[]", payload)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n))
	return n > 0
}

// TestBackupExcludesCredentialTablesAndSecrets asserts the policy BOTH by name
// (the excluded tables are in the auditable list) AND by consequence (no
// planted secret survives anywhere in a generated dump). Testing only one would
// pass while a filter bug leaked data or someone quietly narrowed the list.
func TestBackupExcludesCredentialTablesAndSecrets(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	ctx := context.Background()
	db := s.engine.DB()

	for _, name := range []string{"users", "sessions", "url_tokens"} {
		require.True(t, backupExcludedTables[name], "%s must be on the exclusion list", name)
	}

	_, err := db.Exec(`INSERT INTO users(email, password_hash, created_at) VALUES(?,?,?)`,
		"victim@example.com", "scrypt$aa$USERHASHSECRET", time.Now().Unix())
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sessions(token_hash, user_id, expires_at, created_at) VALUES(?,?,?,?)`,
		"SESSIONHASHSECRET", 1, time.Now().Add(time.Hour).Unix(), time.Now().Unix())
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO url_tokens(token_hash, label, token_prefix, created_at) VALUES(?,?,?,?)`,
		"URLTOKENHASHSECRET", "l", "flmurl_pref…", time.Now().Unix())
	require.NoError(t, err)

	const providerSecret = "sk-provider-PLAINTEXT-never-leak-abcdefg"
	_, err = s.engine.Vault().Add("openai", providerSecret, gateway.AddOptions{Label: "k"})
	require.NoError(t, err)

	const unifiedSecret = "prowl-machine-PLAINTEXT-unified-secret"
	const relaySecret = "relay-PLAINTEXT-token-secret"
	require.NoError(t, s.settings.putRawSetting(ctx, settingUnifiedAPIKey, unifiedSecret))
	require.NoError(t, s.settings.putRawSetting(ctx, settingFetchRelayToken, relaySecret))

	const fullCP = "sk-cp-000102030405060708090a0b0c0d0e0f1011121314151617"
	if tableExists(t, db, "client_profiles") {
		sum := sha256.Sum256([]byte(fullCP))
		_, err = db.Exec(
			`INSERT INTO client_profiles(name, token_hash, masked_key, system_prompt, enabled, created_at, updated_at)
			 VALUES(?,?,?,?,?,?,?)`,
			"cp", hex.EncodeToString(sum[:]), "sk-c...1617", nil, 1, time.Now().Unix(), time.Now().Unix())
		require.NoError(t, err)
	}

	meta, err := createBackup(ctx, db, backupCreateOptions{source: "manual"})
	require.NoError(t, err)

	var payload string
	require.NoError(t, db.QueryRow(`SELECT payload FROM backups WHERE id=?`, meta.ID).Scan(&payload))

	var doc dumpDocument
	require.NoError(t, json.Unmarshal([]byte(payload), &doc))
	for _, name := range []string{"users", "sessions", "url_tokens"} {
		_, present := doc.Data[name]
		require.False(t, present, "%s must not appear in a dump's data", name)
	}
	require.Contains(t, doc.Data, "api_keys", "api_keys is included, but only as ciphertext")
	require.Contains(t, doc.Data, "settings")

	for _, secret := range []string{
		providerSecret, unifiedSecret, relaySecret, fullCP,
		"USERHASHSECRET", "SESSIONHASHSECRET", "URLTOKENHASHSECRET",
	} {
		require.NotContainsf(t, payload, secret, "a planted secret leaked into the dump")
	}
	require.Contains(t, payload, "install-local",
		"the header must record why the machine key is absent")
}

// TestRestoreRefusesMismatchedDumpAndChangesNothing is the whole safety
// property: a schema mismatch is refused, and — the part that actually matters
// — a row written before the refused restore is untouched afterwards, and no
// snapshot was taken. "Returned an error" and "changed nothing" are different
// claims; only the second is safe.
func TestRestoreRefusesMismatchedDumpAndChangesNothing(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	ctx := context.Background()
	db := s.engine.DB()

	tables, err := listBackupTables(ctx, db)
	require.NoError(t, err)
	doc, err := buildDump(ctx, db, tables, time.Now())
	require.NoError(t, err)
	doc.Schema = "prowl-gateway schema v999 (99 migrations)"
	tampered, err := json.Marshal(doc)
	require.NoError(t, err)
	badID := insertBackupPayload(t, s, string(tampered))

	_, err = db.Exec(`INSERT INTO model_overrides(model_id, weight) VALUES(?, ?)`, "marker", 0.5)
	require.NoError(t, err)

	_, err = s.restoreBackup(ctx, badID)
	require.Error(t, err)
	var he httpError
	require.True(t, errors.As(err, &he))
	require.Equal(t, http.StatusConflict, he.status)

	var w float64
	require.NoError(t, db.QueryRow(`SELECT weight FROM model_overrides WHERE model_id=?`, "marker").Scan(&w))
	require.Equal(t, 0.5, w, "a refused restore must not modify the database")

	var snaps int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM backups WHERE source='pre-restore'`).Scan(&snaps))
	require.Equal(t, 0, snaps, "a refused restore must not take a snapshot")
}

// TestRestoreRejectsForeignFileWithFormatMessage: an operator handed a Node
// SQL-text dump or a newer-format file should learn it is the wrong FORMAT, not
// hit a confusing schema-mismatch error.
func TestRestoreRejectsForeignFileWithFormatMessage(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	ctx := context.Background()
	db := s.engine.DB()

	foreignID := insertBackupPayload(t, s, "-- freellmapi backup\nDELETE FROM users;\n")
	_, err := s.restoreBackup(ctx, foreignID)
	var he httpError
	require.True(t, errors.As(err, &he))
	require.Equal(t, http.StatusBadRequest, he.status)
	require.Contains(t, he.message, "not a Prowl gateway backup")

	tables, err := listBackupTables(ctx, db)
	require.NoError(t, err)
	doc, err := buildDump(ctx, db, tables, time.Now())
	require.NoError(t, err)
	doc.Format = 99
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	newerID := insertBackupPayload(t, s, string(b))
	_, err = s.restoreBackup(ctx, newerID)
	require.True(t, errors.As(err, &he))
	require.Equal(t, http.StatusBadRequest, he.status)
	require.Contains(t, he.message, "format")
}

// TestRestoreVerifiesBeforeMutating: a payload whose LAST table names a column
// the live table lacks is rejected in the verification pass, before the FIRST
// table is touched. A streaming restore would have already deleted the first
// table's rows by the time it discovered the bad column.
func TestRestoreVerifiesBeforeMutating(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	ctx := context.Background()
	db := s.engine.DB()

	_, err := db.Exec(`INSERT INTO model_overrides(model_id, weight) VALUES(?, ?)`, "keep", 0.7)
	require.NoError(t, err)

	schema, err := backupSchemaVersion(ctx, db)
	require.NoError(t, err)
	doc := dumpDocument{
		Format:         backupDumpFormat,
		Created:        time.Now().UTC().Format(time.RFC3339),
		Schema:         schema,
		KeyFingerprint: backupKeyFingerprint(ctx, db),
		Tables:         []string{"model_overrides", "settings"},
		ExcludedTables: sortedExcludedTables(),
		Data: map[string]dumpTable{
			"model_overrides": {Columns: []string{"model_id", "weight"}, Rows: [][]any{}},
			"settings":        {Columns: []string{"key", "value", "does_not_exist"}, Rows: [][]any{}},
		},
	}
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	id := insertBackupPayload(t, s, string(b))

	_, err = s.restoreBackup(ctx, id)
	var he httpError
	require.True(t, errors.As(err, &he))
	require.Equal(t, http.StatusBadRequest, he.status)

	var w float64
	require.NoError(t, db.QueryRow(`SELECT weight FROM model_overrides WHERE model_id=?`, "keep").Scan(&w))
	require.Equal(t, 0.7, w, "verification rejected the payload before touching the first table")
	var snaps int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM backups WHERE source='pre-restore'`).Scan(&snaps))
	require.Equal(t, 0, snaps)
}

// TestRestoreRollsBackWholeTransaction: a payload that passes verification but
// fails during writes (a duplicate primary key) must roll back every table, so
// a marker deleted at the start of the restore is back when it finishes.
func TestRestoreRollsBackWholeTransaction(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	ctx := context.Background()
	db := s.engine.DB()

	_, err := db.Exec(`INSERT INTO model_overrides(model_id, weight) VALUES(?, ?)`, "keep", 0.9)
	require.NoError(t, err)

	schema, err := backupSchemaVersion(ctx, db)
	require.NoError(t, err)
	doc := dumpDocument{
		Format:         backupDumpFormat,
		Created:        time.Now().UTC().Format(time.RFC3339),
		Schema:         schema,
		KeyFingerprint: backupKeyFingerprint(ctx, db),
		Tables:         []string{"model_overrides"},
		ExcludedTables: sortedExcludedTables(),
		Data: map[string]dumpTable{
			// Two rows share a primary key; the second INSERT fails.
			"model_overrides": {Columns: []string{"model_id", "weight"}, Rows: [][]any{{"dup", 1}, {"dup", 2}}},
		},
	}
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	id := insertBackupPayload(t, s, string(b))

	_, err = s.restoreBackup(ctx, id)
	require.Error(t, err)
	var he httpError
	require.True(t, errors.As(err, &he))
	require.Equal(t, http.StatusBadRequest, he.status)

	var w float64
	require.NoError(t, db.QueryRow(`SELECT weight FROM model_overrides WHERE model_id=?`, "keep").Scan(&w))
	require.Equal(t, 0.9, w, "a write-phase failure must roll back the DELETE that removed the marker")
}

// TestRestoreSnapshotsRemintsAndNotices covers the happy path: the setting
// reverts to the backed-up value, a pre-restore snapshot is taken, the machine
// key is re-minted (never the old plaintext), and the result carries the notice
// that tells the operator to update clients.
func TestRestoreSnapshotsRemintsAndNotices(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	ctx := context.Background()
	db := s.engine.DB()

	require.NoError(t, s.settings.set(ctx, settingUpdateCheckEnabled, "1"))
	require.NoError(t, s.settings.putRawSetting(ctx, settingUnifiedAPIKey, "old-machine-key"))

	meta, err := createBackup(ctx, db, backupCreateOptions{source: "manual"})
	require.NoError(t, err)

	require.NoError(t, s.settings.set(ctx, settingUpdateCheckEnabled, "0"))

	res, err := s.restoreBackup(ctx, meta.ID)
	require.NoError(t, err)
	require.True(t, res.Success)
	require.NotEmpty(t, res.Notice, "the operator must be told the machine key changed")
	require.Equal(t, "pre-restore", res.Snapshot.Source)

	var snaps int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM backups WHERE source='pre-restore'`).Scan(&snaps))
	require.GreaterOrEqual(t, snaps, 1, "a snapshot is taken before restoring")

	v, err := s.settings.get(ctx, settingUpdateCheckEnabled)
	require.NoError(t, err)
	require.Equal(t, "1", v, "the restore reverted the setting to the backed-up value")

	key, err := s.settings.unifiedAPIKey(ctx)
	require.NoError(t, err)
	require.NotEqual(t, "old-machine-key", string(key), "the excluded machine key is re-minted, not restored")
	require.NotEmpty(t, string(key))
}

// TestBackupHTTPRoundTrip drives the wired session-gated routes end to end:
// create, list, tables, schedule, download, restore and delete, so the surface
// is exercised the way the dashboard reaches it, not just the service layer.
func TestBackupHTTPRoundTrip(t *testing.T) {
	t.Parallel()
	s := testServer(t, Options{})
	h := settingsSession(t, s)
	h["Content-Type"] = "application/json"

	resp, body := do(t, s, http.MethodGet, "/api/backups/tables", "", h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "settings", "the picker lists backupable tables")
	require.NotContains(t, body, `"users"`, "excluded tables are not offered")

	resp, body = do(t, s, http.MethodPost, "/api/backups", `{}`, h)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var created struct {
		Backup backupMeta `json:"backup"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &created))
	require.NotZero(t, created.Backup.ID)
	require.True(t, created.Backup.IsFull)

	resp, body = do(t, s, http.MethodGet, "/api/backups?page=1&pageSize=20", "", h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list struct {
		Items []backupMeta `json:"items"`
		Total int          `json:"total"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	require.Equal(t, 1, list.Total)
	require.Len(t, list.Items, 1)

	resp, body = do(t, s, http.MethodPut, "/api/backups/schedule",
		`{"enabled":true,"time":"04:30","intervalDays":2,"backupPath":""}`, h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `"time":"04:30"`)
	resp, _ = do(t, s, http.MethodPut, "/api/backups/schedule", `{"time":"9:99"}`, h)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	id := strconv.FormatInt(created.Backup.ID, 10)
	resp, body = do(t, s, http.MethodGet, "/api/backups/"+id+"/download", "", h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json; charset=utf-8", resp.Header.Get("Content-Type"))
	require.Contains(t, resp.Header.Get("Content-Disposition"), created.Backup.Filename)
	require.Contains(t, body, `"format": 1`)

	resp, body = do(t, s, http.MethodPost, "/api/backups/"+id+"/restore", "", h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `"success":true`)
	require.Contains(t, body, `"notice"`)

	resp, _ = do(t, s, http.MethodGet, "/api/backups/nope/download", "", h)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp, body = do(t, s, http.MethodDelete, "/api/backups/"+id, "", h)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `"success":true`)
	resp, _ = do(t, s, http.MethodDelete, "/api/backups/"+id, "", h)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
