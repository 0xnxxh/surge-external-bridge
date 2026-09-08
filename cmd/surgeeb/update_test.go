package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ssfun/surge-external-bridge/internal/update"
)

func TestCLIStatusTracksDelegatedInstallation(t *testing.T) {
	dir := t.TempDir()
	lock, err := update.LockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer update.UnlockInstance(lock)
	if err := update.RegisterInstance(dir, os.Getpid(), "review-instance"); err != nil {
		t.Fatal(err)
	}
	var live atomic.Value
	live.Store(update.Status{CurrentVersion: "1.0.0", Phase: "downloading"})
	var statusReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_ = json.NewEncoder(w).Encode(update.Health{PID: os.Getpid(), InstanceID: "review-instance"})
		case "/api/update/check":
			_ = json.NewEncoder(w).Encode(update.Status{Latest: &update.Release{Tag: "v1.1.0"}, CanInstall: true})
		case "/api/update/install":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"accepted","version":"v1.1.0"}`))
		case "/api/update":
			if r.Method != http.MethodGet || r.Header.Get("X-SurgeEB-Instance") != "review-instance" || r.Header.Get("Authorization") != "Bearer test-token" {
				t.Error("status request must be an authenticated, instance-bound GET")
			}
			statusReads.Add(1)
			_ = json.NewEncoder(w).Encode(live.Load().(update.Status))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	config, _ := json.Marshal(map[string]string{"http_bind": strings.TrimPrefix(server.URL, "http://"), "policy_host": "127.0.0.1", "management_token": "test-token"})
	if err := os.WriteFile(filepath.Join(dir, "gateway.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	invoke := func(command string) []byte {
		t.Helper()
		data, err := captureUpdateCommand(t, command, dir)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	accepted := invoke("install")
	if !strings.Contains(string(accepted), `"accepted"`) {
		t.Fatalf("installation not accepted: %s", accepted)
	}
	for _, want := range []update.Status{
		{CurrentVersion: "1.0.0", Phase: "downloading"},
		{CurrentVersion: "1.0.0", Phase: "idle", CheckError: "Release 大小或 SHA256 校验失败"},
	} {
		live.Store(want)
		output := invoke("status")
		var got update.Status
		if err := json.Unmarshal(output, &got); err != nil {
			t.Fatal(err)
		}
		if got.Phase != want.Phase || got.CheckError != want.CheckError || got.CurrentVersion != want.CurrentVersion {
			t.Errorf("after accepted: live phase=%q error=%q; CLI phase=%q error=%q (live status requests=%d)", want.Phase, want.CheckError, got.Phase, got.CheckError, statusReads.Load())
		}
	}
	if statusReads.Load() != 2 {
		t.Fatalf("live status requests = %d", statusReads.Load())
	}
}

func captureUpdateCommand(t *testing.T, command, dir string) ([]byte, error) {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "cli-output-")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	original := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = original }()
	commandErr := updateCommand([]string{command, "--data-dir", dir})
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	return data, commandErr
}

func TestCLIStatusFallsBackOnlyWhenInstanceStopped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "update-transaction.json"), []byte(`{"phase":"succeeded","version":"v1.1.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := captureUpdateCommand(t, "status", dir)
	if err != nil {
		t.Fatal(err)
	}
	var status update.Status
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatal(err)
	}
	if status.Phase != "succeeded" || status.TargetVersion != "v1.1.0" {
		t.Fatalf("offline status = %+v", status)
	}
	instance, err := update.LockInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer update.UnlockInstance(instance)
	// A live process without verifiable identity must not expose the previous
	// successful transaction as the result of its current operation.
	data, err = captureUpdateCommand(t, "status", dir)
	if err == nil || len(data) != 0 {
		t.Fatalf("unidentified live instance fell back to stale success: data=%s err=%v", data, err)
	}
}
