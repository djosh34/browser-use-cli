//go:build integration

package cdp_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/djosh34/browser-use-cli/cdp"
)

func observationFixture(t *testing.T) *httptest.Server {
	t.Helper()
	var s *httptest.Server
	s = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		cross := strings.Replace(s.URL, "127.0.0.1", "localhost", 1)
		switch r.URL.Path {
		case "/":
			fmt.Fprintf(w, `<!doctype html><title>Frames and shadows</title><h1>Root heading</h1><div class="region">Root region <span class="region">nested region</span></div><div>Outside selector</div><div id="open"></div><div id="closed"></div><script>document.querySelector('#open').attachShadow({mode:'open'}).innerHTML='<p class="region">Open shadow text</p>';document.querySelector('#closed').attachShadow({mode:'closed'}).innerHTML='<p class="region">Closed shadow text</p>';</script><iframe src="%s/same"></iframe>`, s.URL)
		case "/same":
			fmt.Fprintf(w, `<h2>Same origin heading</h2><p class="region">Same origin region</p><iframe src="%s/cross"></iframe>`, cross)
		case "/cross":
			fmt.Fprintf(w, `<h2>Cross origin heading</h2><p class="region">Cross origin region</p><iframe src="%s/inner"></iframe>`, s.URL)
		case "/inner":
			fmt.Fprint(w, `<h2>Nested inner heading</h2><p class="region">Nested inner region</p>`)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestChromeReadTraversesFramesAndShadowContent(t *testing.T) {
	fixture := observationFixture(t)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("supported frames warned: %v", result.Warnings)
	}
	for _, text := range []string{"Root heading", "Same origin heading", "Cross origin heading", "Nested inner heading", "Open shadow text", "Closed shadow text"} {
		if !strings.Contains(result.String(), text) {
			t.Errorf("missing %q in %s", text, result)
		}
	}
	if len(result.Sections) != 4 {
		t.Errorf("source documents = %d, want 4", len(result.Sections))
	}
}

func TestChromeReadSelectorScopesEveryDocumentAndShadow(t *testing.T) {
	fixture := observationFixture(t)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Read(testContext(t), cdp.ReadOptions{Selector: ".region"})
	if err != nil {
		t.Fatal(err)
	}
	text := result.String()
	for _, want := range []string{"Root region nested region", "Same origin region", "Cross origin region", "Nested inner region", "Open shadow text", "Closed shadow text"} {
		if strings.Count(text, want) != 1 {
			t.Errorf("selector capture count for %q in %s", want, text)
		}
	}
	if strings.Contains(text, "heading") || strings.Contains(text, "Outside selector") || len(result.Warnings) != 0 {
		t.Fatalf("selector escaped scope: %s", text)
	}
	for _, selector := range []string{"[", ".does-not-exist"} {
		_, err := p.Read(testContext(t), cdp.ReadOptions{Selector: selector})
		var e *cdp.Error
		if !errors.As(err, &e) || e.Code != "invalid_input" {
			t.Errorf("selector %q error: %v", selector, err)
		}
	}
}

func TestChromeReadCapturesRenderedStructuredText(t *testing.T) {
	large := strings.Repeat("Long Unicode 日本語 paragraph. ", 10000)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Read fixture</title><h1>Heading</h1><main><p>Hello <b>world</b>.</p><ul><li>First</li><li>Second</li></ul><table><tr><th>Name</th><th>Value</th></tr><tr><td>Alpha</td><td>42</td></tr></table><input value="visible input"><input type="password" value="private-password"><div hidden>hidden-text</div><div style="visibility:hidden">invisible-text</div><div style="opacity:0">transparent-text</div><div style="margin-top:2000px">Offscreen text</div><p>`+large+`</p></main>`)
	}))
	defer fixture.Close()
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if len(result.Sections) != 1 || len(result.Warnings) != 0 {
		t.Fatalf("sections/warnings: %+v", result)
	}
	text := result.Sections[0].Text
	for _, want := range []string{"# Heading", "Hello world.", "* First", "* Second", "Alpha", "42", "visible input", "Offscreen text", strings.TrimSpace(large)} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing rendered content %q", want[:min(60, len(want))])
		}
	}
	for _, excluded := range []string{"private-password", "hidden-text", "invisible-text", "transparent-text"} {
		if strings.Contains(text, excluded) {
			t.Errorf("included %s", excluded)
		}
	}
	if result.String() != result.String() {
		t.Fatal("captured rendering changed")
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var copied cdp.ReadResult
	if err := json.Unmarshal(data, &copied); err != nil {
		t.Fatal(err)
	}
	if copied.String() != result.String() {
		t.Fatal("text and JSON represent different captured data")
	}
}
