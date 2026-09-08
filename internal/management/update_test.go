package management

import (
	"bytes"
	"github.com/ssfun/surge-external-bridge/internal/update"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateAPIAuthenticationOriginAndConfirmation(t *testing.T) {
	app, server, url := testManagementServer(t, "management-token-1234567890")
	defer app.Close()
	manager, err := update.NewManager(app.DataDir(), "1.0.0", update.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	server.SetUpdater(manager, "test-instance")
	for _, tc := range []struct {
		method, path, token, origin, body string
		want                              int
	}{
		{"GET", "/api/update", "", "", "", 401},
		{"POST", "/api/update/check", "management-token-1234567890", "https://evil.test", "", 403},
		{"POST", "/api/update/install", "management-token-1234567890", url, `{"version":"v2.0.0"}`, 412},
		{"PUT", "/api/update/settings", "management-token-1234567890", url, `{"auto_check":true,"auto_install":true,"install_hour":4}`, 412},
		{"PUT", "/api/update/settings", "management-token-1234567890", url, `{"auto_check":false,"auto_install":false,"install_hour":4}`, 200},
	} {
		req, _ := http.NewRequest(tc.method, url+tc.path, bytes.NewBufferString(tc.body))
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("%s %s: %d want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
	data, err := os.ReadFile(filepath.Join(app.DataDir(), "update-settings.json"))
	if err != nil || !bytes.Contains(data, []byte(`"auto_check": false`)) {
		t.Fatalf("preferences not persisted: %s %v", data, err)
	}
}
func TestUpdateFreezesMutationsButKeepsStatusReadable(t *testing.T) {
	app, _, url := testManagementServer(t, "")
	defer app.Close()
	if err := os.WriteFile(filepath.Join(app.DataDir(), "update-transaction.json"), []byte(`{"phase":"starting"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		want         int
	}{{"POST", "/api/providers", 503}, {"DELETE", "/api/service", 503}, {"GET", "/health", 200}, {"GET", "/api/overview", 200}} {
		req, _ := http.NewRequest(tc.method, url+tc.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("%s %s = %d", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestUpdateRejectsStaleCLIInstanceIdentity(t *testing.T) {
	app, server, address := testManagementServer(t, "")
	defer app.Close()
	manager, err := update.NewManager(app.DataDir(), "1.0.0", update.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	server.SetUpdater(manager, "replacement-instance")
	for _, path := range []string{"/api/update", "/api/update/check", "/api/update/install"} {
		method := http.MethodPost
		if path == "/api/update" {
			method = http.MethodGet
		}
		req, _ := http.NewRequest(method, address+path, bytes.NewBufferString(`{"version":"v1.1.0"}`))
		req.Header.Set("X-SurgeEB-Instance", "previous-instance")
		req.Header.Set("X-SurgeEB-Confirm", "install-update")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s accepted stale CLI identity: %d", path, resp.StatusCode)
		}
	}
}

func TestUpdatePreparationDrainsAdmittedManagementWrites(t *testing.T) {
	app, server, _ := testManagementServer(t, "")
	defer app.Close()
	manager, err := update.NewManager(app.DataDir(), "1.0.0", update.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	server.SetUpdater(manager, "live-instance")
	entered, release, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	handler := server.updateGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	}))
	go func() {
		defer close(completed)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PUT", "/api/settings", nil))
	}()
	<-entered
	prepared := make(chan error, 1)
	go func() {
		prepared <- manager.Freeze(func() error {
			return os.WriteFile(filepath.Join(app.DataDir(), "update-transaction.json"), []byte(`{"phase":"prepared"}`), 0o600)
		})
	}()
	select {
	case err := <-prepared:
		close(release)
		<-completed
		t.Fatalf("prepared before existing write completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-completed
	if err := <-prepared; err != nil {
		t.Fatal(err)
	}
	called := false
	recorder := httptest.NewRecorder()
	server.updateGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })).ServeHTTP(recorder, httptest.NewRequest("PUT", "/api/settings", nil))
	if called || recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("write admitted after preparation: called=%v status=%d", called, recorder.Code)
	}
}
