package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/neur0map/prowl/internal/gateway"
)

// The backup family: a policy-filtered structured dump the operator can
// download, and a guarded restore. Ported from FreeLLMAPI's services/backups.ts
// and routes/backups.ts, with three deliberate divergences documented at their
// call sites: the dump is a structured JSON document rather than SQL text (no
// byte of a backup file is ever parsed as SQL on restore), the payload lives in
// the row rather than on disk (so there is no path to confine or traverse), and
// the machine API key is never carried in a dump.
func (s *Server) registerBackupsRoutes() {
	s.mux.HandleFunc("GET /api/backups/tables", s.RequireSession(s.handleBackupTables))
	s.mux.HandleFunc("GET /api/backups/schedule", s.RequireSession(s.handleBackupScheduleGet))
	s.mux.HandleFunc("PUT /api/backups/schedule", s.RequireSession(s.handleBackupSchedulePut))
	s.mux.HandleFunc("GET /api/backups", s.RequireSession(s.handleBackupList))
	s.mux.HandleFunc("POST /api/backups", s.RequireSession(s.handleBackupCreate))
	s.mux.HandleFunc("GET /api/backups/{id}/download", s.RequireSession(s.handleBackupDownload))
	s.mux.HandleFunc("POST /api/backups/{id}/restore", s.RequireSession(s.handleBackupRestore))
	s.mux.HandleFunc("DELETE /api/backups/{id}", s.RequireSession(s.handleBackupDelete))
}

// backupDumpFormat is the on-disk dump layout version. Restore refuses any
// value it does not know, and the number is bumped only when the structure
// changes in a way an older restore path would misread (backups.ts:15).
const backupDumpFormat = 1

// backupExcludedTables is the ONE place the dump policy names the tables a
// backup must never carry, auditable at a glance. `users` holds password
// hashes, `sessions` holds live login tokens, and `url_tokens` holds bearer
// tokens that grant inference access; restoring any of them would overwrite the
// current operator's accounts and sessions or resurrect revoked tokens
// (backups.ts:61). A dump this server writes never contains them, and a restore
// refuses a file that names one.
var backupExcludedTables = map[string]bool{
	"users":      true,
	"sessions":   true,
	"url_tokens": true,
}

// backupExcludedSettingKeys names the credential rows filtered out of the
// `settings` table when it is dumped. `settings` is otherwise INCLUDED on
// purpose — routing strategy and the like are exactly what an operator expects
// a backup to bring back — but these two are working plaintext credentials, and
// a backup file gets copied to places less protected than its 0600 origin. A
// restore therefore re-mints the machine key rather than carrying the old one;
// the dump header and the restore response both say so.
var backupExcludedSettingKeys = map[string]bool{
	settingUnifiedAPIKey:   true,
	settingFetchRelayToken: true,
}

// backupRestoreNotice is returned to the client and recorded in the dump header
// so an operator restoring months later learns from the file, not from broken
// applications, that the machine key changed.
const backupRestoreNotice = "The machine API key was re-minted on restore; update any client configured with the previous key from Settings."

const backupDumpNote = "The machine API key and the proxy relay token are install-local credentials: a backup never carries them, and a restore re-mints the machine key. Retrieve the new key from Settings and update any client that used the old one."

// backupInternalTable reports whether a table is bookkeeping the dump never
// touches: the goose migration ledger (restore checks the schema version
// against it rather than overwriting it), the backups index itself (dumping it
// would wipe the metadata tracking the files), and SQLite's own tables.
func backupInternalTable(name string) bool {
	return name == "goose_db_version" || name == "backups" || strings.HasPrefix(name, "sqlite_")
}

// backupableTable reports whether a table may appear in a dump.
func backupableTable(name string) bool {
	return !backupInternalTable(name) && !backupExcludedTables[name]
}

// listBackupTables returns every table a dump may contain, in a stable order.
func listBackupTables(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if backupableTable(name) {
			out = append(out, name)
		}
	}
	return out, rows.Err()
}

