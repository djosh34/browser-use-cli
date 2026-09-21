//go:build integration

package cdp_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

func TestCLICancellationSignalsAndBrokenPipe(t *testing.T) {
	binary := cliBinary(t)
	started, release := make(chan struct{}), make(chan struct{})
	var startedOnce, releaseOnce sync.Once
	var navigations atomic.Int64
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<a href="/pending">Pending navigation</a>`)
		case "/pending":
			navigations.Add(1)
			fmt.Fprint(w, `<title>Pending</title><h1>Still alive</h1><img src="/slow">`)
		case "/slow":
			startedOnce.Do(func() { close(started) })
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	}))
	t.Cleanup(fixture.Close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	endpoint := chrome(t, "about:blank")
	runCLI(t, binary, endpoint, 0, "open", fixture.URL)
	var controls cdp.ControlsResult
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "controls", "--json")), &controls); err != nil || len(controls.Controls) != 1 {
		t.Fatalf("navigation fixture: %s %v", controls, err)
	}
	ctx := testContext(t)
	cmd := exec.CommandContext(ctx, binary, "click", "--json", string(controls.Controls[0].Target))
	cmd.Env = append(os.Environ(), "BROWSER_CDP_URL="+endpoint, "GORACE=atexit_sleep_ms=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	t.Cleanup(func() {
		if !waited {
			cmd.Process.Kill()
			<-done
		}
	})
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("CLI input did not start navigation")
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err := <-done
	waited = true
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 130 || stdout.Len() != 0 || !strings.Contains(stderr.String(), `"code":"interrupted"`) {
		t.Fatalf("SIGINT: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if navigations.Load() != 1 {
		t.Fatal("canceled input was replayed")
	}
	releaseOnce.Do(func() { close(release) })
	c := browserClient(t, endpoint)
	p, err := c.Page(testContext(t), controls.Page.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Eval(testContext(t), `new Promise(resolve=>{if(document.readyState==='complete')resolve(true);else addEventListener('load',()=>resolve(true),{once:true})})`); err != nil {
		t.Fatal(err)
	}
	if output := runCLI(t, binary, endpoint, 0, "read"); !strings.Contains(output, "Still alive") {
		t.Fatalf("SIGINT closed the browser or page: %s", output)
	}
	if output := runCLI(t, binary, endpoint, 1, "eval", "--json", "--timeout", "100ms", `new Promise(()=>{})`); !strings.Contains(output, `"code":"timeout"`) {
		t.Fatalf("deadline: %s", output)
	}
	runCLI(t, binary, endpoint, 0, "pages")

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	defer writer.Close()
	pipeCmd := exec.CommandContext(testContext(t), binary, "pages", "--json")
	pipeCmd.Env = append(os.Environ(), "BROWSER_CDP_URL="+endpoint, "GORACE=atexit_sleep_ms=0")
	pipeCmd.Stdout = writer
	stderr.Reset()
	pipeCmd.Stderr = &stderr
	err = pipeCmd.Run()
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), `"code":"output"`) {
		t.Fatalf("broken pipe: %v %s", err, stderr.String())
	}
	runCLI(t, binary, endpoint, 0, "pages")
}

func TestCLIFrameAndShadowReferencesAcrossProcesses(t *testing.T) {
	binary := cliBinary(t)
	fixture := observationFixture(t)
	endpoint := chrome(t, "about:blank")
	runCLI(t, binary, endpoint, 0, "open", fixture.URL)
	var controls cdp.ControlsResult
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "controls", "--json")), &controls); err != nil {
		t.Fatal(err)
	}
	refs := map[string]string{}
	for _, control := range controls.Controls {
		refs[control.Name] = string(control.Target)
	}
	for _, name := range []string{"Nested inner button", "Closed shadow button", "Inner text", "Inner navigation"} {
		if refs[name] == "" {
			t.Fatalf("missing nested CLI ref %q", name)
		}
	}
	runCLI(t, binary, endpoint, 0, "click", refs["Nested inner button"])
	runCLI(t, binary, endpoint, 0, "fill", refs["Inner text"], "station")
	runCLI(t, binary, endpoint, 0, "press", "--page", string(controls.Page.ID), "Control+A")
	runCLI(t, binary, endpoint, 0, "press", "X")
	runCLI(t, binary, endpoint, 0, "click", refs["Closed shadow button"])
	text := runCLI(t, binary, endpoint, 0, "read")
	if !strings.Contains(text, "Nested inner button!") || !strings.Contains(text, "Closed shadow button!") {
		t.Fatalf("native nested targets not reached: %s", text)
	}
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "controls", "--json")), &controls); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, control := range controls.Controls {
		if control.Name == "Inner text" && control.State.Value != nil && *control.State.Value == "X" {
			found = true
		}
	}
	if !found {
		t.Fatal("separate-process keyboard focus did not reach the inner field")
	}
	if output := runCLI(t, binary, "ftp://invalid", 2, "click", "--json", "--page", "another", refs["Inner navigation"]); !strings.Contains(output, "--page conflicts") {
		t.Fatalf("conflicting routing was not rejected locally: %s", output)
	}
	runCLI(t, binary, endpoint, 0, "click", refs["Inner navigation"])
	if output := runCLI(t, binary, endpoint, 1, "fill", "--json", refs["Inner text"], "stale"); !strings.Contains(output, `"code":"stale"`) {
		t.Fatalf("frame document stale ref: %s", output)
	}
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "controls", "--json")), &controls); err != nil {
		t.Fatal(err)
	}
	returned := ""
	for _, control := range controls.Controls {
		if control.Name == "Return inner" {
			returned = string(control.Target)
		}
	}
	if returned == "" {
		t.Fatal("new frame document was not observed")
	}
	runCLI(t, binary, endpoint, 0, "click", returned)
	if output := runCLI(t, binary, endpoint, 0, "read"); !strings.Contains(output, "Nested inner heading") {
		t.Fatalf("reverse frame transition: %s", output)
	}
	other := chrome(t, "about:blank")
	if output := runCLI(t, binary, other, 1, "click", "--json", refs["Nested inner button"]); !strings.Contains(output, `"code":"page"`) {
		t.Fatalf("wrong browser: %s", output)
	}
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
