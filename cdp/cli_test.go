//go:build integration

package cdp_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/djosh34/browser-use-cli/cdp"
)

func cliBinary(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	install := exec.Command("go", "install", "-race", "..")
	install.Env = append(os.Environ(), "GOBIN="+binDir)
	if output, err := install.CombinedOutput(); err != nil {
		t.Fatalf("install CLI: %v\n%s", err, output)
	}
	return filepath.Join(binDir, "browser-use-cli")
}
func runCLI(t *testing.T, binary, endpoint string, want int, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(testContext(t), binary, args...)
	cmd.Env = append(os.Environ(), "BROWSER_CDP_URL="+endpoint, "GORACE=atexit_sleep_ms=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
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
		t.Fatalf("CLI %v exit %d want %d: stdout=%s stderr=%s", args, code, want, stdout.String(), stderr.String())
	}
	if code == 0 {
		if stderr.Len() != 0 {
			t.Fatalf("success wrote diagnostics: %s", stderr.String())
		}
		return stdout.String()
	}
	if stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("failure streams: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	return stderr.String()
}
func cliRead(t *testing.T, binary, endpoint string, flags ...string) cdp.ReadResult {
	t.Helper()
	args := append([]string{"read", "--json"}, flags...)
	var result cdp.ReadResult
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, args...)), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func cliControl(t *testing.T, result cdp.ReadResult, name string) string {
	t.Helper()
	var found cdp.ControlID
	var walk func(*cdp.Node)
	walk = func(n *cdp.Node) {
		if n == nil {
			return
		}
		if n.ID > 0 && n.Name == name {
			if found != 0 {
				t.Fatalf("ambiguous fixture control %q", name)
			}
			found = n.ID
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(result.Tree)
	if found == 0 {
		t.Fatalf("missing control %q: %s", name, result)
	}
	return strconv.Itoa(int(found))
}

func TestCLIInterruptsBlockedOutput(t *testing.T) {
	binary := cliBinary(t)
	endpoint := chrome(t, "about:blank")
	for _, merged := range []bool{false, true} {
		t.Run(fmt.Sprintf("merged_stderr=%t", merged), func(t *testing.T) {
			cmd := exec.CommandContext(testContext(t), binary, "eval", "--json", `'x'.repeat(4*1024*1024)`)
			cmd.Env = append(os.Environ(), "BROWSER_CDP_URL="+endpoint, "GORACE=atexit_sleep_ms=0")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			if merged {
				cmd.Stderr = cmd.Stdout
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cmd.Process.Kill(); cmd.Wait() }()
			if _, err := io.ReadFull(output, make([]byte, 1)); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 130 || (!merged && !strings.Contains(stderr.String(), `"code":"interrupted"`)) {
				t.Fatalf("SIGINT while stdout blocked: %v %s", err, stderr.String())
			}
		})
	}
	runCLI(t, binary, endpoint, 0, "pages")
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
	observation := cliRead(t, binary, endpoint)
	ctx := testContext(t)
	cmd := exec.CommandContext(ctx, binary, "click", "--json", cliControl(t, observation, "Pending navigation"))
	cmd.Env = append(os.Environ(), "BROWSER_CDP_URL="+endpoint, "GORACE=atexit_sleep_ms=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
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
		t.Fatal("canceled input replayed")
	}
	releaseOnce.Do(func() { close(release) })
	c := browserClient(t, endpoint)
	p, err := c.Page(testContext(t), observation.Page.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Eval(testContext(t), `new Promise(resolve=>{if(document.readyState==='complete')resolve(true);else addEventListener('load',()=>resolve(true),{once:true})})`); err != nil {
		t.Fatal(err)
	}
	if output := runCLI(t, binary, endpoint, 0, "read"); !strings.Contains(output, "Still alive") {
		t.Fatalf("SIGINT closed page: %s", output)
	}
	if output := runCLI(t, binary, endpoint, 1, "eval", "--json", "--timeout", "100ms", `new Promise(()=>{})`); !strings.Contains(output, `"code":"timeout"`) {
		t.Fatalf("deadline: %s", output)
	}
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

func TestCLIOrdinalsFiltersAndActionsAcrossProcesses(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/child" {
			fmt.Fprint(w, `<title>Child frame</title><label>Inner text<input></label><button onclick="this.textContent='Inner clicked'">Inner button</button>`)
			return
		}
		if r.URL.Path == "/next" {
			fmt.Fprint(w, `<title>Next page</title><h1>Arrived</h1>`)
			return
		}
		fmt.Fprint(w, `<title>Fixture</title><main><h1>Actions</h1><button id="first" onclick="this.remove()">Remove first</button><button onclick="document.title='Clicked'">Hit</button><label>Text<input id="text"></label><label>Choice<select><option value="a">Alpha</option><option value="b">Beta</option></select></label><a href="/next" target="_blank">Popup</a></main><div id="host"></div><iframe title="Embedded" src="/child"></iframe><script>host.attachShadow({mode:'closed'}).innerHTML='<button onclick="this.textContent=\'Shadow clicked\'">Shadow button</button>'</script>`)
	}))
	defer fixture.Close()
	binary := cliBinary(t)
	endpoint := chrome(t, "about:blank")
	var pages cdp.PagesResult
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "pages", "--json")), &pages); err != nil || len(pages.Pages) != 1 || pages.Pages[0].ID != 1 {
		t.Fatalf("numeric pages: %+v %v", pages, err)
	}
	runCLI(t, binary, "ftp://invalid", 0, "--endpoint", endpoint, "open", fixture.URL)
	full := cliRead(t, binary, endpoint)
	if !full.Complete {
		t.Fatalf("incomplete fixture: %s", full)
	}
	scoped := cliRead(t, binary, endpoint, "--selector", "main", "--controls-only")
	hit := cliControl(t, full, "Hit")
	if hit != cliControl(t, scoped, "Hit") {
		t.Fatal("filter changed control IDs")
	}
	if strings.Contains(scoped.String(), "Inner text") {
		t.Fatal("CSS scope included unrelated child frame")
	}
	runCLI(t, binary, endpoint, 0, "fill", "--json", cliControl(t, full, "Text"), "--nieuw 日本語")
	after := cliRead(t, binary, endpoint)
	if !strings.Contains(after.String(), "--nieuw 日本語") {
		t.Fatalf("filled value not visible: %s", after)
	}
	runCLI(t, binary, endpoint, 0, "press", "--page", "1", "Control+A")
	runCLI(t, binary, endpoint, 0, "press", "Backspace")
	runCLI(t, binary, endpoint, 0, "select", cliControl(t, full, "Choice"), "Beta")
	runCLI(t, binary, endpoint, 0, "fill", cliControl(t, full, "Inner text"), "station")
	runCLI(t, binary, endpoint, 0, "click", cliControl(t, full, "Inner button"))
	runCLI(t, binary, endpoint, 0, "click", cliControl(t, full, "Shadow button"))
	after = cliRead(t, binary, endpoint)
	for _, text := range []string{"Inner clicked", "Shadow clicked", "station", "Beta"} {
		if !strings.Contains(after.String(), text) {
			t.Fatalf("missing outcome %q: %s", text, after)
		}
	}
	// Removing the first control changes ordinals. A new CLI process binds the
	// newly enumerated first control, not an old persistent reference.
	runCLI(t, binary, endpoint, 0, "click", cliControl(t, after, "Remove first"))
	after = cliRead(t, binary, endpoint)
	if cliControl(t, after, "Hit") != "1" || hit == "1" {
		t.Fatalf("ordinal did not change: %s", after)
	}
	output := runCLI(t, binary, endpoint, 0, "click", "--json", "1")
	var action cdp.ActionResult
	if err := json.Unmarshal([]byte(output), &action); err != nil || action.Page.Title != "Clicked" || !strings.Contains(output, `"action":"click"`) {
		t.Fatalf("fresh ordinal input: %s %v", output, err)
	}
	if strings.Contains(output, "tree") || strings.Contains(output, "station") {
		t.Fatal("action returned an observation or fill text")
	}
	runCLI(t, binary, endpoint, 2, "controls")
	runCLI(t, binary, endpoint, 2, "read", "--verbose")
	runCLI(t, binary, endpoint, 1, "read", "--page", "999")
	output = runCLI(t, binary, endpoint, 0, "click", "--json", cliControl(t, after, "Popup"))
	if err := json.Unmarshal([]byte(output), &action); err != nil || len(action.NewPages) != 1 {
		t.Fatalf("popup: %s %v", output, err)
	}
	if output := runCLI(t, binary, endpoint, 2, "read", "--json"); !strings.Contains(output, `"code":"ambiguous"`) {
		t.Fatalf("multiple tabs: %s", output)
	}
	runCLI(t, binary, endpoint, 0, "open", fixture.URL)
	if err := json.Unmarshal([]byte(runCLI(t, binary, endpoint, 0, "pages", "--json")), &pages); err != nil || len(pages.Pages) != 3 {
		t.Fatalf("Open with multiple tabs: %+v %v", pages, err)
	}
	for i, page := range pages.Pages {
		if page.ID != cdp.PageID(i+1) {
			t.Fatalf("non-contiguous ordinals: %+v", pages)
		}
	}
}