// backupSchemaVersion identifies the schema a dump was taken against, built
// from the goose ledger. Restoring a dump taken at a different migration state
// would insert into columns that no longer exist or drop columns added since;
// the header lets restore refuse instead. Because the string is built from
// Prowl's own migration versions, a dump from the Node reference never matches
// it — the two builds deliberately do not interoperate (backups.ts:176-180).
func backupSchemaVersion(ctx context.Context, db *sql.DB) (string, error) {
	var (
		count  int
		latest sql.NullInt64
	)
	err := db.QueryRowContext(ctx,
		`SELECT count(*), max(version_id) FROM goose_db_version WHERE is_applied = 1`).
		Scan(&count, &latest)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("prowl-gateway schema v%d (%d migrations)", latest.Int64, count), nil
}

// backupKeyFingerprint returns the fingerprint of the encryption key the key
// vault sealed provider credentials with. api_keys rows carry AES-GCM
// ciphertext keyed by it; restoring a dump written under a different key would
// load provider keys nothing on this server can decrypt, so restore refuses a
// mismatch. The vault records the fingerprint in settings on open.
func backupKeyFingerprint(ctx context.Context, db *sql.DB) string {
	var fp string
	err := db.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, "keys.master_fingerprint").Scan(&fp)
	if err != nil || strings.TrimSpace(fp) == "" {
		return "none"
	}
	return fp
}

// ── Dump document ────────────────────────────────────────────────────────────

// dumpTable is one table's data: the ordered column names and rows as arrays of
// JSON values, so restore can bind every cell as a parameter without parsing a
// line of SQL.
type dumpTable struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// dumpDocument is the structured backup file.
type dumpDocument struct {
	Format         int                  `json:"format"`
	Created        string               `json:"created"`
	Schema         string               `json:"schema"`
	KeyFingerprint string               `json:"keyFingerprint"`
	Tables         []string             `json:"tables"`
	ExcludedTables []string             `json:"excludedTables"`
	Note           string               `json:"note"`
	Data           map[string]dumpTable `json:"data"`
}

// tableColumns returns the column names of a table in definition order, or an
// error when the table does not exist. It is the authority a restore checks
// every column in a payload against.
func tableColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("no such table %q", table)
	}
	return cols, nil
}

// dumpTableData reads one table into a structured form. Values come back typed
// by the driver (int64/float64/string/nil), and a []byte is normalised to a
// string so a TEXT column round-trips as JSON rather than base64. The settings
// table is the one place a row filter applies, dropping the credential keys the
// policy excludes.
func dumpTableData(ctx context.Context, db *sql.DB, table string) (dumpTable, error) {
	query := fmt.Sprintf(`SELECT * FROM %s`, quoteIdent(table))
	var args []any
	if table == "settings" {
		var placeholders []string
		for key := range backupExcludedSettingKeys {
			placeholders = append(placeholders, "?")
			args = append(args, key)
		}
		if len(placeholders) > 0 {
			query += ` WHERE key NOT IN (` + strings.Join(placeholders, ", ") + `)`
		}
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return dumpTable{}, err
	}
	defer rows.Close()
	colNames, err := rows.Columns()
	if err != nil {
		return dumpTable{}, err
	}
	out := dumpTable{Columns: colNames, Rows: [][]any{}}
	for rows.Next() {
		cells := make([]any, len(colNames))
		ptrs := make([]any, len(colNames))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return dumpTable{}, err
		}
		for i, c := range cells {
			if b, ok := c.([]byte); ok {
				cells[i] = string(b)
			}
		}
		out.Rows = append(out.Rows, cells)
	}
	return out, rows.Err()
}

// quoteIdent renders a SQLite identifier that this code has already verified
// against sqlite_master or pragma_table_info. A double quote can never reach a
// real object name, but the check is defence in depth in case a future caller
// forgets the allow-list.
func quoteIdent(name string) string {
	if strings.Contains(name, `"`) {
		// A verified identifier cannot contain one; refuse to build SQL from it.
		panic("backup: identifier contains a double quote: " + name)
	}
	return `"` + name + `"`
}

