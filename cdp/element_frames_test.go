//go:build integration

package cdp_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestElementActionsInTransformedFramesAndShadows(t *testing.T) {
	var fixture *httptest.Server
	fixture = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		cross := strings.Replace(fixture.URL, "127.0.0.1", "localhost", 1)
		fmt.Fprint(w, `<!doctype html><title>Transformed controls</title><style>iframe{width:500px;height:280px;transform:rotate(11deg) scale(.7);transform-origin:0 0;border:9px solid;pointer-events:none}button,input,select{pointer-events:none}</style>`)
		switch r.URL.Path {
		case "/":
			fmt.Fprintf(w, `<iframe src="%s/same-middle"></iframe><iframe src="%s/cross-middle"></iframe>`, fixture.URL, cross)
			for _, mode := range []string{"open", "closed"} {
				markup, _ := json.Marshal(fmt.Sprintf(`<button>Shadow %s button</button><input aria-label="Shadow %s field" value="old"><select aria-label="Shadow %s choice"><option value="a">Alpha</option><option value="b">Beta</option></select>`, mode, mode, mode))
				fmt.Fprintf(w, `<div id="%s"></div><script>{const root=document.getElementById('%s').attachShadow({mode:'%s'});root.innerHTML=%s;const button=root.querySelector('button');button.onclick=e=>button.textContent='Shadow %s clicked:'+String(root.activeElement===button)+':'+e.isTrusted}</script>`, mode, mode, mode, markup, mode)
			}
			fmt.Fprint(w, `<div style="position:fixed;inset:0;z-index:999;background:white"></div>`)
		case "/same-middle":
			fmt.Fprint(w, `<iframe style="transform:skew(15deg) scale(.8)" src="/same-leaf"></iframe>`)
		case "/cross-middle":
			fmt.Fprintf(w, `<iframe style="transform:skew(-15deg) scale(.8)" src="%s/cross-leaf"></iframe>`, fixture.URL)
		case "/same-leaf", "/cross-leaf":
			name := "Same"
			if r.URL.Path == "/cross-leaf" {
				name = "Cross"
			}
			fmt.Fprintf(w, `<button onclick="this.textContent='%s clicked:'+String(document.activeElement===this)+':'+event.isTrusted">%s button</button><input aria-label="%s field" value="old"><select aria-label="%s choice"><option value="a">Alpha</option><option value="b">Beta</option></select>`, name, name, name, name)
		}
	}))
	t.Cleanup(fixture.Close)
	elementDrivers(t, func(t *testing.T, d elementDriver) {
		for _, name := range []string{"Same", "Cross", "Shadow open", "Shadow closed"} {
			t.Run(name, func(t *testing.T) {
				d.open(t, fixture.URL)
				before := d.read(t)
				if !before.Complete {
					t.Fatalf("fixture capture incomplete: %s", before)
				}
				d.act(t, "click", d.target(t, name+" button"), "", "")
				after := d.read(t)
				if !strings.Contains(after.String(), name+" clicked:true:false") {
					t.Fatalf("activation missed bound frame/shadow button or focus: %s", after)
				}
				d.act(t, "fill", d.target(t, name+" field"), "replacement", "")
				d.value(t, name+" field", "replacement")
				// Press remains input to actual deep browser focus, across CLI processes.
				d.act(t, "press", 0, "Control+A", "")
				d.act(t, "press", 0, "z", "")
				d.value(t, name+" field", "z")
				d.act(t, "select", d.target(t, name+" choice"), "Beta", "")
				d.value(t, name+" choice", "Beta")
			})
		}
	})
}

func TestElementFillRejectsInactiveFrameFocus(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/frame" {
			fmt.Fprint(w, `<input id="field" aria-label="Frame field" value="old" onfocus="parent.document.querySelector('input').focus()"><button onclick="parent.document.title=field.value+'|'+parent.document.querySelector('input').value">Inspect</button>`)
			return
		}
		fmt.Fprint(w, `<title>Untouched</title><input aria-label="Parent field" value="parent"><iframe src="/frame"></iframe>`)
	}))
	t.Cleanup(fixture.Close)
	elementDrivers(t, func(t *testing.T, d elementDriver) {
		d.open(t, fixture.URL)
		d.act(t, "fill", d.target(t, "Frame field"), "must not leak", "blocked")
		d.act(t, "click", d.target(t, "Inspect"), "", "")
		d.title(t, "old|parent")
	})
}

func TestElementClickNavigationAndUserActivatedPopup(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/destination" {
			fmt.Fprint(w, `<title>Destination</title><h1>Destination loaded</h1>`)
			return
		}
		fmt.Fprint(w, `<title>Before</title><a href="/destination" style="pointer-events:none">Navigate</a><button onclick="const popup=window.open('/destination');document.title=popup?'Opened once':'Blocked'">Script popup</button><div style="position:fixed;inset:0;background:white"></div>`)
	}))
	t.Cleanup(fixture.Close)
	elementDrivers(t, func(t *testing.T, d elementDriver) {
		d.open(t, fixture.URL)
		result := d.act(t, "click", d.target(t, "Navigate"), "", "")
		if result.Page.URL != fixture.URL+"/destination" || result.Page.Title != "Destination" || len(result.NewPages) != 0 {
			t.Fatalf("navigation outcome: %+v", result)
		}
		if r := d.read(t); !strings.Contains(r.String(), "Destination loaded") {
			t.Fatalf("native link did not load destination: %s", r)
		}
		d.open(t, fixture.URL)
		result = d.act(t, "click", d.target(t, "Script popup"), "", "")
		if result.Page.Title != "Opened once" || len(result.NewPages) != 1 || result.NewPages[0].URL != fixture.URL+"/destination" || result.NewPages[0].ID == result.Page.ID {
			t.Fatalf("gesture-gated popup outcome: %+v", result)
		}
		if r := d.readPage(t, result.NewPages[0].ID); !strings.Contains(r.String(), "Destination loaded") {
			t.Fatalf("popup did not load its intended destination: %s", r)
		}
	})
}
