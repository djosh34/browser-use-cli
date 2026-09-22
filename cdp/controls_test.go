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
	"time"

	"github.com/djosh34/browser-use-cli/cdp"
)

func controlsFixture(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><title>Controls fixture</title><article><h2>Alpha story</h2><a href="/alpha">Read more</a></article><article><h2>Beta story</h2><a href="/beta">Read more</a></article><form><label>Search <input id="search" value="initial"></label><p>Unlabelled preference <input type="checkbox" checked></p><label>Password <input type="password" value="private-password"></label><label>Units <select><option value="metric">Metric</option><option value="imperial" selected>Imperial</option><option disabled>Disabled option</option></select></label><input aria-label="Readonly" readonly value="fixed"><button disabled>Disabled button</button><button aria-disabled="true">ARIA disabled</button><div inert><button>Inert button</button></div><button hidden>Hidden button</button><button style="opacity:0">Transparent button</button><div style="margin-top:2000px"><button>Offscreen button</button></div></form><div onclick="this.dataset.used='yes'" style="cursor:pointer"><span>Custom choice</span></div><ul onclick="this.dataset.used=event.target.textContent"><li style="cursor:pointer">Delegated Amsterdam</li><li style="cursor:pointer">Delegated Utrecht</li></ul><p style="cursor:pointer">Decorative pointer</p><div tabindex="0">Focus-only container</div><div role="textbox" contenteditable aria-label="Editor">Editable text</div><input type="checkbox" id="mixed" aria-label="Mixed"><button aria-expanded="true"><span onclick="this.dataset.used='yes'">Expander</span></button><div role="listbox" aria-label="Choices"><div role="option" aria-selected="true">Selected item</div></div><div style="height:0;overflow:hidden"><button>Collapsed button</button></div><script>document.querySelector('#mixed').indeterminate=true;document.addEventListener('click',()=>{});addEventListener('scroll',()=>document.title='SCROLLED');</script>`)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestChromeControlsRefreshAfterFrameDocumentNavigation(t *testing.T) {
	advance := make(chan struct{})
	ready := make(chan struct{}, 1)
	var fixture *httptest.Server
	fixture = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/":
			fmt.Fprintf(w, `<iframe src="%s/child"></iframe>`, strings.Replace(fixture.URL, "127.0.0.1", "localhost", 1))
		case "/child":
			fmt.Fprint(w, `<button>Original</button><script>fetch('/advance').then(()=>location.href='/changed')</script>`)
		case "/advance":
			select {
			case <-advance:
			case <-r.Context().Done():
			}
		case "/changed":
			fmt.Fprint(w, `<button>Replacement</button><script>addEventListener('load',()=>fetch('/ready'))</script>`)
		case "/ready":
			ready <- struct{}{}
		}
	}))
	defer fixture.Close()
	defer func() {
		select {
		case <-advance:
		default:
			close(advance)
		}
	}()
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	before, err := p.Controls(testContext(t), cdp.ControlsOptions{})
	if err != nil || len(before.Controls) != 1 {
		t.Fatalf("before frame navigation: %s %v", before, err)
	}
	close(advance)
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("frame navigation did not load")
	}
	after, err := p.Controls(testContext(t), cdp.ControlsOptions{})
	if err != nil || len(after.Controls) != 1 || len(after.Warnings) != 0 {
		t.Fatalf("after frame navigation: %s %v", after, err)
	}
	if before.Page.ID != after.Page.ID || after.Controls[0].Name != "Replacement" || after.Controls[0].Target == before.Controls[0].Target || !strings.HasSuffix(after.Controls[0].Frame.URL, "/changed") {
		t.Fatalf("document identity not refreshed: %s", after)
	}
}

func TestChromeControlsResolveHTMLBase(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<base href="/catalog/"><a href="item">Item</a>`)
	}))
	defer fixture.Close()
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Controls(testContext(t), cdp.ControlsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Controls) != 1 || result.Controls[0].Href != fixture.URL+"/catalog/item" {
		t.Fatalf("base-relative href: %s", result)
	}
}

func TestChromeControlsCaptureLargeNativeSelectWithoutShadowDuplicates(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<label>Place <select>`)
		for i := 0; i < 500; i++ {
			selected := ""
			if i == 250 {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="place-%d"%s>Place 日本語 %d</option>`, i, selected, i)
		}
		fmt.Fprint(w, `</select></label>`)
	}))
	defer fixture.Close()
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Controls(testContext(t), cdp.ControlsOptions{Verbose: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Controls) != 1 {
		t.Fatalf("native shadow duplicates: %s", result)
	}
	control := result.Controls[0]
	if len(control.Options) != 500 || control.Options[499].Label != "Place 日本語 499" || control.State.Value == nil || *control.State.Value != "place-250" || control.Context != "Place" {
		t.Fatalf("large select capture: %+v", control)
	}
	if !strings.Contains(result.String(), "place-499") {
		t.Fatal("String silently truncated options")
	}
}

