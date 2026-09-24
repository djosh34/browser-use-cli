//go:build integration

package cdp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/djosh34/browser-use-cli/cdp"
)

// Explicit integration runs fail if Chrome is not configured. Each launch uses
// a fresh profile, port 0, and the normal sandbox. Only this owned process is stopped.
func chrome(t *testing.T, initial ...string) string {
	t.Helper()
	binary := os.Getenv("CHROME_BIN")
	if binary == "" {
		t.Fatal("integration requires CHROME_BIN pointing to Chrome for Testing")
	}
	for attempt := 1; attempt <= 3; attempt++ {
		profile, err := os.MkdirTemp("", "browser-use-chrome-*")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for attempt := 1; attempt <= 3; attempt++ {
				if err := os.RemoveAll(profile); err == nil {
					return
				} else if attempt == 3 {
					t.Errorf("remove disposable Chrome profile: %v", err)
				}
				time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
			}
		})
		logPath := filepath.Join(profile, "startup.log")
		log, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		args := []string{"--headless=new", "--window-size=1440,1000", "--ozone-override-screen-size=1440,1000", "--force-device-scale-factor=1", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--user-data-dir=" + profile, "--no-first-run", "--no-default-browser-check"}
		if len(initial) == 0 {
			args = append(args, "--no-startup-window")
		} else {
			args = append(args, initial...)
		}
		cmd := exec.Command(binary, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Stdout = log
		cmd.Stderr = log
		if err := cmd.Start(); err != nil {
			log.Close()
			if attempt == 3 {
				t.Fatalf("start Chrome: %v", err)
			}
			time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
			continue
		}
		done := make(chan error, 1)
		go func() { err := cmd.Wait(); log.Close(); done <- err }()
		stop := func() {
			select {
			case <-done:
				return
			default:
			}
			syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				<-done
			}
		}
		exited := false
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			data, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if err == nil && len(lines) == 2 {
				endpoint := "http://127.0.0.1:" + lines[0]
				t.Cleanup(stop)
				return endpoint
			}
			select {
			case err := <-done:
				exited = true
				data, _ := os.ReadFile(logPath)
				if attempt == 3 {
					t.Fatalf("Chrome exited: %v\n%s", err, data)
				}
				deadline = time.Time{}
			default:
				time.Sleep(20 * time.Millisecond)
			}
		}
		// If Wait's result was consumed above the process has already exited.
		if !exited {
			stop()
		}
		if attempt == 3 {
			data, _ := os.ReadFile(logPath)
			t.Fatalf("Chrome startup timed out\n%s", data)
		}
		time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
	}
	t.Fatal("Chrome startup failed")
	return ""
}
func browserClient(t *testing.T, endpoint string) *cdp.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := cdp.Connect(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestChromePageIDsAreNumericOrdinals(t *testing.T) {
	c := browserClient(t, chrome(t, "about:blank"))
	pages, err := c.Pages(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(pages)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Pages []struct {
			ID int `json:"id"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(data, &got); err != nil || len(got.Pages) != 1 || got.Pages[0].ID != 1 {
		t.Fatalf("expected sole page ordinal 1: %s (%v)", data, err)
	}
}

func TestChromePageHandlesStayBoundWhenOrdinalsChange(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Tabs</title><button onclick="window.open('/popup')">Open tab</button>`)
	}))
	defer fixture.Close()
	endpoint := chrome(t, "about:blank")
	c := browserClient(t, endpoint)
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := p.Click(testContext(t), targetNamed(t, p, "Open tab")); err != nil {
			t.Fatal(err)
		}
	}
	pages, err := c.Pages(testContext(t))
	if err != nil || len(pages.Pages) != 3 {
		t.Fatalf("fixture tabs: %+v %v", pages, err)
	}
	last, err := c.Page(testContext(t), 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := last.Eval(testContext(t), `document.title='Bound last tab';void 0`); err != nil {
		t.Fatal(err)
	}
	original, err := p.Info(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	closeID := cdp.PageID(1)
	if original.ID == closeID {
		closeID = 2
	}
	first, err := c.Page(testContext(t), closeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Eval(testContext(t), `setTimeout(()=>window.close(),0);void 0`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		pages, err = c.Pages(testContext(t))
		if err != nil {
			t.Fatal(err)
		}
		if len(pages.Pages) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture tab did not close")
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, err := last.Info(testContext(t))
	if err != nil || info.ID != 2 || info.Title != "Bound last tab" {
		t.Fatalf("handle retargeted: %+v %v", info, err)
	}
	if _, err := first.Info(testContext(t)); err == nil {
		t.Fatal("closed handle retargeted a replacement ordinal")
	}
	c.Close()
	c = browserClient(t, endpoint)
	fresh, err := c.Page(testContext(t), 2)
	if err != nil {
		t.Fatal(err)
	}
	info, err = fresh.Info(testContext(t))
	if err != nil || info.Title != "Bound last tab" {
		t.Fatalf("fresh ordinal 2: %+v %v", info, err)
	}
	if _, err := c.Page(testContext(t), 3); err == nil {
		t.Fatal("unavailable ordinal fell back")
	}
	var typed *cdp.Error
	if _, err := c.Page(testContext(t), -1); !errors.As(err, &typed) || typed.Code != "invalid_input" {
		t.Fatalf("negative page: %v", err)
	}
}

func TestChromeOpenSelectionAndDisconnect(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<title>Loaded 日本語</title><h1>Hello</h1>")
	}))
	defer fixture.Close()
	for _, initial := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("%d initial tabs", initial), func(t *testing.T) {
			var args []string
			if initial > 0 {
				args = append(args, "about:blank")
			}
			endpoint := chrome(t, args...)
			if initial == 2 {
				// Arrange the second tab through Chrome's external HTTP endpoint.
				// Headless Chrome accepts only one startup URL.
				req, err := http.NewRequestWithContext(testContext(t), http.MethodPut, endpoint+"/json/new?about:blank", nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Fatalf("create fixture tab: HTTP %d", resp.StatusCode)
				}
			}
			c := browserClient(t, endpoint)
			before, err := c.Pages(testContext(t))
			if err != nil {
				t.Fatal(err)
			}
			if len(before.Pages) != initial {
				t.Fatalf("initial pages = %d, want %d", len(before.Pages), initial)
			}
			if initial != 1 {
				if _, err := c.Page(testContext(t), 0); err == nil {
					t.Fatal("ambiguous observation succeeded")
				}
			}
			p, err := c.Open(testContext(t), fixture.URL)
			if err != nil {
				t.Fatal(err)
			}
			info, err := p.Info(testContext(t))
			if err != nil || info.Title != "Loaded 日本語" || info.URL != fixture.URL+"/" {
				t.Fatalf("loaded info: %+v %v", info, err)
			}
			after, err := c.Pages(testContext(t))
			if err != nil {
				t.Fatal(err)
			}
			want := initial + 1
			if initial == 1 {
				want = 1
				if info.ID != before.Pages[0].ID {
					t.Fatal("sole tab was not reused")
				}
			}
			if len(after.Pages) != want {
				t.Fatalf("pages = %d, want %d", len(after.Pages), want)
			}
			if _, err := c.Page(testContext(t), 999); err == nil {
				t.Fatal("invalid explicit page accepted")
			}
			c.Close()
			other := browserClient(t, endpoint)
			alive, err := other.Pages(testContext(t))
			if err != nil || len(alive.Pages) != want {
				t.Fatalf("Close changed browser tabs: %+v %v", alive, err)
			}
		})
	}
}

