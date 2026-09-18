package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vocat/internal/store"
)

// Writing must be atomic: Asterisk can read this file at any moment, and a
// half-written dialplan is a broken one.
func TestWriteAsteriskRoutesFileReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", routesFileName)

	if err := writeAsteriskRoutesFile(path, "first\n"); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(path); string(body) != "first\n" {
		t.Fatalf("contents = %q", body)
	}
	if err := writeAsteriskRoutesFile(path, "second\n"); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(path); string(body) != "second\n" {
		t.Fatalf("contents after replace = %q", body)
	}
	// The temporary must not survive, or the directory accumulates debris
	// Asterisk might one day be pointed at.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("left a temporary file: %s", entry.Name())
		}
	}
}

// Pending drives the Apply button. It must be true when the file is missing,
// true when it is stale, and false only when disk matches what is stored.
func TestAsteriskRoutesDifferDetectsAStaleFile(t *testing.T) {
	dir := t.TempDir()
	server := &Server{asteriskDialplanDir: dir}

	if !server.asteriskRoutesDiffer("rendered\n") {
		t.Fatal("a missing file reported as up to date")
	}
	if err := writeAsteriskRoutesFile(server.asteriskRoutesPath(), "rendered\n"); err != nil {
		t.Fatal(err)
	}
	if server.asteriskRoutesDiffer("rendered\n") {
		t.Fatal("a matching file reported as pending")
	}
	if !server.asteriskRoutesDiffer("something else\n") {
		t.Fatal("a stale file reported as up to date")
	}
}

// With no directory configured there is nothing to write, and nothing to be
// pending either -- reporting a permanent "unapplied" state would be noise.
func TestAsteriskRoutesPathEmptyWithoutADirectory(t *testing.T) {
	server := &Server{}
	if server.asteriskRoutesPath() != "" {
		t.Fatal("a path was produced with no directory configured")
	}
	if server.asteriskRoutesDiffer("anything") {
		t.Fatal("pending reported with nowhere to write")
	}
}

// A route naming a SIM that has been removed is the one failure the routes
// page cannot otherwise show: the dialplan is valid, Apply succeeds, and the
// call fails at dial time with an error only the caller hears.
func TestUnknownRouteDevicesNamesWhatIsGone(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertDevice(ctx, store.Device{
		ID: "usb-2c7c-0125-3-4-4", Name: "SLOT1-1", DeviceType: store.DeviceTypePCIeEC20EC25,
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: database, logger: regionTestLogger()}

	unknown := server.unknownRouteDevices(ctx, []asteriskRoute{
		// By ID, by name, and one that is gone.
		{Pattern: "_1NXXNXXXXXX", Devices: []string{"usb-2c7c-0125-3-4-4", "slot1-1", "usb-2c7c-0125-3-4-9"}},
		// "*" is every device rather than a name.
		{Pattern: "_011.", Devices: []string{"*"}},
	})
	if len(unknown) != 1 || unknown[0] != "usb-2c7c-0125-3-4-9" {
		t.Fatalf("unknown = %v; want only the removed device", unknown)
	}

	// Nothing to report when every device resolves, so the page stays quiet
	// on a configuration that is fine.
	if got := server.unknownRouteDevices(ctx, []asteriskRoute{
		{Pattern: "_.", Devices: []string{"SLOT1-1"}},
	}); got != nil {
		t.Fatalf("a valid route reported %v", got)
	}
}

// Crying wolf because the device listing failed would be worse than saying
// nothing: the routes may be perfectly fine.
func TestUnknownRouteDevicesStaysQuietWithNoRoutes(t *testing.T) {
	server := &Server{logger: regionTestLogger()}
	if got := server.unknownRouteDevices(context.Background(), nil); got != nil {
		t.Fatalf("an empty route list reported %v", got)
	}
}
