//go:build integration

package cdp_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/djosh34/browser-use-cli/cdp"
)

func axPage(t *testing.T, html string) *cdp.Page {
	t.Helper()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, html)
	}))
	t.Cleanup(fixture.Close)
	c := browserClient(t, chrome(t, "--window-size=1440,1000", "--ozone-override-screen-size=1440,1000", "--force-device-scale-factor=1", "--site-per-process", "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func axNodes(n *cdp.Node) []*cdp.Node {
	if n == nil {
		return nil
	}
	result := []*cdp.Node{n}
	for _, c := range n.Children {
		result = append(result, axNodes(c)...)
	}
	return result
}
func axNamed(t *testing.T, r cdp.ReadResult, name string) *cdp.Node {
	t.Helper()
	for _, n := range axNodes(r.Tree) {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("missing %q: %s", name, r)
	return nil
}

func TestAXSemanticsStatesAndProtectedValues(t *testing.T) {
	p := axPage(t, `<title>Semantics</title><main><h2>Heading</h2><button aria-describedby="desc">Exact label</button><p id="desc">Distinct description.</p><button aria-pressed="mixed">Mixed</button><input type="checkbox" aria-label="Unchecked"><input aria-label="Ordinary" value="visible" readonly aria-invalid="grammar"><input aria-label="Password" type="password" value="length-must-not-leak"><p>Repeat</p><p>Repeat</p><button aria-label="Prefix">Prefix suffix</button></main>`)
	r, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Tree.Role != "document" || !r.Complete {
		t.Fatalf("root/completeness: %s", r)
	}
	h := axNamed(t, r, "Heading")
	if h.Role != "heading" || h.Level != 2 || len(h.Children) != 0 {
		t.Fatalf("heading dedup: %+v", h)
	}
	b := axNamed(t, r, "Exact label")
	if b.Description != "Distinct description." || len(b.Children) != 0 {
		t.Fatalf("description/dedup: %+v", b)
	}
	if axNamed(t, r, "Mixed").State["pressed"] != "mixed" || axNamed(t, r, "Unchecked").State["checked"] != false {
		t.Fatalf("mixed/false lost: %s", r)
	}
	ordinary := axNamed(t, r, "Ordinary")
	if ordinary.Value == nil || *ordinary.Value != "visible" || ordinary.State["readonly"] != true || ordinary.State["invalid"] != true {
		t.Fatalf("ordinary field: %+v", ordinary)
	}
	if axNamed(t, r, "Password").Value != nil {
		t.Fatal("password value leaked")
	}
	if len(axNamed(t, r, "Prefix").Children) != 1 {
		t.Fatalf("substring dedup: %s", r)
	}
	repeats := 0
	for _, n := range axNodes(r.Tree) {
		if n.Text == "Repeat" {
			repeats++
		}
	}
	if repeats != 2 {
		t.Fatalf("cross-element dedup: %s", r)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var copy cdp.ReadResult
	if err = json.Unmarshal(data, &copy); err != nil {
		t.Fatal(err)
	}
	if r.String() != copy.String() {
		t.Fatal("JSON and String disagree")
	}
	if strings.Contains(string(data), "length-must-not-leak") || strings.Contains(string(data), "••") {
		t.Fatal("protected value leaked")
	}
}

func TestAXRolesEditableRootsAndOptions(t *testing.T) {
	p := axPage(t, `<title>Roles</title><div contenteditable aria-label="Editor">Editable <b>bold</b><div>nested</div></div><div tabindex="0" aria-label="Generic">Context</div><div role="menu" aria-label="Menu"><div role="menuitem">Item</div></div><div role="grid"><div role="row"><div role="gridcell" aria-label="Static cell">A</div><div role="gridcell" tabindex="0" aria-label="Active cell">B</div></div></div><div role="separator" aria-label="Static separator"></div><div role="separator" tabindex="0" aria-label="Active separator"></div><progress aria-label="Progress"></progress><select aria-label="Choice"><option value="a">Visible A</option><option value="b" selected>Visible B</option><option disabled>Disabled option</option></select><input type="date" aria-label="Date"><input type="time" aria-label="Time"><input type="color" aria-label="Color"><details><summary>Disclosure</summary>Detail</details><button></button>`)
	r, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Editor", "Item", "Active cell", "Active separator", "Choice", "Visible A", "Visible B", "Disabled option", "Date", "Time", "Color", "Disclosure"} {
		if axNamed(t, r, name).ID == 0 {
			t.Errorf("unnumbered %q: %s", name, r)
		}
	}
	for _, name := range []string{"Generic", "Menu", "Static cell", "Static separator", "Progress"} {
		if axNamed(t, r, name).ID != 0 {
			t.Errorf("numbered context %q", name)
		}
	}
	editor := axNamed(t, r, "Editor")
	for _, n := range axNodes(editor)[1:] {
		if n.ID != 0 {
			t.Errorf("duplicate editable descendant: %+v", n)
		}
	}
	b := axNamed(t, r, "Visible B")
	if b.State["selected"] != true || axNamed(t, r, "Disabled option").State["disabled"] != true {
		t.Fatalf("option states: %+v", b)
	}
	if axNamed(t, r, "Choice").Value == nil || *axNamed(t, r, "Choice").Value != "Visible B" {
		t.Fatal("select value not AX label")
	}
	roles := map[string]string{}
	for _, name := range []string{"Date", "Time", "Color", "Disclosure"} {
		roles[name] = axNamed(t, r, name).Role
	}
	want := map[string]string{"Date": "Date", "Time": "InputTime", "Color": "ColorWell", "Disclosure": "DisclosureTriangle"}
	if !reflect.DeepEqual(roles, want) {
		t.Fatalf("native aliases = %#v", roles)
	}
}

func TestAXVisibilityOverrideRetainsExposedDescendants(t *testing.T) {
	p := axPage(t, `<title>Visibility override</title><div style="visibility:hidden"><button>Hidden inherited</button><button style="visibility:visible">Visible descendant</button></div><button>Outside</button>`)
	r, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete {
		t.Fatalf("visibility capture incomplete: %s", r)
	}
	if got := controlNames(r); !reflect.DeepEqual(got, []string{"Visible descendant", "Outside"}) {
		t.Fatalf("native exposed descendant lost: %s", r)
	}
}

func TestAXNativeModalPreservesExposedDialog(t *testing.T) {
	p := axPage(t, `<title>Native dialog</title><button>Background</button><dialog><p>Modal content</p><button>Inside dialog</button></dialog><script>document.querySelector('dialog').showModal()</script>`)
	r, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete {
		t.Fatalf("modal incomplete: %s", r)
	}
	if got := controlNames(r); !reflect.DeepEqual(got, []string{"Inside dialog"}) {
		t.Fatalf("native exposed modal content lost: %s", r)
	}
	if !strings.Contains(r.String(), "Modal content") {
		t.Fatalf("dialog text missing: %s", r)
	}
}

func TestAXDirectRolesVersusCollectionContexts(t *testing.T) {
	p := axPage(t, `<title>Role matrix</title><div role="button" aria-label="Button"></div><a href="#">Link</a><div role="checkbox" aria-label="Checkbox" aria-checked="mixed"></div><div role="radio" aria-label="Radio" aria-checked="false"></div><div role="textbox" aria-label="Textbox"></div><div role="searchbox" aria-label="Search"></div><div role="combobox" aria-label="Combo" aria-expanded="false"></div><div role="listbox" aria-label="Listbox"><div role="option" aria-label="Option" aria-selected="false"></div></div><div role="menu"><div role="menuitem" aria-label="Menuitem"></div><div role="menuitemcheckbox" aria-label="Menucheck" aria-checked="mixed"></div><div role="menuitemradio" aria-label="Menuradio" aria-checked="false"></div></div><div role="slider" aria-label="Slider" aria-valuenow="4"></div><div role="spinbutton" aria-label="Spin" aria-valuenow="2"></div><div role="scrollbar" aria-label="Scroll" aria-valuenow="10" aria-controls="content"></div><div role="switch" aria-label="Switch" aria-checked="false"></div><div role="tablist" aria-label="Tablist"><div role="tab" aria-label="Tab" aria-selected="false"></div></div><div role="tree" aria-label="Tree"><div role="treeitem" aria-label="Treeitem"></div></div><div role="menubar" aria-label="Menubar"></div><div role="radiogroup" aria-label="Radiogroup"></div><div role="tabpanel" aria-label="Panel"></div><input type="datetime-local" aria-label="Datetime"><button disabled aria-label="Disabled"></button><button></button>`)
	r, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"Button", "Link", "Checkbox", "Radio", "Textbox", "Search", "Combo", "Listbox", "Option", "Menuitem", "Menucheck", "Menuradio", "Slider", "Spin", "Scroll", "Switch", "Tab", "Treeitem"}
	for i, name := range names {
		if n := axNamed(t, r, name); n.ID != cdp.ControlID(i+1) {
			t.Errorf("%s id=%d want=%d", name, n.ID, i+1)
		}
	}
	for _, name := range []string{"Tablist", "Tree", "Menubar", "Radiogroup", "Panel"} {
		if axNamed(t, r, name).ID != 0 {
			t.Errorf("collection/context numbered: %s", name)
		}
	}
	if n := axNamed(t, r, "Datetime"); n.ID == 0 || n.Role != "DateTime" {
		t.Fatalf("datetime alias: %+v", n)
	}
	if n := axNamed(t, r, "Disabled"); n.ID == 0 || n.State["disabled"] != true {
		t.Fatalf("disabled omitted: %+v", n)
	}
	unnamed := 0
	for _, n := range axNodes(r.Tree) {
		if n.Role == "button" && n.Name == "" && n.ID != 0 {
			unnamed++
		}
	}
	if unnamed != 1 {
		t.Fatalf("unnamed controls=%d", unnamed)
	}
	if axNamed(t, r, "Checkbox").State["checked"] != "mixed" || axNamed(t, r, "Combo").State["expanded"] != false || axNamed(t, r, "Option").State["selected"] != false {
		t.Fatalf("meaningful false/mixed state: %s", r)
	}
}

func TestAXLongDistinctContentIsNotTruncated(t *testing.T) {
	content := strings.Repeat("Long Unicode 日本語 paragraph. ", 10000)
	p := axPage(t, `<title>Long content</title><article><p>`+content+`</p><p>Tail survives.</p></article>`)
	r, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, n := range axNodes(r.Tree) {
		text.WriteString(n.Text)
	}
	if !strings.Contains(text.String(), strings.TrimSpace(content)) || !strings.Contains(text.String(), "Tail survives.") {
		t.Fatalf("long content truncated (%d bytes)", text.Len())
	}
}

func TestAXExposedContentNotLayoutDiscovery(t *testing.T) {
	p := axPage(t, `<title>Exposure</title><h1>Heading</h1><button disabled>Disabled</button><button style="opacity:0">Transparent</button><button style="display:contents">Contents</button><button style="position:absolute;top:3000px">Offscreen</button><button aria-hidden="true">Hidden AX</button><div inert><button>Inert</button></div><button hidden>Hidden DOM</button>`)
	r, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Heading", "Disabled", "Transparent", "Contents", "Offscreen"} {
		if !strings.Contains(r.String(), want) {
			t.Errorf("missing exposed %q: %s", want, r)
		}
	}
	for _, absent := range []string{"Hidden AX", "Inert", "Hidden DOM"} {
		if strings.Contains(r.String(), absent) {
			t.Errorf("resurrected hidden %q: %s", absent, r)
		}
	}
}
