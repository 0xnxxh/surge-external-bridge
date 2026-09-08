package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestInstallRunningDelegatesToMatchingInstance(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching", true: "different-instance"}[mismatch], func(t *testing.T) {
			dir := t.TempDir()
			instance, err := LockInstance(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer UnlockInstance(instance)
			if err := RegisterInstance(dir, os.Getpid(), "live-instance"); err != nil {
				t.Fatal(err)
			}
			var checks, installs atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					id := "live-instance"
					if mismatch {
						id = "other-instance"
					}
					_ = json.NewEncoder(w).Encode(Health{PID: os.Getpid(), InstanceID: id})
					return
				}
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-management-token" || r.Header.Get("X-SurgeEB-Instance") != "live-instance" {
					t.Errorf("missing authenticated instance-bound request: %s %s", r.Method, r.URL.Path)
				}
				switch r.URL.Path {
				case "/api/update/check":
					checks.Add(1)
					_ = json.NewEncoder(w).Encode(Status{Latest: &Release{Tag: "v1.1.0"}, CanInstall: true})
				case "/api/update/install":
					installs.Add(1)
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["version"] != "v1.1.0" || r.Header.Get("X-SurgeEB-Confirm") != "install-update" {
						t.Error("missing installation confirmation/version")
					}
					w.WriteHeader(http.StatusAccepted)
					_, _ = w.Write([]byte(`{"status":"accepted","version":"v1.1.0"}`))
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
				}
			}))
			defer server.Close()
			if err := writeJSON(filepath.Join(dir, "gateway.json"), map[string]string{"http_bind": strings.TrimPrefix(server.URL, "http://"), "policy_host": "127.0.0.1", "management_token": "test-management-token"}); err != nil {
				t.Fatal(err)
			}
			result, running, err := InstallRunning(context.Background(), dir)
			if !running {
				t.Fatal("live instance fell back to offline installation")
			}
			if mismatch {
				if err == nil || checks.Load() != 0 || installs.Load() != 0 {
					t.Fatalf("mismatch: checks=%d installs=%d err=%v", checks.Load(), installs.Load(), err)
				}
				return
			}
			if err != nil || checks.Load() != 1 || installs.Load() != 1 || !strings.Contains(string(result), `"accepted"`) {
				t.Fatalf("checks=%d installs=%d result=%s err=%v", checks.Load(), installs.Load(), result, err)
			}
			if _, err := ReadJournal(dir); !os.IsNotExist(err) {
				t.Fatal("CLI created its own transaction instead of delegating")
			}
		})
	}
}

func TestInstallRunningOnlyFallsBackWhenInstanceIsStopped(t *testing.T) {
	dir := t.TempDir()
	if err := RegisterInstance(dir, os.Getpid(), "stale-instance"); err != nil {
		t.Fatal(err)
	}
	if _, running, err := InstallRunning(context.Background(), dir); err != nil || running {
		t.Fatalf("stopped instance: running=%v err=%v", running, err)
	}
	instance, err := LockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer UnlockInstance(instance)
	if err := os.Remove(filepath.Join(dir, "update-instance.json")); err != nil {
		t.Fatal(err)
	}
	if _, running, err := InstallRunning(context.Background(), dir); err == nil || !running {
		t.Fatalf("unidentified live instance: running=%v err=%v", running, err)
	}
}

func TestOfflineInstallRejectsInstanceStartedAfterCLIDiscovery(t *testing.T) {
	dir := t.TempDir()
	if _, running, err := InstallRunning(context.Background(), dir); err != nil || running {
		t.Fatal("expected offline CLI discovery")
	}
	// A process starts while the CLI is checking releases. Its management writes
	// must not overlap a CLI-prepared transaction without the server's Freeze.
	instance, err := LockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer UnlockInstance(instance)
	manager, err := NewManager(dir, "1.0.0", Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.install(context.Background(), Release{Tag: "v1.1.0"}); !errors.Is(err, errLocked) {
		t.Fatalf("offline install did not reject running instance: %v", err)
	}
	if _, err := ReadJournal(dir); !os.IsNotExist(err) {
		t.Fatal("offline CLI prepared a transaction while management writes were possible")
	}
}