func TestChromeControlsKeepContextFromLongLocalParagraphs(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<article><p>A long report about education. `+strings.Repeat("More report details. ", 30)+`<a href="/education">Read more</a></p></article><article><p>A long report about weather. `+strings.Repeat("More report details. ", 30)+`<a href="/weather">Read more</a></p></article><p>`+strings.Repeat("Shared boilerplate. ", 30)+`Near-end transport topic <a href="/transport">Read more</a></p><p>`+strings.Repeat("Shared boilerplate. ", 30)+`Near-end housing topic <a href="/housing">Read more</a></p>`)
	}))
	defer fixture.Close()
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Controls(testContext(t), cdp.ControlsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Controls) != 4 {
		t.Fatalf("controls: %s", result)
	}
	for i, topic := range []string{"education", "weather", "Near-end transport", "Near-end housing"} {
		control := result.Controls[i]
		if control.Name != "Read more" || !strings.Contains(control.Context, topic) || len([]rune(control.Context)) > 240 {
			t.Errorf("local context: %+v", control)
		}
	}
}

func TestChromeVerboseControlsUseLocalInteractionEvidence(t *testing.T) {
	fixture := controlsFixture(t)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	normal, err := p.Controls(testContext(t), cdp.ControlsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	verbose, err := p.Controls(testContext(t), cdp.ControlsOptions{Verbose: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Custom choice", "Delegated Amsterdam", "Delegated Utrecht"} {
		if strings.Contains(normal.String(), text) {
			t.Errorf("extra target in normal controls: %s", normal)
		}
		found := false
		for _, control := range verbose.Controls {
			if control.Source == "dom" && strings.Contains(control.Context, text) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing custom target %q: %s", text, verbose)
		}
	}
	for _, text := range []string{"Decorative pointer", "Focus-only container"} {
		if strings.Contains(verbose.String(), text) {
			t.Errorf("false target %q: %s", text, verbose)
		}
	}
	if len(verbose.Controls) != len(normal.Controls)+3 {
		t.Errorf("extra duplicate/container targets: %s", verbose)
	}
	info, err := p.Info(testContext(t))
	if err != nil || info.Title != "Controls fixture" {
		t.Fatalf("observation scrolled: %+v %v", info, err)
	}
}

func TestChromeObservationExcludesFullyClippedContent(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<div style="height:1px;overflow:hidden"><p style="margin-top:20px">Clipped text<button>Clipped button</button></p></div><div style="height:20px;overflow:auto"><button style="margin-top:100px">Scrollable button</button></div>`)
	}))
	defer fixture.Close()
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	read, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read.String(), "Clipped") || !strings.Contains(read.String(), "Scrollable button") {
		t.Fatalf("clipped read: %s", read)
	}
	controls, err := p.Controls(testContext(t), cdp.ControlsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(controls.Controls) != 1 || controls.Controls[0].Name != "Scrollable button" {
		t.Fatalf("clipped controls: %s", controls)
	}
}

func TestChromeObservationRespectsFrameVisibility(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/" {
			fmt.Fprint(w, `<iframe hidden src="/hidden"></iframe><iframe style="margin-top:2000px" src="/offscreen"></iframe>`)
		} else if r.URL.Path == "/hidden" {
			fmt.Fprint(w, `<p>Hidden frame text</p><button>Hidden frame button</button>`)
		} else {
			fmt.Fprint(w, `<p>Offscreen frame text</p><button>Offscreen frame button</button>`)
		}
	}))
	defer fixture.Close()
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	read, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read.String(), "Hidden frame text") || !strings.Contains(read.String(), "Offscreen frame text") || len(read.Warnings) != 0 {
		t.Fatalf("frame text visibility: %s", read)
	}
	controls, err := p.Controls(testContext(t), cdp.ControlsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(controls.Controls) != 1 || controls.Controls[0].Name != "Offscreen frame button" || !controls.Controls[0].Offscreen || len(controls.Warnings) != 0 {
		t.Fatalf("frame control visibility: %s", controls)
	}
}