func buildDump(ctx context.Context, db *sql.DB, tables []string, created time.Time) (dumpDocument, error) {
	schema, err := backupSchemaVersion(ctx, db)
	if err != nil {
		return dumpDocument{}, err
	}
	doc := dumpDocument{
		Format:         backupDumpFormat,
		Created:        created.UTC().Format(time.RFC3339),
		Schema:         schema,
		KeyFingerprint: backupKeyFingerprint(ctx, db),
		Tables:         tables,
		ExcludedTables: sortedExcludedTables(),
		Note:           backupDumpNote,
		Data:           map[string]dumpTable{},
	}
	for _, table := range tables {
		data, err := dumpTableData(ctx, db, table)
		if err != nil {
			return dumpDocument{}, err
		}
		doc.Data[table] = data
	}
	return doc, nil
}

func sortedExcludedTables() []string {
	// A fixed, sorted view of the named exclusion list for the header.
	out := make([]string, 0, len(backupExcludedTables))
	for name := range backupExcludedTables {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// ── Backup metadata ──────────────────────────────────────────────────────────

// backupMeta is the wire shape the dashboard renders. createdAt is an RFC3339
// string, not the stored Unix seconds, because the client parses it as a date.
type backupMeta struct {
	ID        int64    `json:"id"`
	Filename  string   `json:"filename"`
	Filesize  int64    `json:"filesize"`
	IsFull    bool     `json:"isFull"`
	Source    string   `json:"source"`
	CreatedAt string   `json:"createdAt"`
	Tables    []string `json:"tables"`
}

type backupCreateOptions struct {
	tables []string
	source string
}

// createBackup builds a policy-filtered dump of the selected tables (all of
// them when none are named) and records it. Requested table names are
// intersected with the real, backupable table list before any of them reaches
// a query, so a caller cannot name a table outside the policy.
func createBackup(ctx context.Context, db *sql.DB, opts backupCreateOptions) (backupMeta, error) {
	available, err := listBackupTables(ctx, db)
	if err != nil {
		return backupMeta{}, err
	}
	allowed := map[string]bool{}
	for _, t := range available {
		allowed[t] = true
	}
	var requested []string
	for _, t := range opts.tables {
		if allowed[t] {
			requested = append(requested, t)
		}
	}
	isFull := len(requested) == 0
	tables := requested
	if isFull {
		tables = available
	}
	if len(tables) == 0 {
		return backupMeta{}, httpErr(http.StatusBadRequest, "no tables to back up")
	}

	source := opts.source
	if source != "scheduled" && source != "pre-restore" {
		source = "manual"
	}
	now := time.Now()
	doc, err := buildDump(ctx, db, tables, now)
	if err != nil {
		return backupMeta{}, err
	}
	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return backupMeta{}, err
	}
	tablesJSON, err := json.Marshal(tables)
	if err != nil {
		return backupMeta{}, err
	}

	prefix := "backup"
	switch source {
	case "scheduled":
		prefix = "auto-backup"
	case "pre-restore":
		prefix = "pre-restore"
	}
	stampRand := make([]byte, 3)
	if _, err := rand.Read(stampRand); err != nil {
		return backupMeta{}, err
	}
	filename := fmt.Sprintf("%s-%s-%s.json", prefix, now.UTC().Format("20060102-150405"), hex.EncodeToString(stampRand))

	res, err := db.ExecContext(ctx,
		`INSERT INTO backups (filename, filesize, is_full, source, created_at, tables_json, payload)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		filename, len(payload), boolToInt(isFull), source, now.Unix(), string(tablesJSON), string(payload))
	if err != nil {
		return backupMeta{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return backupMeta{}, err
	}
	return backupMeta{
		ID:        id,
		Filename:  filename,
		Filesize:  int64(len(payload)),
		IsFull:    isFull,
		Source:    source,
		CreatedAt: now.UTC().Format(time.RFC3339),
		Tables:    tables,
	}, nil
}

type backupRow struct {
	id         int64
	filename   string
	filesize   int64
	isFull     int
	source     string
	createdAt  int64
	tablesJSON string
}

const backupSelectColumns = "id, filename, filesize, is_full, source, created_at, tables_json"

func (r backupRow) meta() backupMeta {
	var tables []string
	if err := json.Unmarshal([]byte(r.tablesJSON), &tables); err != nil {
		tables = []string{}
	}
	if tables == nil {
		tables = []string{}
	}
	return backupMeta{
		ID:        r.id,
		Filename:  r.filename,
		Filesize:  r.filesize,
		IsFull:    r.isFull == 1,
		Source:    r.source,
		CreatedAt: time.Unix(r.createdAt, 0).UTC().Format(time.RFC3339),
		Tables:    tables,
	}
}

func scanBackupRow(scan func(dest ...any) error) (backupRow, error) {
	var r backupRow
	err := scan(&r.id, &r.filename, &r.filesize, &r.isFull, &r.source, &r.createdAt, &r.tablesJSON)
	return r, err
}

func readBackupRecord(ctx context.Context, db *sql.DB, id int64) (backupRow, error) {
	row := db.QueryRowContext(ctx,
		`SELECT `+backupSelectColumns+` FROM backups WHERE id = ?`, id)
	r, err := scanBackupRow(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return backupRow{}, httpErr(http.StatusNotFound, "backup not found")
	}
	return r, err
}

// ── Restore ──────────────────────────────────────────────────────────────────

type restoreResult struct {
	Success  bool       `json:"success"`
	Backup   backupMeta `json:"backup"`
	Snapshot backupMeta `json:"snapshot"`
	Notice   string     `json:"notice"`
}

// restorePlan is the fully verified set of writes, resolved before a single row
// is touched so a bad column in the last table cannot corrupt the first.
type restorePlan struct {
	table   string
	columns []string
	rows    [][]any
}

// restoreBackup applies a dump. Everything is verified first — the format, the
// schema version, the encryption-key fingerprint, and then every table name and
// every column against the live database — and only then, after a pre-restore
// snapshot, does one transaction delete and reload the affected tables. Any
// failure rolls the whole transaction back, so a restore that fails on its
// fourth table leaves the first three exactly as they were.
func (s *Server) restoreBackup(ctx context.Context, id int64) (restoreResult, error) {
	db := s.engine.DB()
	row, err := readBackupRecord(ctx, db, id)
	if err != nil {
		return restoreResult{}, err
	}

	var payload string
	if err := db.QueryRowContext(ctx, `SELECT payload FROM backups WHERE id = ?`, id).Scan(&payload); err != nil {
		return restoreResult{}, err
	}

	var doc dumpDocument
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return restoreResult{}, httpErr(http.StatusBadRequest,
			"this file is not a Prowl gateway backup (it could not be read as one)")
	}
	if doc.Format != backupDumpFormat {
		return restoreResult{}, httpErr(http.StatusBadRequest,
			fmt.Sprintf("this backup uses format %d; this server reads format %d", doc.Format, backupDumpFormat))
	}

	current, err := backupSchemaVersion(ctx, db)
	if err != nil {
		return restoreResult{}, err
	}
	if doc.Schema != current {
		return restoreResult{}, httpErr(http.StatusConflict,
			fmt.Sprintf("this backup was taken at a different schema version (backup: %s; database: %s). Restore it into a server running the matching version.", doc.Schema, current))
	}
	if fp := backupKeyFingerprint(ctx, db); doc.KeyFingerprint != fp {
		return restoreResult{}, httpErr(http.StatusConflict,
			fmt.Sprintf("this backup was written under a different encryption key (backup: %s; server: %s). The stored provider keys could not be decrypted. Restore it on the server that holds the original key.", doc.KeyFingerprint, fp))
	}

	// Verify EVERYTHING before mutating. Every table named in the payload must
	// be a real, backupable table, and every column must exist on it, resolved
	// from the live database rather than trusted from the file.
	plans, err := s.planRestore(ctx, db, doc)
	if err != nil {
		return restoreResult{}, err
	}

	// The safety net, written before the first row changes: a restore that
	// turns out to be the wrong file is one restore away from being undone.
	snapshot, err := createBackup(ctx, db, backupCreateOptions{source: "pre-restore"})
	if err != nil {
		return restoreResult{}, err
	}

	if err := applyRestore(ctx, db, plans); err != nil {
		return restoreResult{}, httpErr(http.StatusBadRequest,
			fmt.Sprintf("restore failed and was rolled back; the database is unchanged (%s). A snapshot of the current data was saved as %s.", gateway.Redact(err.Error()), snapshot.Filename))
	}

	// The machine key was deliberately excluded from the dump, so it is gone
	// after the settings reload; seed a fresh one now so the state is defined
	// and the notice below is accurate.
	if _, err := s.settings.unifiedAPIKey(ctx); err != nil {
		return restoreResult{}, err
	}

	return restoreResult{Success: true, Backup: row.meta(), Snapshot: snapshot, Notice: backupRestoreNotice}, nil
}

// planRestore resolves and validates every write the dump asks for, touching
// nothing. A table the file names that is excluded, unknown, or carries a
// column the live table does not have is refused here, before any mutation.
func (s *Server) planRestore(ctx context.Context, db *sql.DB, doc dumpDocument) ([]restorePlan, error) {
	if len(doc.Data) != len(doc.Tables) {
		return nil, httpErr(http.StatusBadRequest, "this backup's table list and data do not agree")
	}
	plans := make([]restorePlan, 0, len(doc.Tables))
	for _, table := range doc.Tables {
		if backupExcludedTables[table] {
			return nil, httpErr(http.StatusBadRequest,
				fmt.Sprintf("this backup writes to %q, which backups never include; refusing to restore it", table))
		}
		if !backupableTable(table) {
			return nil, httpErr(http.StatusBadRequest, fmt.Sprintf("this backup names a table Prowl does not back up: %q", table))
		}
		data, ok := doc.Data[table]
		if !ok {
			return nil, httpErr(http.StatusBadRequest, fmt.Sprintf("this backup lists table %q but carries no data for it", table))
		}
		live, err := tableColumns(ctx, db, table)
		if err != nil {
			return nil, httpErr(http.StatusBadRequest, fmt.Sprintf("this backup names a table this database does not have: %q", table))
		}
		liveSet := map[string]bool{}
		for _, c := range live {
			liveSet[c] = true
		}
		for _, c := range data.Columns {
			if !liveSet[c] {
				return nil, httpErr(http.StatusBadRequest,
					fmt.Sprintf("this backup names a column %q that table %q does not have", c, table))
			}
		}
		for _, r := range data.Rows {
			if len(r) != len(data.Columns) {
				return nil, httpErr(http.StatusBadRequest,
					fmt.Sprintf("a row in table %q has %d values for %d columns", table, len(r), len(data.Columns)))
			}
		}
		plans = append(plans, restorePlan{table: table, columns: data.Columns, rows: data.Rows})
	}
	return plans, nil
}

// applyRestore performs every delete and insert in ONE transaction on a pinned
// connection with foreign-key enforcement disabled for its duration (SQLite
// only honours the pragma outside a transaction). Disabling enforcement makes
// table order irrelevant — the dump reloads whole tables, which transiently
// breaks references between them — and the single transaction makes the whole
// restore atomic.
func applyRestore(ctx context.Context, db *sql.DB, plans []restorePlan) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	// Restore enforcement on the way out regardless of outcome; the pinned
	// connection is returned to the pool with foreign keys back on.
	defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys = ON") }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, p := range plans {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+quoteIdent(p.table)); err != nil {
			return err
		}
		if len(p.rows) == 0 {
			continue
		}
		cols := make([]string, len(p.columns))
		placeholders := make([]string, len(p.columns))
		for i, c := range p.columns {
			cols[i] = quoteIdent(c)
			placeholders[i] = "?"
		}
		stmt := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
			quoteIdent(p.table), strings.Join(cols, ", "), strings.Join(placeholders, ", "))
		for _, r := range p.rows {
			args := make([]any, len(r))
			for i, v := range r {
				args[i] = coerceCell(v)
			}
			if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// coerceCell turns a decoded JSON value back into something SQLite stores with
// the right affinity. A json.Number becomes an int64 when it is integral, so an
// id or a Unix-seconds timestamp does not round-trip into a float; otherwise it
// becomes a float64. Everything else (string, nil) binds as-is.
func coerceCell(v any) any {
	if n, ok := v.(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := n.Float64(); err == nil {
			return f
		}
		return n.String()
	}
	return v
}

// ── Schedule ─────────────────────────────────────────────────────────────────

const backupScheduleSetting = "backup_schedule"

// backupSchedule is stored and returned faithfully, but Prowl runs no
// auto-backup timer: the setting is honoured as state the dashboard round-trips,
// not as behaviour. An operator relying on scheduled backups gets an honest
// stored schedule that nothing acts on rather than a silent no-op that looks
// armed.
type backupSchedule struct {
	Enabled      bool   `json:"enabled"`
	Time         string `json:"time"`
	IntervalDays int    `json:"intervalDays"`
	BackupPath   string `json:"backupPath"`
}

func defaultBackupSchedule() backupSchedule {
	return backupSchedule{Enabled: false, Time: "03:00", IntervalDays: 1, BackupPath: ""}
}

var backupTimePattern = regexpMustCompileHHMM()

func (s *Server) readBackupSchedule(ctx context.Context) backupSchedule {
	var raw string
	err := s.engine.DB().QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, backupScheduleSetting).Scan(&raw)
	if err != nil || raw == "" {
		return defaultBackupSchedule()
	}
	var parsed backupSchedule
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return defaultBackupSchedule()
	}
	def := defaultBackupSchedule()
	if !backupTimePattern(parsed.Time) {
		parsed.Time = def.Time
	}
	if parsed.IntervalDays < 1 {
		parsed.IntervalDays = def.IntervalDays
	}
	return parsed
}

// ── Handlers ─────────────────────────────────────────────────────────────────

func (s *Server) handleBackupTables(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	tables, err := listBackupTables(r.Context(), s.engine.DB())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not list tables")
		return
	}
	if tables == nil {
		tables = []string{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"tables": tables})
}

func (s *Server) handleBackupScheduleGet(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	WriteJSON(w, http.StatusOK, map[string]any{"schedule": s.readBackupSchedule(r.Context())})
}

func (s *Server) handleBackupSchedulePut(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var body backupSchedule
	if !DecodeJSON(w, r, &body) {
		return
	}
	if body.Time == "" {
		body.Time = defaultBackupSchedule().Time
	}
	if !backupTimePattern(body.Time) {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "time must be HH:mm")
		return
	}
	if body.IntervalDays < 1 || body.IntervalDays > 365 {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "intervalDays must be between 1 and 365")
		return
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not save the schedule")
		return
	}
	_, err = s.engine.DB().ExecContext(r.Context(),
		`INSERT INTO settings(key, value, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		backupScheduleSetting, string(encoded), time.Now().Unix())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not save the schedule")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"schedule": body})
}

func (s *Server) handleBackupList(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	page := atoiDefault(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := atoiDefault(r.URL.Query().Get("pageSize"), 20)
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	ctx := r.Context()

	var total int
	if err := s.engine.DB().QueryRowContext(ctx, `SELECT count(*) FROM backups`).Scan(&total); err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not list backups")
		return
	}
	rows, err := s.engine.DB().QueryContext(ctx,
		`SELECT `+backupSelectColumns+` FROM backups ORDER BY id DESC LIMIT ? OFFSET ?`,
		pageSize, (page-1)*pageSize)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "could not list backups")
		return
	}
	defer rows.Close()
	items := []backupMeta{}
	for rows.Next() {
		rec, err := scanBackupRow(rows.Scan)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, TypeServer, "could not list backups")
			return
		}
		items = append(items, rec.meta())
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (s *Server) handleBackupCreate(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	var body struct {
		Tables []string `json:"tables"`
	}
	// An empty body is a full backup; only reject a body that is present and
	// malformed.
	if r.ContentLength != 0 {
		if !DecodeJSON(w, r, &body) {
			return
		}
	}
	meta, err := createBackup(r.Context(), s.engine.DB(), backupCreateOptions{tables: body.Tables, source: "manual"})
	if err != nil {
		writeBackupError(w, err, http.StatusInternalServerError, "backup failed")
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"backup": meta})
}

