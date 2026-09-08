package service

import (
	"path/filepath"
	"testing"
)

func TestResolveUpdateServiceDefinition(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		for _, uid := range []int{501, 0} {
			if goos == "darwin" && uid == 0 {
				continue
			}
			exe := filepath.Join(t.TempDir(), "Application Support", "SurgeEB")
			dir := filepath.Join(t.TempDir(), "data with % and spaces")
			content, err := renderForContext(goos, exe, dir, uid)
			if err != nil {
				t.Fatal(err)
			}
			got, matched, err := updateDefinition(goos, uid, content, dir)
			if err != nil || !matched || got != exe {
				t.Fatalf("%s/%d: %q %v %v", goos, uid, got, matched, err)
			}
			_, matched, err = updateDefinition(goos, uid, content, dir+"-other")
			if err != nil || matched {
				t.Fatalf("unrelated service selected: %v %v", matched, err)
			}
		}
	}
}

func TestUpdateServiceRejectsAmbiguousLaunchArguments(t *testing.T) {
	for _, content := range []string{
		`<plist><dict><key>ProgramArguments</key><array><string>/tmp/SurgeEB</string><string>serve</string></array></dict></plist>`,
		`<plist><dict><key>ProgramArguments</key><array><string>relative</string><string>serve</string><string>--data-dir</string><string>/tmp/data</string></array></dict></plist>`,
	} {
		if _, _, err := updateDefinition("darwin", 501, []byte(content), "/tmp/data"); err == nil {
			t.Fatal("accepted ambiguous service")
		}
	}
}

func TestLoadedLaunchAgentMustMatchInstalledInstance(t *testing.T) {
	output := []byte("gui/501/test = {\n\tprogram = /Users/test/Application Support/SurgeEB/bin/SurgeEB\n\targuments = {\n\t\t/Users/test/Application Support/SurgeEB/bin/SurgeEB\n\t\tserve\n\t\t--data-dir\n\t\t/Users/test/data\n\t}\n}")
	if err := validateLoadedLaunchAgent(output, "/Users/test/Application Support/SurgeEB/bin/SurgeEB", "/Users/test/data"); err != nil {
		t.Fatal(err)
	}
	if err := validateLoadedLaunchAgent(output, "/Users/test/Application Support/SurgeEB/bin/SurgeEB", "/Users/test/other"); err == nil {
		t.Fatal("accepted different loaded data directory")
	}
}