func TestChromeControlContextOmitsSVGStyles(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="/details">Route details<svg><style>.icon { fill: red }</style></svg></a>`)
	}))
	t.Cleanup(fixture.Close)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Controls(testContext(t), cdp.ControlsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Controls) != 1 || result.Controls[0].Context != "Route details" {
		t.Fatalf("non-rendered SVG stylesheet in context: %s", result)
	}
}

func TestChromeVerboseKeepsPointerWrapperForKeyboardOnlyChild(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<div style="display:inline-block;padding:8px" onclick="document.title='Calendar opened'"><label style="pointer-events:none"><input type="button" aria-label="Open calendar" value="Open calendar"></label></div><div onclick="document.title='Ordinary'"><button>Ordinary button</button></div>`)
	}))
	t.Cleanup(fixture.Close)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	ref := targetNamed(t, p, "Open calendar")
	_, err = p.Click(testContext(t), ref)
	var typed *cdp.Error
	if !errors.As(err, &typed) || typed.Code != "blocked" {
		t.Fatalf("keyboard-only child must not bypass hit testing: %v", err)
	}
	result, err := p.Controls(testContext(t), cdp.ControlsOptions{Verbose: true})
	if err != nil {
		t.Fatal(err)
	}
	var wrapper cdp.ControlRef
	extra := 0
	for _, control := range result.Controls {
		if control.Source == "dom" {
			extra++
			if control.Context == "Open calendar" {
				wrapper = control.Target
			}
		}
	}
	if extra != 1 || wrapper == "" {
		t.Fatalf("missing useful wrapper or duplicate ordinary container: %s", result)
	}
	if action, err := p.Click(testContext(t), wrapper); err != nil || action.Page.Title != "Calendar opened" {
		t.Fatalf("explicit wrapper ref: %s %v", action, err)
	}
}

func TestChromeNativeFieldsUseNearbyLineContext(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<table><tr><td><input type="CHECKBOX" value="t">Temperature<br><input type="CHECKBOX" value="td">Dewpoint<br><input type="CHECKBOX" value="wind">Surface Wind<select><option>mph</option><option>km/h</option></select></td><td>Transport Wind<select><option>mph</option><option>km/h</option></select></td></tr></table>`)
	}))
	t.Cleanup(fixture.Close)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Controls(testContext(t), cdp.ControlsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Temperature", "Dewpoint", "Surface Wind", "Surface Wind", "Transport Wind"}
	if len(result.Controls) != len(want) {
		t.Fatalf("controls: %s", result)
	}
	for i, control := range result.Controls {
		if control.Name != "" || control.Context != want[i] {
			t.Fatalf("field %d: semantic name %q context %q, want unnamed with %q", i, control.Name, control.Context, want[i])
		}
	}
	read, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read.String(), "tTemperature") || strings.Contains(read.String(), "tdDewpoint") || strings.Contains(read.String(), "windSurface") {
		t.Fatalf("hidden checkbox submission values leaked into visible text: %s", read)
	}
}

func TestChromeControlsTraverseFramesAndShadows(t *testing.T) {
	fixture := observationFixture(t)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Controls(testContext(t), cdp.ControlsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 0 || len(result.Controls) != 9 {
		t.Fatalf("frame/shadow controls: %s", result)
	}
	frames := map[string]bool{}
	names := map[string]bool{}
	for _, control := range result.Controls {
		frames[control.Frame.ID] = true
		names[control.Name] = true
		id, err := control.Target.PageID()
		if err != nil || id != result.Page.ID {
			t.Fatalf("nested reference routing: %s %v", id, err)
		}
	}
	if len(frames) != 4 {
		t.Fatalf("source frames: %v", frames)
	}
	for _, name := range []string{"Root button", "Same origin button", "Cross origin button", "Nested inner button", "Open shadow button", "Closed shadow button"} {
		if !names[name] {
			t.Errorf("missing %q", name)
		}
	}
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
	if control := names["Editor"]; control.State.Value == nil || *control.State.Value != "Editable text" {
		t.Errorf("contenteditable value: %+v", control)
	}
	if control := names["Mixed"]; control.State.Checked != "mixed" {
		t.Errorf("mixed checkbox: %+v", control)
	}
	if control := names["Expander"]; control.State.Expanded == nil || !*control.State.Expanded {
		t.Errorf("expanded state: %+v", control)
	}
	if control := names["Selected item"]; control.State.Selected == nil || !*control.State.Selected {
		t.Errorf("selected state: %+v", control)
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
	for _, name := range []string{"Disabled button", "ARIA disabled", "Inert button", "Hidden button", "Transparent button", "Collapsed button"} {
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
