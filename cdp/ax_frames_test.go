//go:build integration

package cdp_test

import (
	"fmt"
	"github.com/djosh34/browser-use-cli/cdp"
	"strings"
	"testing"
)

func TestAXSelectorFindsSameProcessDocumentInsideAuthorShadow(t *testing.T) {
	for _, mode := range []string{"open", "closed"} {
		t.Run(mode, func(t *testing.T) {
			p := axPage(t, fmt.Sprintf(`<title>Shadow frame</title><button>Outside</button><div id="host"></div><script>const root=document.querySelector('#host').attachShadow({mode:%q});const frame=document.createElement('iframe');frame.srcdoc='<main class="region"><button>Child button</button></main>';root.append(frame)</script>`, mode))
			full, err := p.Read(testContext(t), cdp.ReadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !full.Complete {
				t.Fatalf("full capture incomplete: %s", full)
			}
			child := axNamed(t, full, "Child button")
			scoped, err := p.Read(testContext(t), cdp.ReadOptions{Selector: ".region"})
			if err != nil {
				t.Fatal(err)
			}
			if !scoped.Complete {
				t.Fatalf("shadow child CSS capture incomplete: %s", scoped)
			}
			names := controlNames(scoped)
			if len(names) != 1 || names[0] != "Child button" {
				t.Fatalf("scope: %s", scoped)
			}
			if axNamed(t, scoped, "Child button").ID != child.ID {
				t.Fatal("filtered child was renumbered")
			}
		})
	}
}

func TestAXHiddenEmbeddingsDoNotResurrectChildDocuments(t *testing.T) {
	p := axPage(t, `<title>Hidden frames</title><button>Visible</button><iframe hidden srcdoc="<button>Hidden child</button>"></iframe><iframe aria-hidden="true" srcdoc="<button>ARIA hidden child</button>"></iframe><div inert><iframe srcdoc="<button>Inert child</button>"></iframe></div>`)
	r, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete {
		t.Fatalf("hidden frames are not failed captures: %s", r)
	}
	if got := controlNames(r); len(got) != 1 || got[0] != "Visible" {
		t.Fatalf("hidden child resurrection: %v\n%s", got, r)
	}
}

func TestAXFramesAndShadowsSharePreorder(t *testing.T) {
	fixture := observationFixture(t)
	c := browserClient(t, chrome(t, "--window-size=1440,1000", "--ozone-override-screen-size=1440,1000", "--force-device-scale-factor=1", "--site-per-process", "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete {
		t.Fatalf("frames incomplete: %s", r)
	}
	for i, name := range []string{"Root button", "Open shadow button", "Closed shadow button", "Same origin button", "Cross origin button", "Nested inner button", "Inner text", "Inner dialog", "Inner navigation"} {
		if n := axNamed(t, r, name); n.ID != cdp.ControlID(i+1) {
			t.Errorf("%q ID=%d, want %d", name, n.ID, i+1)
		}
	}
	documents := 0
	for _, n := range axNodes(r.Tree) {
		if n.Role == "document" {
			documents++
			if n.URL == "" {
				t.Error("document missing URL")
			}
		}
	}
	if documents != 4 {
		t.Fatalf("documents=%d: %s", documents, r)
	}
	scoped, err := p.Read(testContext(t), cdp.ReadOptions{Selector: ".region"})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Root region", "nested region", "Open shadow text", "Closed shadow text", "Same origin region", "Cross origin region", "Nested inner region"} {
		if !strings.Contains(scoped.String(), text) {
			t.Errorf("missing scoped %q: %s", text, scoped)
		}
	}
	if strings.Contains(scoped.String(), "Outside selector") || strings.Contains(scoped.String(), "heading") {
		t.Errorf("scope escaped: %s", scoped)
	}
}
