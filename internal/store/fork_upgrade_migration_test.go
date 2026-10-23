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
	} {
		var name string
		if err := database.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&name); err != nil {
			t.Fatalf("%s missing from a fresh database: %v", table, err)
		}
	}
}
