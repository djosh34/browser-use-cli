//go:build integration

package cdp_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djosh34/browser-use-cli/cdp"
)

func cliBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "browser-use-cli")
	if output, err := exec.Command("go", "build", "-race", "-o", binary, "..").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	return binary
}
func runCLI(t *testing.T, binary, endpoint string, want int, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(testContext(t), binary, args...)
	cmd.Env = append(os.Environ(), "BROWSER_CDP_URL="+endpoint, "GORACE=atexit_sleep_ms=0")
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
	if code != want {
		t.Fatalf("CLI exit %d want %d: stdout=%s stderr=%s", code, want, stdout.String(), stderr.String())
	}
	if code == 0 {
		if stderr.Len() != 0 {
			t.Fatalf("success wrote diagnostics: %s", stderr.String())
		}
		return stdout.String()
	}
	if stdout.Len() != 0 {
		t.Fatalf("failure wrote stdout: %s", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("failure had no diagnostic")
	}
	return stderr.String()
}

func TestCLIChromeCommandsAndCopiedReferences(t *testing.T) {
	binary := cliBinary(t)
	fixture := actionFixture(t)
	endpoint := chrome(t, "about:blank")
	var pages cdp.PagesResult
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "--json", "pages")), &pages); err != nil || len(pages.Pages) != 1 {
		t.Fatalf("pages: %+v %v", pages, err)
	}
	var page cdp.PageInfo
	// An explicit global flag overrides an unusable environment endpoint.
	if err := json.Unmarshal([]byte(runCLI(t, binary, "ftp://invalid", 0, "--endpoint", endpoint, "open", "--json", fixture.URL)), &page); err != nil || page.ID != pages.Pages[0].ID {
		t.Fatalf("sole-tab open: %+v %v", page, err)
	}
	var read cdp.ReadResult
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "read", "--page", string(page.ID), "--selector", "body", "--json")), &read); err != nil || !strings.Contains(read.String(), "Hit") {
		t.Fatalf("read: %s %v", read, err)
	}
	var controls cdp.ControlsResult
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "controls", "--json")), &controls); err != nil {
		t.Fatal(err)
	}
	ref := func(name string) string {
		t.Helper()
		for _, control := range controls.Controls {
			if control.Name == name {
				return string(control.Target)
			}
		}
		t.Fatalf("missing CLI control %q: %s", name, controls)
		return ""
	}
	hit, text, choice, navigate := ref("Hit"), ref("Text"), ref("Choice"), ref("Navigate")
	if output := runCLI(t, binary, endpoint, 0, "controls", "--verbose"); !strings.Contains(output, hit) {
		t.Fatal("text controls did not expose the same copyable reference")
	}
	var action cdp.ActionResult
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "fill", "--json", "--", text, "--nieuw 日本語")), &action); err != nil || action.Page.ID != page.ID {
		t.Fatalf("fill: %s %v", action, err)
	}
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "controls", "--json")), &controls); err != nil {
		t.Fatal(err)
	}
	filled := false
	for _, control := range controls.Controls {
		if control.Name == "Text" && control.State.Value != nil && *control.State.Value == "--nieuw 日本語" {
			filled = true
		}
	}
	if !filled {
		t.Fatal("CLI arguments did not preserve replacement text")
	}
	runCLI(t, binary, endpoint, 0, "press", "--json", "--page", string(page.ID), "Control+A")
	runCLI(t, binary, endpoint, 0, "press", "--json", "Backspace")
	runCLI(t, binary, endpoint, 0, "select", "--json", choice, "Beta")
	var evaluated cdp.EvalResult
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "eval", "--json", `({text:document.querySelector('#text').value,choice:document.querySelector('select').value})`)), &evaluated); err != nil {
		t.Fatal(err)
	}
	var values struct{ Text, Choice string }
	if json.Unmarshal(evaluated.Value, &values) != nil || values.Text != "" || values.Choice != "b" {
		t.Fatalf("native key/select values: %s", evaluated)
	}
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "click", "--json", hit)), &action); err != nil || action.Page.Title != "Clicked" {
		t.Fatalf("click: %s %v", action, err)
	}
	runCLI(t, binary, endpoint, 0, "eval", `document.body.insertAdjacentHTML('beforeend','<div id="overlay" style="position:fixed;inset:0;z-index:999;background:white"></div>')`)
	if output := runCLI(t, binary, endpoint, 1, "click", "--json", hit); !strings.Contains(output, `"code":"blocked"`) {
		t.Fatalf("overlay: %s", output)
	}
	runCLI(t, binary, endpoint, 0, "eval", `document.querySelector('#overlay').remove();{const old=document.querySelector('#hit');old.replaceWith(old.cloneNode(true))}`)
	if output := runCLI(t, binary, endpoint, 1, "click", "--json", hit); !strings.Contains(output, `"code":"stale"`) {
		t.Fatalf("cloned node: %s", output)
	}
	runCLI(t, binary, endpoint, 0, "click", "--json", navigate)
	if output := runCLI(t, binary, endpoint, 1, "fill", "--json", text, "must not retarget"); !strings.Contains(output, `"code":"stale"`) {
		t.Fatalf("old document: %s", output)
	}
	runCLI(t, binary, endpoint, 0, "open", "--page", string(page.ID), fixture.URL)
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "controls", "--json")), &controls); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "click", "--json", ref("Popup"))), &action); err != nil || len(action.NewPages) != 1 {
		t.Fatalf("popup: %s %v", action, err)
	}
	if output := runCLI(t, binary, endpoint, 2, "read", "--json"); !strings.Contains(output, `"code":"ambiguous"`) {
		t.Fatalf("multiple tabs: %s", output)
	}
	var created cdp.PageInfo
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "open", "--json", fixture.URL)), &created); err != nil || created.ID == page.ID || created.ID == action.NewPages[0].ID {
		t.Fatalf("multi-tab open: %s %v", created, err)
	}
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "pages", "--json")), &pages); err != nil || len(pages.Pages) != 3 {
		t.Fatalf("CLI closed external tabs: %+v %v", pages, err)
	}
}
