//go:build integration

package litestream

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sqlite3 runs the sqlite3 CLI tool and returns its combined output.
func sqlite3(dbPath, query string) (string, error) {
	cmd := exec.Command("sqlite3", dbPath, query)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// waitForReplica polls the replica directory recursively until litestream has written
// at least one LTX file, or until timeout expires.
func waitForReplica(replicaDir string, timeout time.Duration) error {
	deadline := time.After(timeout)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			return fmt.Errorf("timeout waiting for LTX files in %s", replicaDir)
		case <-tick.C:
			found, err := hasLTXFile(replicaDir)
			if err != nil {
				continue
			}
			if found {
				return nil
			}
		}
	}
}

// hasLTXFile walks the directory tree and returns true if any .ltx file is found.
func hasLTXFile(dir string) (bool, error) {
	var found bool
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".ltx") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found, err
}

// runLitestreamReplicate starts litestream replication, waits for a sync,
// then shuts it down gracefully. Called by integration tests to produce
// a replica that can be restored from.
func runLitestreamReplicate(t *testing.T, ctx context.Context, binPath, dbPath, replicaURL string, timeout time.Duration) {
	t.Helper()

	s := &Sidecar{
		DBPath:     dbPath,
		BinaryPath: binPath,
		ReplicaURL: replicaURL,
	}

	// RestoreIfNeeded should be a no-op since DB already exists
	if err := s.RestoreIfNeeded(ctx); err != nil {
		t.Fatalf("RestoreIfNeeded: %v", err)
	}

	// Start replication
	cmd, err := s.Replicate(ctx)
	if err != nil {
		t.Fatalf("Replicate: %v", err)
	}

	// Wait for litestream to sync — poll the replica directory
	if err := waitForReplica(replicaURLToDir(replicaURL), timeout); err != nil {
		_ = Shutdown(cmd, 5*time.Second)
		t.Fatalf("waitForReplica: %v", err)
	}

	// Stop replication
	if err := Shutdown(cmd, 5*time.Second); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// replicaURLToDir converts a file:// replica URL to a local directory path.
// Panics if the URL does not start with file:// — test-only helper.
func replicaURLToDir(url string) string {
	if !strings.HasPrefix(url, "file://") {
		panic(fmt.Sprintf("replicaURLToDir: expected file:// URL, got %q", url))
	}
	return strings.TrimPrefix(url, "file://")
}

func TestIntegration_RestoreIfNeeded_FirstBootEmptyReplica(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	binPath, err := exec.LookPath("litestream")
	if err != nil {
		t.Skip("litestream binary not found in PATH; skipping integration test")
	}

	td := t.TempDir()
	dbPath := filepath.Join(td, "app.db")
	replicaURL := "file://" + filepath.ToSlash(filepath.Join(td, "replica"))

	// No local DB and no backups in the replica: a fresh deployment must
	// boot without error and without creating a database.
	s := &Sidecar{
		DBPath:     dbPath,
		BinaryPath: binPath,
		ReplicaURL: replicaURL,
	}
	if err := s.RestoreIfNeeded(context.Background()); err != nil {
		t.Fatalf("RestoreIfNeeded on empty replica: %v", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("expected no database to be created, stat err = %v", err)
	}
}

func TestIntegration_RestoreAndReplicate_URLMode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Verify litestream binary is available
	binPath, err := exec.LookPath("litestream")
	if err != nil {
		t.Skip("litestream binary not found in PATH; skipping integration test")
	}

	// Verify sqlite3 CLI is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not found; skipping integration test")
	}

	td := t.TempDir()

	dbPath := filepath.Join(td, "app.db")
	replicaDir := filepath.Join(td, "replica")
	if err := os.MkdirAll(replicaDir, 0755); err != nil {
		t.Fatal(err)
	}
	replicaURL := "file://" + filepath.ToSlash(replicaDir)

	// Step 1: Create a database and insert some data using sqlite3 CLI
	if _, err := sqlite3(dbPath, "CREATE TABLE test (id INTEGER PRIMARY KEY, val TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite3(dbPath, "INSERT INTO test (id, val) VALUES (1, 'hello'), (2, 'world')"); err != nil {
		t.Fatal(err)
	}

	// Verify data was written
	count, err := sqlite3(dbPath, "SELECT COUNT(*) FROM test")
	if err != nil || count != "2" {
		t.Fatalf("expected 2 rows, got %q (err: %v)", count, err)
	}

	// Step 2: Replicate using litestream
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	runLitestreamReplicate(t, ctx, binPath, dbPath, replicaURL, 10*time.Second)

	// Step 3: Remove the original DB and restore from replica
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}

	s2 := &Sidecar{
		DBPath:     dbPath,
		BinaryPath: binPath,
		ReplicaURL: replicaURL,
	}

	if err := s2.RestoreIfNeeded(ctx); err != nil {
		t.Fatalf("RestoreIfNeeded (after delete): %v", err)
	}

	// Step 4: Verify the restored data
	restoredCount, err := sqlite3(dbPath, "SELECT COUNT(*) FROM test")
	if err != nil {
		t.Fatal(err)
	}
	if restoredCount != "2" {
		t.Errorf("expected 2 rows after restore, got %q", restoredCount)
	}

	// Verify integrity
	integrity, err := sqlite3(dbPath, "PRAGMA integrity_check")
	if err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Errorf("integrity check: %s", integrity)
	}
}

