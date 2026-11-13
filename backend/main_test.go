package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupRejectsUnsupportedConfigurationBeforeOpeningDatabase(t *testing.T) {
	if os.Getenv("UNIFINSG_STARTUP_GUARD_TEST") == "1" {
		main()
		return
	}
	for _, test := range []struct {
		name, config, proxies, message string
	}{
		{"async", `{"async":{"enabled":true}}`, "", "UniFinSG does not support the inherited async framework; set async.enabled=false"},
		{"trusted_proxy", `{}`, "invalid-network", "configure trusted proxies:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(test.config), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("UNIFINSG_STARTUP_GUARD_TEST", "1")
			t.Setenv("UNICHATGO_CONFIG", path)
			t.Setenv("UNICHATGO_DB", "invalid-driver-must-never-open")
			t.Setenv("TRUSTED_PROXY_CIDRS", test.proxies)
			command := exec.Command(os.Args[0], "-test.run=^TestStartupRejectsUnsupportedConfigurationBeforeOpeningDatabase$")
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), test.message) || strings.Contains(string(output), "dbType:") {
				t.Fatalf("expected early configuration rejection: err=%v output=%s", err, output)
			}
		})
	}
}
