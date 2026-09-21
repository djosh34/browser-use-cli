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

func actionFixture(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path != "/" {
			fmt.Fprint(w, `<title>Destination</title><h1>Destination loaded</h1>`)
			return
		}
		fmt.Fprint(w, `<!doctype html><title>Actions</title><button id="hit" onclick="document.title='Clicked';this.dataset.count=Number(this.dataset.count||0)+1">Hit</button><label>Text <input id="text" value="old"></label><label>Notes <textarea>old notes</textarea></label><div contenteditable aria-label="Editor">old editor</div><label>Choice <select id="choice"><option value="a">Alpha</option><option value="b">Beta</option><option value="d" disabled>Disabled</option><option value="x">Duplicate</option><option value="y">Duplicate</option></select></label><input aria-label="Readonly" readonly value="fixed"><a href="/destination">Navigate</a><a href="/popup" target="_blank">Popup</a><button onclick="alert('blocked')">Dialog</button>`)
	}))
	t.Cleanup(s.Close)
	return s
}
func targetNamed(t *testing.T, p *cdp.Page, name string) cdp.ControlRef {
	t.Helper()
	result, err := p.Controls(testContext(t), cdp.ControlsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, control := range result.Controls {
		if control.Name == name {
			return control.Target
		}
	}
	t.Fatalf("control %q missing: %s", name, result)
	return ""
}
func evalValue(t *testing.T, p *cdp.Page, expression string) string {
	t.Helper()
	result, err := p.Eval(testContext(t), expression)
	if err != nil {
		t.Fatal(err)
	}
	var s string
	if json.Unmarshal(result.Value, &s) != nil {
		t.Fatalf("expected JS string: %s", result)
	}
	return s
}
func TestChromeFillUsesNativeReplacement(t *testing.T) {
	fixture := actionFixture(t)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Eval(testContext(t), `document.addEventListener('input',e=>{document.body.dataset.trusted=String(e.isTrusted)});void 0`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, text, read string }{
		{"Text", "Nieuw 日本語 😀", `document.querySelector('#text').value`},
		{"Notes", "line one\nline two", `document.querySelector('textarea').value`},
		{"Editor", "edited body", `document.querySelector('[contenteditable]').textContent`},
		{"Text", "", `document.querySelector('#text').value`},
	} {
		if _, err := p.Fill(testContext(t), targetNamed(t, p, tc.name), tc.text); err != nil {
			t.Fatalf("fill %s: %v", tc.name, err)
		}
		if got := evalValue(t, p, tc.read); got != tc.text {
			t.Fatalf("fill %s got %q want %q", tc.name, got, tc.text)
		}
		if evalValue(t, p, `document.body.dataset.trusted`) != "true" {
			t.Fatal("fill did not use trusted browser input")
		}
	}
	_, err = p.Fill(testContext(t), targetNamed(t, p, "Readonly"), "changed")
	var e *cdp.Error
	if !errors.As(err, &e) || e.Code != "blocked" {
		t.Fatalf("readonly fill: %v", err)
	}
	if evalValue(t, p, `document.querySelector('[readonly]').value`) != "fixed" {
		t.Fatal("readonly field changed")
	}
	if _, err := p.Eval(testContext(t), `{const input=document.querySelector('#text');input.value='old';input.addEventListener('select',()=>input.setSelectionRange(0,0),{once:true})}`); err != nil {
		t.Fatal(err)
	}
	_, err = p.Fill(testContext(t), targetNamed(t, p, "Text"), "new")
	if !errors.As(err, &e) || e.Code != "blocked" {
		t.Fatalf("selection redirected: %v", err)
	}
	if evalValue(t, p, `document.querySelector('#text').value`) != "old" {
		t.Fatal("fill edited a redirected selection")
	}
}

func TestChromeClickRejectsHoverOverlayAndAncestorCover(t *testing.T) {
	t.Run("hover", func(t *testing.T) {
		fixture := actionFixture(t)
		c := browserClient(t, chrome(t, "about:blank"))
		p, err := c.Open(testContext(t), fixture.URL)
		if err != nil {
			t.Fatal(err)
		}
		ref := targetNamed(t, p, "Hit")
		if _, err := p.Eval(testContext(t), `document.querySelector('#hit').onmouseenter=()=>document.body.insertAdjacentHTML('beforeend','<div style="position:fixed;inset:0;z-index:999;background:white"></div>');void 0`); err != nil {
			t.Fatal(err)
		}
		_, err = p.Click(testContext(t), ref)
		var e *cdp.Error
		if !errors.As(err, &e) || e.Code != "blocked" {
			t.Fatalf("hover cover: %v", err)
		}
		if evalValue(t, p, `String(document.querySelector('#hit').dataset.count||0)`) != "0" {
			t.Fatal("hover caused unsafe input")
		}
	})
	t.Run("ancestor", func(t *testing.T) {
		fixture := observationFixture(t)
		c := browserClient(t, chrome(t, "about:blank"))
		p, err := c.Open(testContext(t), fixture.URL)
		if err != nil {
			t.Fatal(err)
		}
		ref := targetNamed(t, p, "Nested inner button")
		if _, err := p.Eval(testContext(t), `document.body.insertAdjacentHTML('beforeend','<div style="position:fixed;inset:0;z-index:999;background:white"></div>')`); err != nil {
			t.Fatal(err)
		}
		_, err = p.Click(testContext(t), ref)
		var e *cdp.Error
		if !errors.As(err, &e) || e.Code != "blocked" {
			t.Fatalf("ancestor cover: %v", err)
		}
		read, err := p.Read(testContext(t), cdp.ReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(read.String(), "Nested inner button!") {
			t.Fatal("input bypassed covered ancestor frame")
		}
	})
}

func TestChromeClickTraversesNestedFramesAndShadows(t *testing.T) {
	fixture := observationFixture(t)
	endpoint := chrome(t, "about:blank")
	c := browserClient(t, endpoint)
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	controls, err := p.Controls(testContext(t), cdp.ControlsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	for _, name := range []string{"Nested inner button", "Cross origin button", "Same origin button", "Open shadow button", "Closed shadow button", "Root button"} {
		var ref cdp.ControlRef
		for _, control := range controls.Controls {
			if control.Name == name {
				ref = control.Target
			}
		}
		if ref == "" {
			t.Fatalf("missing fixture control %s", name)
		}
		client := browserClient(t, endpoint)
		page, err := client.Page(testContext(t), controls.Page.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := page.Click(testContext(t), ref); err != nil {
			t.Fatalf("click %s: %v", name, err)
		}
		read, err := page.Read(testContext(t), cdp.ReadOptions{})
		if err != nil || !strings.Contains(read.String(), name+"!") {
			t.Fatalf("click %s did not hit intended frame/shadow node: %s %v", name, read, err)
		}
		client.Close()
	}
}

func TestChromeClickReattachesAndRejectsUnsafeTargets(t *testing.T) {
	fixture := actionFixture(t)
	endpoint := chrome(t, "about:blank")
	c := browserClient(t, endpoint)
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	ref := targetNamed(t, p, "Hit")
	id, err := ref.PageID()
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	c = browserClient(t, endpoint)
	p, err = c.Page(testContext(t), id)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Click(testContext(t), ref)
	if err != nil {
		t.Fatal(err)
	}
	if result.Page.Title != "Clicked" || evalValue(t, p, `document.querySelector('#hit').dataset.count`) != "1" {
		t.Fatalf("click: %s", result)
	}
	if _, err := p.Eval(testContext(t), `document.body.insertAdjacentHTML('beforeend','<div id="overlay" style="position:fixed;inset:0;z-index:999;background:white"></div>')`); err != nil {
		t.Fatal(err)
	}
	_, err = p.Click(testContext(t), ref)
	var e *cdp.Error
	if !errors.As(err, &e) || e.Code != "blocked" {
		t.Fatalf("overlay: %v", err)
	}
	if evalValue(t, p, `document.querySelector('#hit').dataset.count`) != "1" {
		t.Fatal("overlay click dispatched input")
	}
	if _, err := p.Eval(testContext(t), `document.querySelector('#overlay').remove(); const old=document.querySelector('#hit');old.replaceWith(old.cloneNode(true))`); err != nil {
		t.Fatal(err)
	}
	_, err = p.Click(testContext(t), ref)
	if !errors.As(err, &e) || e.Code != "stale" {
		t.Fatalf("replacement: %v", err)
	}
	fresh := targetNamed(t, p, "Hit")
	if err := p.Navigate(testContext(t), fixture.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(testContext(t), fresh); !errors.As(err, &e) || e.Code != "stale" {
		t.Fatalf("navigation staleness: %v", err)
	}
	if _, err := p.Click(testContext(t), "invalid"); !errors.As(err, &e) || e.Code != "invalid_input" {
		t.Fatalf("malformed ref: %v", err)
	}
}
