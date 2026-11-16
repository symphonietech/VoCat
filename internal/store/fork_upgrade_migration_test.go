package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// This fork's migrations 25-28 (SMS test tables, call records, Asterisk
// registrations, card_policies.mbn_profile) were released before upstream
// added its own 25-27, so upstream's three are renumbered to 29-31 here. That
// choice only holds up if a database already sitting at user_version 28 --
// every deployment of this fork -- still receives them.
//
// Upstream's own migration tests start at 24 and so never exercise that path.
// Nothing else does either, and the last time a renumbering went in, the
// suite covered only fresh databases and the upgrade broke unnoticed.
func TestUpgradeFromForkSchema28AppliesUpstreamMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v28.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 28; version++ {
		for _, statement := range migrationStatements(version) {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("schema %d: %v", version, err)
			}
		}
	}
	if _, err := raw.ExecContext(ctx, "PRAGMA user_version = 28"); err != nil {
		t.Fatal(err)
	}

	// A task written by the old schema, to prove the rebuild at 29 carries
	// rows across rather than quietly recreating an empty table.
	//
	// task_type is 'sms' and environment 'vowifi' because schema 28 constrains
	// them to ('sms', 'call', 'public_ip') and ('vowifi', 'cellular').
	// 'cellular_attach' only becomes legal once upstream's rebuild at 29 has
	// run, which is the thing under test.
	legacy := &Store{db: raw}
	mustSaveDevice(t, legacy, "modem", "Modem")
	if _, err := raw.ExecContext(ctx, `INSERT INTO automatic_tasks (
		name, enabled, device_id, profile_iccid, profile_aid, task_type,
		environment, interval_days, start_date, run_time, timezone, payload_json,
		retry_count, notify, next_run_at, last_run_at, last_status,
		created_at, updated_at
	) VALUES ('survives', 1, 'modem', '', '', 'sms', 'vowifi', 1, '', '', 'UTC',
		'{}', 0, 0, ?, 0, '', ?, ?)`,
		time.Now().Add(time.Hour).Unix(), time.Now().Unix(), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database := openTestStore(t, path)

	var version int
	if err := database.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// Migration 30: upstream's revert_profile column.
	tasks, err := database.ListAutomaticTasks(ctx)
	if err != nil {
		t.Fatalf("ListAutomaticTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Name != "survives" {
		t.Fatalf("tasks = %+v, want the row written at schema 28 to survive", tasks)
	}
	if !tasks[0].RevertProfile {
		t.Fatal("revert_profile did not default to true, so migration 30 did not run")
	}

	// Migration 31: upstream's raw SMS table.
	var name string
	if err := database.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'sms_raw_records'`,
	).Scan(&name); err != nil {
		t.Fatalf("sms_raw_records is absent, so migration 31 did not run: %v", err)
	}

	// Our own 25-28 must still be intact after upstream's ran on top.
	for _, table := range []string{"smstest_endpoints", "call_records", "asterisk_registrations"} {
		if err := database.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&name); err != nil {
			t.Fatalf("%s is missing after the upgrade: %v", table, err)
		}
	}
	var mbn int
	if err := database.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('card_policies') WHERE name = 'mbn_profile'`,
	).Scan(&mbn); err != nil || mbn != 1 {
		t.Fatalf("card_policies.mbn_profile count = %d error = %v", mbn, err)
	}

	var table string
	if err := database.db.QueryRowContext(ctx, "PRAGMA foreign_key_check").Scan(&table); err != sql.ErrNoRows {
		t.Fatalf("foreign key check failed after the upgrade: %v", err)
	}
}

// A fresh database must also land on the full chain, with upstream's
// renumbered migrations applied in their new positions.
func TestFreshDatabaseAppliesTheWholeChain(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t, filepath.Join(t.TempDir(), "fresh.db"))
	var version int
	if err := database.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
	for _, table := range []string{
		"smstest_endpoints", "call_records", "asterisk_registrations",
		"automatic_tasks", "automatic_task_runs", "sms_raw_records",
		"api_tokens", "sso_bindings", "sso_flows",
	} {
		var name string
		if err := database.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&name); err != nil {
			t.Fatalf("%s missing from a fresh database: %v", table, err)
		}
	}
}

