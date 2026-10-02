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

func TestIntegration_RestoreIfNeeded_FirstBootEmptyReplica(t *testing.T) {
	requireCLIs(t, "litestream")

	td := t.TempDir()
	dbPath := filepath.Join(td, "app.db")
	s := &Sidecar{
		DBPath:     dbPath,
		ReplicaURL: "file://" + filepath.ToSlash(filepath.Join(td, "replica")),
	}

	if err := s.RestoreIfNeeded(t.Context()); err != nil {
		t.Fatalf("RestoreIfNeeded on empty replica: %v", err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("expected no database to be created, stat err = %v", err)
	}
}

func TestIntegration_RestoreAndReplicate_URLMode(t *testing.T) {
	requireCLIs(t, "litestream", "sqlite3")

	td := t.TempDir()
	replicaDir := filepath.Join(td, "replica")
	s := &Sidecar{
		DBPath:     filepath.Join(td, "app.db"),
		ReplicaURL: "file://" + filepath.ToSlash(replicaDir),
	}

	testReplicateThenRestore(t, s, replicaDir)
}

func TestIntegration_ConfigMode(t *testing.T) {
	requireCLIs(t, "litestream", "sqlite3")
	t.Setenv("LITESTREAM_REPLICA_URL", "")

	td := t.TempDir()
	dbPath := filepath.Join(td, "app.db")
	replicaDir := filepath.Join(td, "replica")
	config := fmt.Sprintf(`
dbs:
  - path: %s
    replicas:
      - url: %s
`, dbPath, "file://"+filepath.ToSlash(replicaDir))
	configPath := filepath.Join(td, "litestream.yml")
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	s := &Sidecar{
		DBPath:     dbPath,
		ConfigPath: configPath,
	}

	testReplicateThenRestore(t, s, replicaDir)
}

// testReplicateThenRestore seeds s.DBPath, replicates it to replicaDir,
// deletes it, and checks that RestoreIfNeeded brings the data back.
func testReplicateThenRestore(t *testing.T, s *Sidecar, replicaDir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	mustSQLite(t, s.DBPath, "CREATE TABLE test (id INTEGER PRIMARY KEY, val TEXT)")
	mustSQLite(t, s.DBPath, "INSERT INTO test (id, val) VALUES (1, 'hello'), (2, 'world')")

	// The DB already exists, so this must be a no-op.
	if err := s.RestoreIfNeeded(ctx); err != nil {
		t.Fatalf("RestoreIfNeeded: %v", err)
	}

	cmd, err := s.Replicate(ctx)
	if err != nil {
		t.Fatalf("Replicate: %v", err)
	}
	if err := waitForReplica(replicaDir, 10*time.Second); err != nil {
		_ = Shutdown(cmd, 5*time.Second)
		t.Fatalf("waitForReplica: %v", err)
	}
	if err := Shutdown(cmd, 5*time.Second); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if err := os.Remove(s.DBPath); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreIfNeeded(ctx); err != nil {
		t.Fatalf("RestoreIfNeeded (after delete): %v", err)
	}

	if got := mustSQLite(t, s.DBPath, "SELECT COUNT(*) FROM test"); got != "2" {
		t.Errorf("expected 2 rows after restore, got %q", got)
	}
	if got := mustSQLite(t, s.DBPath, "PRAGMA integrity_check"); got != "ok" {
		t.Errorf("integrity check: %s", got)
	}
}

func requireCLIs(t *testing.T, names ...string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s not found in PATH; skipping integration test", name)
		}
	}
}

func mustSQLite(t *testing.T, dbPath, query string) string {
	t.Helper()
	out, err := exec.Command("sqlite3", dbPath, query).CombinedOutput()
	if err != nil {
		t.Fatalf("sqlite3 %q: %v: %s", query, err, out)
	}
	return strings.TrimSpace(string(out))
}

// waitForReplica polls until litestream has written at least one LTX file
// under replicaDir.
func waitForReplica(replicaDir string, timeout time.Duration) error {
	deadline := time.After(timeout)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			return fmt.Errorf("timeout waiting for LTX files in %s", replicaDir)
		case <-tick.C:
			if found, _ := hasLTXFile(replicaDir); found {
				return nil
			}
		}
	}
}

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
