package update

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStableReleaseSelection(t *testing.T) {
	releases := []Release{{Tag: "v2.0.0-rc.1"}, {Tag: "v1.12.0", Prerelease: true}, {Tag: "v1.11.0", Draft: true}, {Tag: "v1.10.0"}, {Tag: "v1.9.0"}}
	got, err := selectRelease(releases, "1.9.0")
	if err != nil || got == nil || got.Tag != "v1.10.0" {
		t.Fatalf("got %+v, %v", got, err)
	}
	for _, current := range []string{"1.10.0", "2.0.0"} {
		got, err = selectRelease(releases, current)
		if err != nil || got != nil {
			t.Fatalf("downgrade offered: %+v %v", got, err)
		}
	}
	if _, err = selectRelease(releases, "0.2.0-dev"); err == nil {
		t.Fatal("development build must not auto-update")
	}
}

func TestDownloadChecksChecksumBeforePublication(t *testing.T) {
	payload := []byte("release bytes")
	sum := fmt.Sprintf("%x", sha256.Sum256(payload))
	broken := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "SHA256SUMS") {
			fmt.Fprintf(w, "%s  SurgeEB-linux-arm64\n", sum)
			return
		}
		if broken {
			_, _ = w.Write([]byte("corrupt"))
		} else {
			_, _ = w.Write(payload)
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), allowTestHTTP: true}
	release := Release{Tag: "v1.2.0", Assets: []Asset{{Name: "SurgeEB-linux-arm64", URL: srv.URL + "/bin", Size: int64(len(payload))}, {Name: "SHA256SUMS", URL: srv.URL + "/SHA256SUMS"}}}
	target := filepath.Join(t.TempDir(), "SurgeEB")
	path, _, err := c.Download(context.Background(), release, "linux", "arm64", target)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(payload) {
		t.Fatal("wrong download")
	}
	_ = os.Remove(path)
	broken = true
	if _, _, err = c.Download(context.Background(), release, "linux", "arm64", target); err == nil {
		t.Fatal("accepted corrupt file")
	}
	entries, _ := os.ReadDir(filepath.Dir(target))
	if len(entries) != 0 {
		t.Fatalf("failed download leaked files: %v", entries)
	}
}

func TestChecksumRequiresUniqueExactAsset(t *testing.T) {
	sum := strings.Repeat("a", 64)
	for _, text := range []string{"", sum + "  other", sum + "  bin\n" + sum + "  bin", "invalid  bin"} {
		if _, err := checksum([]byte(text), "bin"); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
}

func TestAssetSourceRestrictions(t *testing.T) {
	for _, url := range []string{"http://github.com/ssfun/surge-external-bridge/releases/download/v1/a", "https://evil.test/a", "https://github.com/another/project/releases/download/v1/a"} {
		if validAssetURL(url) {
			t.Fatalf("accepted %s", url)
		}
	}
	if !validAssetURL("https://github.com/ssfun/surge-external-bridge/releases/download/v1.0.0/SurgeEB-linux-amd64") {
		t.Fatal("official asset refused")
	}
}

func TestBinaryValidationMatchesExecutableVersionAndArchitecture(t *testing.T) {
	path := os.Getenv("SURGEEB_UPDATE_TEST_NEW")
	if path == "" {
		t.Skip("requires built candidate")
	}
	if err := ValidateBinary(path, "v1.0.1", runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBinary(path, "v9.0.0", runtime.GOOS, runtime.GOARCH); err == nil {
		t.Fatal("accepted wrong version")
	}
	if err := ValidateBinary(path, "v1.0.1", "windows", runtime.GOARCH); err == nil {
		t.Fatal("accepted wrong platform")
	}
}
