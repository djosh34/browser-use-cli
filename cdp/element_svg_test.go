//go:build integration

package cdp_test

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestElementClickSVGButtons(t *testing.T) {
	elementDrivers(t, func(t *testing.T, d elementDriver) {
		for _, tc := range []struct{ name, attributes, want string }{
			{"nonfocusable", "", "click:prior:false:true"},
			{"focusable", `tabindex="0"`, "focus:target:true|click:target:false:true"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fixture := elementFixture(t, `<input id="prior" aria-label="Prior" value="untouched">
<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100" style="pointer-events:none">
 <g id="target" role="button" aria-label="SVG button" `+tc.attributes+`><rect width="200" height="100"/><text x="10" y="30">SVG button</text></g>
</svg><div style="position:fixed;inset:0;background:white"></div>
<script>
 const log=[];
 for(const type of ['mouseenter','pointerdown','mousedown','pointerup','mouseup']) target.addEventListener(type,()=>log.push(type));
 // In Chrome an SVG focus listener itself makes the element focusable.
 if(target.hasAttribute('tabindex'))target.addEventListener('focus',()=>log.push('focus:'+document.activeElement.id+':'+navigator.userActivation.isActive));
 target.addEventListener('click',e=>{log.push('click:'+document.activeElement.id+':'+e.isTrusted+':'+navigator.userActivation.isActive);document.title=log.join('|')});
 prior.focus();
</script>`)
				d.open(t, fixture.URL)
				n := axNamed(t, d.read(t), "SVG button")
				if (n.State["focusable"] == true) != (tc.attributes != "") {
					t.Fatalf("SVG fixture has unexpected AX focusability: %+v", n)
				}
				d.act(t, "click", n.ID, "", "")
				d.title(t, tc.want)
				d.value(t, "Prior", "untouched")
			})
		}
	})
}

func TestElementClickSVGNativeLinks(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/destination" {
			fmt.Fprintf(w, `<title>SVG arrived:%s</title><h1>SVG destination loaded</h1>`, html.EscapeString(r.URL.Query().Get("event")))
			return
		}
		target := ""
		if r.URL.Path == "/popup" {
			target = `target="_blank"`
		}
		fmt.Fprintf(w, `<title>Before</title><svg xmlns="http://www.w3.org/2000/svg" width="200" height="100" style="pointer-events:none"><a href="/destination" aria-label="SVG link" %s onclick="this.setAttribute('href','/destination?event='+[navigator.userActivation.isActive,event.isTrusted,document.activeElement===this].join(':'));document.title='SVG clicked'"><rect width="200" height="100"/><text x="10" y="30">SVG link</text></a></svg><div style="position:fixed;inset:0;background:white"></div>`, target)
	}))
	t.Cleanup(fixture.Close)
	elementDrivers(t, func(t *testing.T, d elementDriver) {
		for _, path := range []string{"/navigate", "/popup"} {
			t.Run(path, func(t *testing.T) {
				d.open(t, fixture.URL+path)
				result := d.act(t, "click", d.target(t, "SVG link"), "", "")
				pageID := result.Page.ID
				if path == "/popup" {
					if len(result.NewPages) != 1 || result.NewPages[0].ID == result.Page.ID || result.NewPages[0].URL != fixture.URL+"/destination?event=true:false:true" {
						t.Fatalf("SVG popup outcome: %+v", result)
					}
					pageID = result.NewPages[0].ID
				} else if len(result.NewPages) != 0 || result.Page.URL != fixture.URL+"/destination?event=true:false:true" {
					t.Fatalf("SVG navigation outcome: %+v", result)
				}
				r := d.readPage(t, pageID)
				if r.Page.Title != "SVG arrived:true:false:true" || !strings.Contains(r.String(), "SVG destination loaded") {
					t.Fatalf("SVG default activation did not load destination with user activation: %s", r)
				}
			})
		}
	})
}
