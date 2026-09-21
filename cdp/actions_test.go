//go:build integration

package cdp_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