func TestIntegration_ConfigMode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Verify litestream binary is available
	binPath, err := exec.LookPath("litestream")
	if err != nil {
		t.Skip("litestream binary not found in PATH; skipping integration test")
	}

	// Verify sqlite3 CLI is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not found; skipping integration test")
	}

	td := t.TempDir()

	dbPath := filepath.Join(td, "app.db")
	replicaDir := filepath.Join(td, "replica")
	if err := os.MkdirAll(replicaDir, 0755); err != nil {
		t.Fatal(err)
	}
	replicaURL := "file://" + filepath.ToSlash(replicaDir)

	// Write a litestream.yml config file
	configContent := fmt.Sprintf(`
dbs:
  - path: %s
    replicas:
      - url: %s
`, dbPath, replicaURL)
	configPath := filepath.Join(td, "litestream.yml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Step 1: Create a database and insert some data using sqlite3 CLI
	if _, err := sqlite3(dbPath, "CREATE TABLE test (id INTEGER PRIMARY KEY, val TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite3(dbPath, "INSERT INTO test (id, val) VALUES (1, 'hello'), (2, 'world')"); err != nil {
		t.Fatal(err)
	}

	// Verify data was written
	count, err := sqlite3(dbPath, "SELECT COUNT(*) FROM test")
	if err != nil || count != "2" {
		t.Fatalf("expected 2 rows, got %q (err: %v)", count, err)
	}

	// Step 2: Replicate using litestream with config file
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	s := &Sidecar{
		DBPath:     dbPath,
		BinaryPath: binPath,
		ConfigPath: configPath,
	}

	// RestoreIfNeeded should be a no-op since DB already exists
	if err := s.RestoreIfNeeded(ctx); err != nil {
		t.Fatalf("RestoreIfNeeded: %v", err)
	}

	// Start replication
	cmd, err := s.Replicate(ctx)
	if err != nil {
		t.Fatalf("Replicate: %v", err)
	}

	// Wait for litestream to sync
	if err := waitForReplica(replicaDir, 10*time.Second); err != nil {
		_ = Shutdown(cmd, 5*time.Second)
		t.Fatalf("waitForReplica: %v", err)
	}

	// Stop replication
	if err := Shutdown(cmd, 5*time.Second); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Step 3: Remove the original DB and restore from replica using config mode
	if err := os.Remove(dbPath); err != nil {
		t.Fatal(err)
	}

	s2 := &Sidecar{
		DBPath:     dbPath,
		BinaryPath: binPath,
		ConfigPath: configPath,
	}

	if err := s2.RestoreIfNeeded(ctx); err != nil {
		t.Fatalf("RestoreIfNeeded (after delete): %v", err)
	}

	// Step 4: Verify the restored data
	restoredCount, err := sqlite3(dbPath, "SELECT COUNT(*) FROM test")
	if err != nil {
		t.Fatal(err)
	}
	if restoredCount != "2" {
		t.Errorf("expected 2 rows after restore, got %q", restoredCount)
	}

	// Verify integrity
	integrity, err := sqlite3(dbPath, "PRAGMA integrity_check")
	if err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Errorf("integrity check: %s", integrity)
	}
}