func TestChromeNavigationWaitsForRequestedLoad(t *testing.T) {
	requested := make(chan struct{}, 1)
	release := make(chan struct{})
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/document", http.StatusFound)
		case "/document":
			fmt.Fprint(w, `<title>Before</title><script src="/slow.js"></script><h1>Complete</h1>`)
		case "/slow.js":
			requested <- struct{}{}
			<-release
			fmt.Fprint(w, `document.title="After load"`)
		}
	}))
	defer fixture.Close()
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Page(testContext(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- p.Navigate(testContext(t), fixture.URL+"/redirect") }()
	select {
	case <-requested:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("script not requested")
	}
	select {
	case err := <-result:
		close(release)
		t.Fatalf("returned before load: %v", err)
	default:
	}
	otherHandle, err := c.Page(testContext(t), 0)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	blockedCtx, cancel := context.WithTimeout(testContext(t), 20*time.Millisecond)
	_, blockedErr := otherHandle.Info(blockedCtx)
	cancel()
	if !errors.Is(blockedErr, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("same-page operation was not serialized: %v", blockedErr)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	info, err := p.Info(testContext(t))
	if err != nil || info.Title != "After load" || info.URL != fixture.URL+"/document" {
		t.Fatalf("redirect load: %+v %v", info, err)
	}
	if err := p.Navigate(testContext(t), fixture.URL+"/document#section"); err != nil {
		t.Fatalf("same-document navigation: %v", err)
	}
	info, err = p.Info(testContext(t))
	if err != nil || !strings.HasSuffix(info.URL, "#section") {
		t.Fatalf("hash: %+v %v", info, err)
	}
}

func TestChromeNavigationFailures(t *testing.T) {
	release := make(chan struct{})
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dialog":
			fmt.Fprint(w, `<script>alert("Do not accept me")</script>`)
		case "/hang":
			<-release
		default:
			fmt.Fprint(w, "<title>Ready</title>")
		}
	}))
	defer fixture.Close()
	defer close(release)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Page(testContext(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(testContext(t), 100*time.Millisecond)
	err = p.Navigate(ctx, fixture.URL+"/hang")
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("navigation deadline: %v", err)
	}
	if err := p.Navigate(testContext(t), fixture.URL+"/ready"); err != nil {
		t.Fatalf("navigate after deadline: %v", err)
	}
	err = p.Navigate(testContext(t), "http://127.0.0.1:1/")
	var e *cdp.Error
	if !errors.As(err, &e) || e.Code != "navigation" {
		t.Fatalf("navigation error: %v", err)
	}
	err = p.Navigate(testContext(t), fixture.URL+"/dialog")
	if !errors.As(err, &e) || e.Code != "dialog" {
		t.Fatalf("blocking dialog: %v", err)
	}
	// A dialog must not block connection shutdown or an unrelated browser call.
	if _, err := c.Pages(testContext(t)); err != nil {
		t.Fatalf("dialog stranded browser call: %v", err)
	}
}