func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	id, ok := parseBackupID(w, r)
	if !ok {
		return
	}
	var (
		filename string
		payload  string
	)
	err := s.engine.DB().QueryRowContext(r.Context(),
		`SELECT filename, payload FROM backups WHERE id = ?`, id).Scan(&filename, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, TypeNotFound, "backup not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "download failed")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(payload))
}

func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	id, ok := parseBackupID(w, r)
	if !ok {
		return
	}
	result, err := s.restoreBackup(r.Context(), id)
	if err != nil {
		writeBackupError(w, err, http.StatusBadRequest, "restore failed")
		return
	}
	WriteJSON(w, http.StatusOK, result)
}

func (s *Server) handleBackupDelete(w http.ResponseWriter, r *http.Request, _ gateway.SessionUser) {
	id, ok := parseBackupID(w, r)
	if !ok {
		return
	}
	res, err := s.engine.DB().ExecContext(r.Context(), `DELETE FROM backups WHERE id = ?`, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, TypeServer, "delete failed")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		WriteError(w, http.StatusNotFound, TypeNotFound, "backup not found")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"success": true})
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// httpError carries a status alongside a message so a service-layer failure can
// choose the client-facing code, mirroring the reference's `httpError` helper.
type httpError struct {
	status  int
	message string
}

