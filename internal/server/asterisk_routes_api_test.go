package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
