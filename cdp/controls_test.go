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

func controlsFixture(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Controls fixture</title><article><h2>Alpha story</h2><a href="/alpha">Read more</a></article><article><h2>Beta story</h2><a href="/beta">Read more</a></article><form><label>Search <input id="search" value="initial"></label><p>Unlabelled preference <input type="checkbox" checked></p><label>Password <input type="password" value="private-password"></label><label>Units <select><option value="metric">Metric</option><option value="imperial" selected>Imperial</option><option disabled>Disabled option</option></select></label><input aria-label="Readonly" readonly value="fixed"><button disabled>Disabled button</button><button aria-disabled="true">ARIA disabled</button><div inert><button>Inert button</button></div><button hidden>Hidden button</button><button style="opacity:0">Transparent button</button><div style="margin-top:2000px"><button>Offscreen button</button></div></form>`)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestChromeControlsCaptureSemanticStateAndContext(t *testing.T) {
	fixture := controlsFixture(t)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Controls(testContext(t), cdp.ControlsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings: %v", result.Warnings)
	}
	links := []cdp.Control{}
	names := map[string]cdp.Control{}
	for _, control := range result.Controls {
		id, err := control.Target.PageID()
		if err != nil || id != result.Page.ID {
			t.Errorf("reference page: %s %v", id, err)
		}
		if control.Source != "semantic" {
			t.Errorf("normal discovery source: %+v", control)
		}
		names[control.Name] = control
		if control.Role == "link" {
			links = append(links, control)
		}
	}
	if len(links) != 2 || links[0].Name != "Read more" || links[1].Name != "Read more" || !strings.Contains(links[0].Context, "Alpha story") || !strings.Contains(links[1].Context, "Beta story") || links[0].Href != fixture.URL+"/alpha" {
		t.Fatalf("repeated links: %+v", links)
	}
	if control, ok := names["Search"]; !ok || control.State.Value == nil || *control.State.Value != "initial" {
		t.Fatalf("input state: %+v; controls: %s", control, result)
	}
	if control, ok := names["Units"]; !ok || len(control.Options) != 3 || control.Options[0].Value != "metric" || !control.Options[2].Disabled || len(control.State.SelectedOptions) != 1 || control.State.SelectedOptions[0] != "imperial" {
		t.Fatalf("native options: %+v", control)
	}
	if control, ok := names["Readonly"]; !ok || !control.State.Readonly {
		t.Fatalf("readonly field: %+v", control)
	}
	if control, ok := names["Offscreen button"]; !ok || !control.Offscreen {
		t.Fatalf("offscreen: %+v", control)
	}
	foundUnnamed := false
	for _, control := range result.Controls {
		if control.Role == "checkbox" && control.Name == "" && strings.Contains(control.Context, "Unlabelled preference") && control.State.Checked == "true" {
			foundUnnamed = true
		}
	}
	if !foundUnnamed {
		t.Fatal("unnamed native checkbox was omitted or its semantic name was fabricated")
	}
	for _, name := range []string{"Disabled button", "ARIA disabled", "Inert button", "Hidden button", "Transparent button"} {
		if _, ok := names[name]; ok {
			t.Errorf("included unavailable control %q", name)
		}
	}
	c.Close()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-password") || strings.Contains(result.String(), "private-password") {
		t.Fatal("password value leaked")
	}
	var copied cdp.ControlsResult
	if err := json.Unmarshal(data, &copied); err != nil {
		t.Fatal(err)
	}
	if copied.String() != result.String() {
		t.Fatal("String and JSON differ after disconnect")
	}
}