// Every deployment of this fork sits at user_version 31 with api_tokens in
// the layout this fork created at 24. Upstream's token table arrives as 32,
// which must rebuild that table, not skip it: upstream's CREATE TABLE IF NOT
// EXISTS would be a no-op here and its token code would then fail on columns
// that do not exist. The tokens already issued must keep authenticating,
// which means their hashes survive untouched.
func TestUpgradeFromForkSchema31RebuildsAPITokens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v31.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 31; version++ {
		for _, statement := range migrationStatements(version) {
			if _, err := raw.ExecContext(ctx, statement); err != nil {
				t.Fatalf("schema %d: %v", version, err)
			}
		}
	}
	if _, err := raw.ExecContext(ctx, "PRAGMA user_version = 31"); err != nil {
		t.Fatal(err)
	}

	expires := time.Now().Add(24 * time.Hour).Unix()
	created := time.Now().Add(-time.Hour).Unix()
	rows := []struct {
		id       int64
		name     string
		hash     []byte
		lastUsed any
	}{
		{1, "claude", []byte("hash-prefix-less-token-000000001"), nil},
		{2, "claude", []byte("hash-vocat-at-token-000000000002"), int64(1_700_000_000)},
		{3, "my script", []byte("hash-name-with-a-space-000000003"), nil},
		{4, "-leading", []byte("hash-leading-hyphen-000000000004"), nil},
		{5, "ok.name_1-x", []byte("hash-every-allowed-character-005"), nil},
	}
	for _, row := range rows {
		if _, err := raw.ExecContext(ctx,
			`INSERT INTO api_tokens (id, name, token_hash, created_at, expires_at, last_used_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			row.id, row.name, row.hash, created, expires, row.lastUsed); err != nil {
			t.Fatalf("seed token %d: %v", row.id, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database := openTestStore(t, path)

	var version int
	if err := database.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	wantNames := map[int64]string{
		1: "claude",
		2: "legacy-2", // repeats an earlier name; tokens are revoked by name now
		3: "legacy-3", // a space is not allowed in a name
		4: "legacy-4", // must start with a letter or digit
		5: "ok.name_1-x",
	}
	for _, row := range rows {
		token, err := database.APITokenByHash(ctx, row.hash)
		if err != nil {
			t.Fatalf("token %d is not found by its hash after the upgrade: %v", row.id, err)
		}
		if token.Name != wantNames[row.id] {
			t.Fatalf("token %d name = %q, want %q", row.id, token.Name, wantNames[row.id])
		}
		if token.ReadOnly {
			t.Fatalf("token %d became read-only; every existing token had full access", row.id)
		}
		if token.ExpiresAt.Unix() != expires {
			t.Fatalf("token %d expires_at = %v, want %v", row.id, token.ExpiresAt.Unix(), expires)
		}
		if row.lastUsed == nil && !token.LastUsedAt.IsZero() {
			t.Fatalf("token %d last_used_at = %v, want zero for never used", row.id, token.LastUsedAt)
		}
		if row.lastUsed != nil && token.LastUsedAt.Unix() != row.lastUsed.(int64) {
			t.Fatalf("token %d last_used_at = %v, want %v", row.id, token.LastUsedAt.Unix(), row.lastUsed)
		}
	}
	// Upstream's code revokes by name, which needs the new names to resolve.
	if err := database.DeleteAPIToken(ctx, "legacy-3"); err != nil {
		t.Fatalf("revoke a renamed token by name: %v", err)
	}
	// And the table now enforces what upstream's code assumes.
	if _, err := database.CreateAPIToken(ctx, "claude", []byte("hash-a-second-claude-00000000006"), false, time.Time{}); err == nil {
		t.Fatal("a duplicate token name was accepted; name must be UNIQUE after migration 32")
	}

	// Migrations 33 and 34: upstream's SSO tables and session cookie policy.
	var name string
	for _, table := range []string{"sso_bindings", "sso_flows"} {
		if err := database.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&name); err != nil {
			t.Fatalf("%s is absent, so migration 33 did not run: %v", table, err)
		}
	}
	for column, migration := range map[string]int{"login_method": 33, "sso_binding_id": 33, "secure_cookies": 34} {
		var count int
		if err := database.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = ?`, column,
		).Scan(&count); err != nil || count != 1 {
			t.Fatalf("sessions.%s count = %d error = %v, so migration %d did not run", column, count, err, migration)
		}
	}

	var table string
	if err := database.db.QueryRowContext(ctx, "PRAGMA foreign_key_check").Scan(&table); err != sql.ErrNoRows {
		t.Fatalf("foreign key check failed after the upgrade: %v", err)
	}
}
