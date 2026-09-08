package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTransactionRestoresExecutableAndConfigAfterReadinessFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "SurgeEB")
	staged := filepath.Join(dir, "candidate")
	config := filepath.Join(dir, "gateway.json")
	for path, value := range map[string]string{target: "old binary", staged: "new binary", config: "old config"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	digest, _ := fileDigest(staged)
	j := Journal{ID: "test", Target: target, Staged: staged, Digest: digest, Version: "2.0.0", PreviousVersion: "1.0.0", Service: true, DataDir: dir}
	starts := 0
	stops := 0
	ops := transactionOps{
		stop: func() error { stops++; return nil },
		start: func() error {
			starts++
			if starts == 1 {
				return os.WriteFile(config, []byte("migrated config"), 0o600)
			}
			return nil
		},
		ready: func(version string) error {
			if version == "2.0.0" {
				return errors.New("core did not start")
			}
			return nil
		},
	}
	if err := runTransaction(&j, ops); err == nil {
		t.Fatal("failure hidden")
	}
	for path, want := range map[string]string{target: "old binary", config: "old config"} {
		got, _ := os.ReadFile(path)
		if string(got) != want {
			t.Fatalf("%s = %s", path, got)
		}
	}
	if j.Phase != "rolled_back" || starts != 2 || stops != 2 {
		t.Fatalf("%+v starts=%d stops=%d", j, starts, stops)
	}
}

func TestTransactionDoesNotStopOnCorruptStagedFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "SurgeEB")
	staged := filepath.Join(dir, "candidate")
	_ = os.WriteFile(target, []byte("old"), 0o755)
	_ = os.WriteFile(staged, []byte("tampered"), 0o755)
	j := Journal{ID: "test", DataDir: dir, Target: target, Staged: staged, Digest: "wrong"}
	stopped := false
	err := runTransaction(&j, transactionOps{stop: func() error { stopped = true; return nil }})
	if err == nil || stopped {
		t.Fatalf("err=%v stopped=%v", err, stopped)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "old" {
		t.Fatal("target changed")
	}
}

func TestInstanceLockRejectsSecondProcessAndReleases(t *testing.T) {
	dir := t.TempDir()
	a, err := LockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := LockInstance(dir); err == nil {
		UnlockInstance(b)
		t.Fatal("second instance acquired lock")
	}
	UnlockInstance(a)
	b, err := LockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	UnlockInstance(b)
}

func TestUpdateRefusesExecutableUsedByAnotherInstance(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "SurgeEB")
	staged := filepath.Join(dir, "candidate")
	_ = os.WriteFile(target, []byte("old"), 0o755)
	_ = os.WriteFile(staged, []byte("new"), 0o755)
	other, err := LockExecutable(target, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { UnlockInstance(other) }()
	digest, _ := fileDigest(staged)
	j := Journal{ID: "test", DataDir: dir, Target: target, Staged: staged, Digest: digest}
	if err = runTransaction(&j, transactionOps{stop: func() error { return nil }}); err == nil {
		t.Fatal("updated a shared running binary")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "old" {
		t.Fatal("target changed")
	}
}

func TestInterruptedManualUpdateCanRecoverWithoutService(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "SurgeEB")
	backup := filepath.Join(dir, "update-backup")
	_ = os.MkdirAll(backup, 0o700)
	for path, value := range map[string]string{target: "new", target + ".previous": "old", filepath.Join(dir, "gateway.json"): "new config", filepath.Join(backup, "gateway.json"): "old config"} {
		_ = os.WriteFile(path, []byte(value), 0o600)
	}
	digest, _ := fileDigest(target)
	oldDigest, _ := fileDigest(target + ".previous")
	j := Journal{ID: "manual", Phase: "installing", Target: target, Staged: filepath.Join(dir, "candidate"), DataDir: dir, Digest: digest, PreviousDigest: oldDigest}
	if err := writeJSON(j.path(), j); err != nil {
		t.Fatal(err)
	}
	if err := Worker(dir, true); err == nil {
		t.Fatal("recovery should report original interruption")
	}
	result, err := ReadJournal(dir)
	if err != nil || result.Phase != "rolled_back" {
		t.Fatalf("%+v %v", result, err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "old" {
		t.Fatal("not restored")
	}
}

func TestWorkerCannotExecuteAnotherTransaction(t *testing.T) {
	dir := t.TempDir()
	j := Journal{ID: "current", Phase: "prepared", DataDir: dir, Target: "/not-touched", Staged: "/not-touched"}
	if err := writeJSON(j.path(), j); err != nil {
		t.Fatal(err)
	}
	// An empty worker identifier must fail before inspecting/mutating any transaction.
	if err := WorkerFor(dir, ""); err == nil {
		t.Fatal("accepted unbound worker")
	}
	got, err := ReadJournal(dir)
	if err != nil || got.ID != j.ID || got.Phase != j.Phase {
		t.Fatalf("transaction changed: %+v %v", got, err)
	}
}

func TestRecoveryRespectsOtherInstanceExecutableLock(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "SurgeEB")
	backup := filepath.Join(dir, "update-backup")
	if err := os.MkdirAll(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{target: "new binary", target + ".previous": "old binary", filepath.Join(dir, "gateway.json"): "new config", filepath.Join(backup, "gateway.json"): "old config"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	digest, _ := fileDigest(target)
	previous, _ := fileDigest(target + ".previous")
	j := Journal{ID: "shared", Phase: "installing", DataDir: dir, Target: target, Staged: filepath.Join(dir, "candidate"), Digest: digest, PreviousDigest: previous}
	if err := writeJSON(j.path(), j); err != nil {
		t.Fatal(err)
	}
	other, err := LockExecutable(target, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { UnlockInstance(other) }()
	if err := Worker(dir, true); err == nil {
		t.Fatal("recovered over a shared executable")
	}
	got, _ := os.ReadFile(target)
	config, _ := os.ReadFile(filepath.Join(dir, "gateway.json"))
	journal, _ := ReadJournal(dir)
	if string(got) != "new binary" || string(config) != "new config" || journal.Phase != "recovery_failed" {
		t.Fatalf("modified another instance's binary: target=%s config=%s phase=%s", got, config, journal.Phase)
	}
	UnlockInstance(other)
	other = nil
	_ = Worker(dir, true)
	got, _ = os.ReadFile(target)
	journal, _ = ReadJournal(dir)
	if string(got) != "old binary" || journal.Phase != "rolled_back" {
		t.Fatalf("recovery after lock release: target=%s phase=%s", got, journal.Phase)
	}
}