func (e httpError) Error() string { return e.message }

func httpErr(status int, message string) error { return httpError{status: status, message: message} }

func writeBackupError(w http.ResponseWriter, err error, fallbackStatus int, fallbackMessage string) {
	var he httpError
	if errors.As(err, &he) {
		WriteError(w, he.status, backupErrorType(he.status), he.message)
		return
	}
	WriteError(w, fallbackStatus, backupErrorType(fallbackStatus), fallbackMessage)
}

func backupErrorType(status int) ErrorType {
	if status >= 500 {
		return TypeServer
	}
	if status == http.StatusNotFound {
		return TypeNotFound
	}
	return TypeInvalidRequest
}

// parseBackupID reads the {id} path value. Only a bare run of digits is a valid
// id; a permissive parse would accept "12/../etc" and quietly read 12.
func parseBackupID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	if raw == "" || strings.TrimLeft(raw, "0123456789") != "" {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "invalid backup id")
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		WriteError(w, http.StatusBadRequest, TypeInvalidRequest, "invalid backup id")
		return 0, false
	}
	return id, true
}

func atoiDefault(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

// regexpMustCompileHHMM returns an HH:mm validator without pulling regexp into
// the hot path: the schedule format is fixed and cheap to check by hand.
func regexpMustCompileHHMM() func(string) bool {
	return func(s string) bool {
		if len(s) != 5 || s[2] != ':' {
			return false
		}
		hh := s[0:2]
		mm := s[3:5]
		if !allDigits(hh) || !allDigits(mm) {
			return false
		}
		h, _ := strconv.Atoi(hh)
		m, _ := strconv.Atoi(mm)
		return h >= 0 && h <= 23 && m >= 0 && m <= 59
	}
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}
