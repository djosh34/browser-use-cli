//go:build integration

package cdp_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func observationFixture(t *testing.T) *httptest.Server {
	t.Helper()
	var s *httptest.Server
	s = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		cross := strings.Replace(s.URL, "127.0.0.1", "localhost", 1)
		switch r.URL.Path {
		case "/":
			fmt.Fprintf(w, `<!doctype html><title>Frames and shadows</title><h1>Root heading</h1><button onclick="this.textContent+=String.fromCharCode(33)">Root button</button><div class="region">Root region <span class="region">nested region</span></div><div>Outside selector</div><div id="open"></div><div id="closed"></div><script>document.querySelector('#open').attachShadow({mode:'open'}).innerHTML='<p class="region">Open shadow text</p><button onclick="this.textContent+=String.fromCharCode(33)">Open shadow button</button>';document.querySelector('#closed').attachShadow({mode:'closed'}).innerHTML='<p class="region">Closed shadow text</p><button onclick="this.textContent+=String.fromCharCode(33)">Closed shadow button</button>';</script><iframe style="margin-top:900px;border:7px solid" src="%s/same"></iframe>`, s.URL)
		case "/same":
			fmt.Fprintf(w, `<h2>Same origin heading</h2><button onclick="this.textContent+=String.fromCharCode(33)">Same origin button</button><p class="region">Same origin region</p><iframe src="%s/cross"></iframe>`, cross)
		case "/cross":
			fmt.Fprintf(w, `<h2>Cross origin heading</h2><button onclick="this.textContent+=String.fromCharCode(33)">Cross origin button</button><p class="region">Cross origin region</p><iframe src="%s/inner"></iframe>`, s.URL)
		case "/inner":
			fmt.Fprintf(w, `<h2>Nested inner heading</h2><button onclick="this.textContent+=String.fromCharCode(33)">Nested inner button</button><input aria-label="Inner text"><button onclick="alert(String.fromCharCode(33))">Inner dialog</button><a href="%s/inner-next">Inner navigation</a><p class="region">Nested inner region</p>`, cross)
		case "/inner-next":
			fmt.Fprintf(w, `<h2>Inner destination</h2><p>Changed frame document</p><button onclick="this.nextElementSibling.focus()">Focus return</button><a href="%s/inner">Return inner</a>`, s.URL)
		}
	}))
	s.Start()
	t.Cleanup(s.Close)
	return s
}
