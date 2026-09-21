//go:build integration

package cdp_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/djosh34/browser-use-cli/cdp"
)

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
