package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestChecksNeverInstallAndPreferencesPersist(t *testing.T) {
	dir := t.TempDir()
	manager, err := NewManager(dir, "1.0.0", Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]Release{{Tag: "v1.1.0"}})
	}))
	defer server.Close()
	manager.client.HTTP = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		request, _ := http.NewRequest(req.Method, server.URL, nil)
		return server.Client().Do(request)
	})}
	if err = manager.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.latest == nil || manager.latest.Tag != "v1.1.0" {
		t.Fatal("update not discovered")
	}
	if _, err = ReadJournal(dir); !os.IsNotExist(err) {
		t.Fatalf("check created install transaction: %v", err)
	}
	if err = manager.Preferences(Preferences{AutoCheck: false, InstallHour: 12}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewManager(dir, "1.0.0", Runtime{})
	if err != nil || reloaded.prefs.AutoCheck || reloaded.prefs.AutoInstall || reloaded.prefs.InstallHour != 12 {
		t.Fatalf("preferences: %+v %v", reloaded, err)
	}
	info, _ := os.Stat(filepath.Join(dir, "update-settings.json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatal("preferences not private")
	}
	manager.prefs = Preferences{AutoCheck: true, AutoInstall: false, InstallHour: time.Now().Hour()}
	if err = manager.startInstall(context.Background(), "v1.1.0", false, true); err == nil {
		t.Fatal("unattended install without opt-in")
	}
	manager.prefs = Preferences{AutoCheck: true, AutoInstall: true, InstallHour: (time.Now().Hour() + 1) % 24}
	if err = manager.startInstall(context.Background(), "v1.1.0", false, true); err == nil {
		t.Fatal("unattended install outside window")
	}
}
