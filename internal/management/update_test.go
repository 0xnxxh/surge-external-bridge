package management

import (
	"bytes"
	"github.com/ssfun/surge-external-bridge/internal/update"
	"net/http"
	"os"
	"path/filepath"
	"testing"
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
