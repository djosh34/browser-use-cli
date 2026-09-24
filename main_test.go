package main_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIUsageAndMetadata(t *testing.T) {
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "browser-use-cli")
	install := exec.Command("go", "install", ".")
	install.Env = append(os.Environ(), "GOBIN="+binDir)
	if output, err := install.CombinedOutput(); err != nil {
		t.Fatalf("install CLI: %v\n%s", err, output)
	}
	for _, tc := range []struct {
		name string
		args []string
		code int
		json bool
	}{
		{"help", []string{"--help"}, 0, false},
		{"command help", []string{"read", "--help"}, 0, false},
		{"version", []string{"--version"}, 0, false},
		{"missing command", nil, 2, false},
		{"unknown command", []string{"unknown"}, 2, false},
		{"removed controls", []string{"controls", "--json"}, 2, false},
		{"removed verbose", []string{"read", "--json", "--verbose"}, 2, true},
		{"zero page", []string{"--endpoint", "http://127.0.0.1:1", "read", "--json", "--page", "0"}, 2, true},
		{"negative page", []string{"--endpoint", "http://127.0.0.1:1", "read", "--json", "--page=-1"}, 2, true},
		{"non-numeric page", []string{"--endpoint", "http://127.0.0.1:1", "read", "--json", "--page", "SECRET-value"}, 2, true},
		{"zero control", []string{"click", "--json", "0"}, 2, true},
		{"opaque control", []string{"click", "--json", "SECRET-value"}, 2, true},
		{"missing endpoint", []string{"--json", "pages"}, 2, true},
		{"missing argument", []string{"fill", "--json"}, 2, true},
		{"invalid duration", []string{"--json", "--timeout", "SECRET-value", "pages"}, 2, true},
		{"unknown flag", []string{"--json", "pages", "--unknown"}, 2, true},
		{"invalid endpoint", []string{"--json", "--endpoint", "ftp://user:SECRET-value@example.org/private?token=SECRET-value", "pages"}, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(binary, tc.args...)
			cmd.Env = append(os.Environ(), "BROWSER_CDP_URL=")
			cmd.Stdin = strings.NewReader("pages\n")
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if code != tc.code {
				t.Fatalf("exit %d want %d; stdout=%s stderr=%s", code, tc.code, stdout.String(), stderr.String())
			}
			if strings.Contains(stdout.String()+stderr.String(), "SECRET-value") {
				t.Fatal("diagnostic exposed argument/endpoint data")
			}
			if code == 0 {
				if stdout.Len() == 0 || stderr.Len() != 0 {
					t.Fatalf("metadata streams: %q %q", stdout.String(), stderr.String())
				}
				return
			}
			if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("error streams: %q %q", stdout.String(), stderr.String())
			}
			if tc.json {
				var result struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}
				if json.Unmarshal(stderr.Bytes(), &result) != nil || result.Code != "invalid_input" || result.Message == "" {
					t.Fatalf("JSON diagnostic: %s", stderr.String())
				}
			}
		})
	}
}
