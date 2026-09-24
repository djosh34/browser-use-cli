//go:build integration

package cdp_test

import (
	"errors"
	"github.com/djosh34/browser-use-cli/cdp"
	"reflect"
	"testing"
)

func controlNames(r cdp.ReadResult) []string {
	var result []string
	for _, n := range axNodes(r.Tree) {
		if n.ID != 0 {
			result = append(result, n.Name)
		}
	}
	return result
}

func TestAXFiltersKeepOrdinalsOwnershipAndEmptyMatches(t *testing.T) {
	p := axPage(t, `<title>Scopes</title><button>Before</button><main><p>Unrelated text</p><div class="scope" role="presentation"><button>Inside</button><div class="scope" role="group" aria-label="Owner" aria-owns="external"><button>Nested</button></div></div></main><button id="external">Owned outside DOM</button><button>After</button><div hidden class="empty"><button>Hidden</button></div><div aria-hidden="true" class="empty">Secret</div>`)
	full, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := p.Read(testContext(t), cdp.ReadOptions{Selector: ".scope"})
	if err != nil {
		t.Fatal(err)
	}
	if got := controlNames(scoped); !reflect.DeepEqual(got, []string{"Inside", "Nested", "Owned outside DOM"}) {
		t.Fatalf("scope ownership/overlap: %v\n%s", got, scoped)
	}
	for _, name := range controlNames(scoped) {
		if axNamed(t, scoped, name).ID != axNamed(t, full, name).ID {
			t.Fatalf("renumbered %s", name)
		}
	}
	controls, err := p.Read(testContext(t), cdp.ReadOptions{Selector: ".scope", ControlsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range axNodes(controls.Tree) {
		if n.Text != "" {
			t.Fatalf("controls-only leaked unrelated text: %s", controls)
		}
	}
	if axNamed(t, controls, "Owner").Role != "group" {
		t.Fatal("lost ancestor path")
	}
	for _, selector := range []string{"[", ".missing"} {
		_, err := p.Read(testContext(t), cdp.ReadOptions{Selector: selector})
		var e *cdp.Error
		if !errors.As(err, &e) || e.Code != "invalid_input" {
			t.Fatalf("selector %s: %v", selector, err)
		}
	}
	empty, err := p.Read(testContext(t), cdp.ReadOptions{Selector: ".empty"})
	if err != nil || empty.Tree != nil || !empty.Complete {
		t.Fatalf("matched hidden must be empty: %s %v", empty, err)
	}
	textOnly, err := p.Read(testContext(t), cdp.ReadOptions{Selector: "p", ControlsOnly: true})
	if err != nil || textOnly.Tree != nil {
		t.Fatalf("text-only controls: %s %v", textOnly, err)
	}
}

func TestAXSelectorMapsEachMatchToNearestExposedRoots(t *testing.T) {
	p := axPage(t, `<title>Outgoing ownership</title><main id="scope"><button>Inside</button><button id="moved">Owned away</button></main><div role="group" aria-label="Outside owner" aria-owns="moved"></div><div id="wrapper" role="presentation"><section aria-label="First region"><button>First</button><button id="movedToo">Also owned away</button></section><section aria-label="Second region"><button>Second</button></section></div><div role="group" aria-label="Second owner" aria-owns="movedToo"></div>`)
	full, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if owner := axNamed(t, full, "Outside owner"); len(owner.Children) != 1 || owner.Children[0].Name != "Owned away" {
		t.Fatalf("fixture ownership not exposed: %s", full)
	}
	cases := []struct {
		selector string
		names    []string
	}{
		{"#scope", []string{"Inside"}},
		{"#scope, #moved", []string{"Inside", "Owned away"}},
		{"#wrapper", []string{"First", "Second"}},
		{"#wrapper, #movedToo", []string{"First", "Second", "Also owned away"}},
	}
	for _, tc := range cases {
		t.Run(tc.selector, func(t *testing.T) {
			scoped, err := p.Read(testContext(t), cdp.ReadOptions{Selector: tc.selector})
			if err != nil {
				t.Fatal(err)
			}
			if got := controlNames(scoped); !reflect.DeepEqual(got, tc.names) {
				t.Fatalf("CSS match must retain AX subtrees of nearest exposed roots: got %v want %v\n%s", got, tc.names, scoped)
			}
			for _, name := range tc.names {
				if axNamed(t, scoped, name).ID != axNamed(t, full, name).ID {
					t.Fatalf("renumbered %s", name)
				}
			}
		})
	}
}

func TestAXSelectorMatchesSoleDeduplicatedTextRegion(t *testing.T) {
	p := axPage(t, `<button><span class="label">Label</span></button>`)
	r, err := p.Read(testContext(t), cdp.ReadOptions{Selector: ".label"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Tree == nil {
		t.Fatal("visible matched label was lost by normalization")
	}
}
