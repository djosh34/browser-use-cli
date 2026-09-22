//go:build integration

package cdp_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
		fmt.Fprint(w, `<!doctype html><title>Actions</title><button id="hit" onclick="document.title='Clicked';this.dataset.count=Number(this.dataset.count||0)+1">Hit</button><label>Text <input id="text" value="old"></label><label>Notes <textarea>old notes</textarea></label><div contenteditable aria-label="Editor">old editor</div><label>Email <input type="email" value="old@example.test"></label><label>Number <input type="number" value="12"></label><label>Choice <select id="choice"><option value="a">Alpha</option><option value="b">Beta</option><option value="d" disabled>Disabled</option><option value="x">Duplicate</option><option value="y">Duplicate</option></select></label><input aria-label="Readonly" readonly value="fixed"><a href="/destination">Navigate</a><a href="#same">Same document</a><a href="/popup" target="_blank">Popup</a><button onclick="alert('blocked')">Dialog</button><div style="width:20px;height:25px;overflow:clip;margin:30px 0 0 100px"><button id="clip" style="width:200px;height:25px;margin-left:-37px" onclick="document.title='Clipped click'">Clipped</button></div><div style="width:70px;margin-top:700px"><a href="#done">Several words wrapping over many lines</a></div>`)
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
func TestChromeClickClippedAndMultirectControls(t *testing.T) {
	fixture := actionFixture(t)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Click(testContext(t), targetNamed(t, p, "Clipped"))
	if err != nil || result.Page.Title != "Clipped click" {
		t.Fatalf("positive-sized clipped target: %s %v", result, err)
	}
	result, err = p.Click(testContext(t), targetNamed(t, p, "Several words wrapping over many lines"))
	if err != nil || !strings.HasSuffix(result.Page.URL, "#done") {
		t.Fatalf("offscreen multi-rectangle link: %s %v", result, err)
	}
}

func TestChromeInputNavigatesRemoteFrameAcrossProcesses(t *testing.T) {
	fixture := observationFixture(t)
	endpoint := chrome(t, "about:blank")
	c := browserClient(t, endpoint)
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	ref := targetNamed(t, p, "Inner navigation")
	old := targetNamed(t, p, "Nested inner button")
	if _, err := p.Click(testContext(t), ref); err != nil {
		t.Fatalf("frame navigation: %v", err)
	}
	read, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil || !strings.Contains(read.String(), "Inner destination") {
		t.Fatalf("frame destination: %s %v", read, err)
	}
	_, err = p.Click(testContext(t), old)
	var typed *cdp.Error
	if !errors.As(err, &typed) || typed.Code != "stale" {
		t.Fatalf("old frame document ref: %v", err)
	}
	if _, err := p.Click(testContext(t), targetNamed(t, p, "Return inner")); err != nil {
		t.Fatalf("navigation into new remote renderer: %v", err)
	}
	read, err = p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil || !strings.Contains(read.String(), "Nested inner heading") {
		t.Fatalf("return frame: %s %v", read, err)
	}
	if _, err := p.Click(testContext(t), targetNamed(t, p, "Inner navigation")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(testContext(t), targetNamed(t, p, "Focus return")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Press(testContext(t), "Enter"); err != nil {
		t.Fatalf("focused frame navigation: %v", err)
	}
	read, err = p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil || !strings.Contains(read.String(), "Nested inner heading") {
		t.Fatalf("keyboard frame destination: %s %v", read, err)
	}
}

func TestChromeInputDialogInRemoteFrame(t *testing.T) {
	fixture := observationFixture(t)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Click(testContext(t), targetNamed(t, p, "Inner dialog"))
	var typed *cdp.Error
	if !errors.As(err, &typed) || typed.Code != "dialog" {
		t.Fatalf("remote native dialog: %v", err)
	}
}

func TestChromeInputSameDocumentPopupAndDialog(t *testing.T) {
	fixture := actionFixture(t)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	hit := targetNamed(t, p, "Hit")
	result, err := p.Click(testContext(t), targetNamed(t, p, "Same document"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(result.Page.URL, "/#same") {
		t.Fatalf("same-document URL missing: %s", result)
	}
	if _, err := p.Click(testContext(t), hit); err != nil {
		t.Fatalf("same document invalidated existing node: %v", err)
	}
	result, err = p.Click(testContext(t), targetNamed(t, p, "Popup"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.NewPages) != 1 || result.NewPages[0].ID == result.Page.ID {
		t.Fatalf("native popup not reported: %s", result)
	}
	text := result.String()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Click(testContext(t), targetNamed(t, p, "Dialog"))
	var typed *cdp.Error
	if !errors.As(err, &typed) || typed.Code != "dialog" {
		t.Fatalf("native dialog: %v", err)
	}
	c.Close()
	again, err := json.Marshal(result)
	if err != nil || string(again) != string(data) || result.String() != text {
		t.Fatal("captured action result changed after disconnect")
	}
}

func TestChromeInputDoesNotAwaitLaterSuggestionsOrBackgroundFetch(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/suggestions" {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			fmt.Fprint(w, "Suggested place")
			return
		}
		fmt.Fprint(w, `<title>Before</title><input aria-label="Search" oninput="document.title='Immediate';setTimeout(()=>fetch('/suggestions').then(r=>r.text()).then(text=>document.querySelector('output').textContent=text),0)"><output></output>`)
	}))
	t.Cleanup(fixture.Close)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Fill(testContext(t), targetNamed(t, p, "Search"), "place")
	if err != nil {
		t.Fatal(err)
	}
	if result.Page.Title != "Immediate" {
		t.Fatalf("synchronous work was not observed: %s", result)
	}
	read, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read.String(), "Suggested place") {
		t.Fatal("fixture did not delay suggestions")
	}
	once.Do(func() { close(release) })
	if _, err := p.Eval(testContext(t), `new Promise(resolve=>{const output=document.querySelector('output');if(output.textContent){resolve(true);return}const observer=new MutationObserver(()=>{observer.disconnect();resolve(true)});observer.observe(output,{childList:true})})`); err != nil {
		t.Fatal(err)
	}
	read, err = p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil || !strings.Contains(read.String(), "Suggested place") {
		t.Fatalf("later observation missed suggestions: %s %v", read, err)
	}
}

func TestChromeInputsWaitForTriggeredDocumentLoad(t *testing.T) {
	for _, tc := range []struct{ kind, target string }{{"click", "Navigate"}, {"fill", "Auto"}, {"press", "Query"}, {"select", "Route"}} {
		t.Run(tc.kind, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var startOnce, releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/":
					fmt.Fprint(w, `<title>Before</title><a href="/destination">Navigate</a><input aria-label="Auto" oninput="location.href='/destination'"><form action="/destination"><input aria-label="Query"></form><select aria-label="Route" onchange="location.href=this.value"><option value="">Stay</option><option value="/destination">Go</option></select>`)
				case "/destination":
					fmt.Fprint(w, `<title>Loaded</title><img src="/slow">`)
				case "/slow":
					startOnce.Do(func() { close(started) })
					select {
					case <-release:
					case <-r.Context().Done():
					}
				}
			}))
			t.Cleanup(fixture.Close)
			endpoint := chrome(t, "about:blank")
			c := browserClient(t, endpoint)
			p, err := c.Open(testContext(t), fixture.URL)
			if err != nil {
				t.Fatal(err)
			}
			ref := targetNamed(t, p, tc.target)
			id, err := ref.PageID()
			if err != nil {
				t.Fatal(err)
			}
			if tc.kind == "press" {
				if _, err := p.Fill(testContext(t), ref, "place"); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() {
				var result cdp.ActionResult
				var err error
				switch tc.kind {
				case "click":
					result, err = p.Click(testContext(t), ref)
				case "fill":
					result, err = p.Fill(testContext(t), ref, "place")
				case "press":
					result, err = p.Press(testContext(t), "Enter")
				case "select":
					result, err = p.Select(testContext(t), ref, "Go")
				}
				if err == nil && (result.Page.Title != "Loaded" || !strings.HasPrefix(result.Page.URL, fixture.URL+"/destination")) {
					err = fmt.Errorf("wrong completed page: %s", result)
				}
				done <- err
			}()
			select {
			case <-started:
			case err := <-done:
				t.Fatalf("click returned before load began: %v", err)
			case <-testContext(t).Done():
				t.Fatal("navigation did not begin")
			}
			observer := browserClient(t, endpoint)
			observed, err := observer.Page(testContext(t), id)
			if err != nil {
				t.Fatal(err)
			}
			if evalValue(t, observed, `new Promise(resolve=>requestAnimationFrame(()=>resolve(document.readyState)))`) == "complete" {
				t.Fatal("fixture failed to block document load")
			}
			select {
			case err := <-done:
				t.Fatalf("input returned while document load was blocked: %v", err)
			default:
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-testContext(t).Done():
				t.Fatal("input did not complete")
			}
		})
	}
}

func TestChromeFillAndPressInNestedRemoteFrame(t *testing.T) {
	fixture := observationFixture(t)
	endpoint := chrome(t, "about:blank")
	c := browserClient(t, endpoint)
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	ref := targetNamed(t, p, "Inner text")
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
	if _, err := p.Fill(testContext(t), ref, "remote value"); err != nil {
		t.Fatal(err)
	}
	c.Close()
	c = browserClient(t, endpoint)
	p, err = c.Page(testContext(t), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Control+A", "Backspace", "z"} {
		if _, err := p.Press(testContext(t), key); err != nil {
			t.Fatal(err)
		}
	}
	result, err := p.Controls(testContext(t), cdp.ControlsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, control := range result.Controls {
		if control.Name == "Inner text" && control.State.Value != nil && *control.State.Value == "z" {
			return
		}
	}
	t.Fatalf("native focus did not survive connection changes: %s", result)
}

func TestChromeSelectNativeValueAndLabel(t *testing.T) {
	fixture := actionFixture(t)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	ref := targetNamed(t, p, "Choice")
	if _, err := p.Eval(testContext(t), `document.body.dataset.events='';for(const type of ['input','change'])document.querySelector('select').addEventListener(type,e=>document.body.dataset.events+=e.type+',');void 0`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ input, value string }{{"b", "b"}, {"Alpha", "a"}} {
		if _, err := p.Select(testContext(t), ref, tc.input); err != nil {
			t.Fatalf("select %s: %v", tc.input, err)
		}
		if evalValue(t, p, `document.querySelector('select').value`) != tc.value {
			t.Fatal("wrong native selection")
		}
	}
	if evalValue(t, p, `document.body.dataset.events`) != "input,change,input,change," {
		t.Fatal("native select events missing")
	}
	for _, tc := range []struct{ input, code string }{{"Duplicate", "ambiguous"}, {"Disabled", "blocked"}, {"absent", "invalid_input"}} {
		_, err := p.Select(testContext(t), ref, tc.input)
		var e *cdp.Error
		if !errors.As(err, &e) || e.Code != tc.code {
			t.Fatalf("select %s: %v", tc.input, err)
		}
	}
	if evalValue(t, p, `document.querySelector('select').value`) != "a" {
		t.Fatal("rejected selection changed value")
	}
	if evalValue(t, p, `document.body.dataset.events`) != "input,change,input,change," {
		t.Fatal("rejected selection emitted events")
	}
}

func TestChromePressNativeEditingAndNavigationKeys(t *testing.T) {
	fixture := actionFixture(t)
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fill(testContext(t), targetNamed(t, p, "Text"), "query"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Control+A", "Backspace", "Shift+x", "Tab", "Enter", "Shift+Tab", "ArrowDown", "Escape"} {
		if _, err := p.Press(testContext(t), key); err != nil {
			t.Fatalf("press %s: %v", key, err)
		}
	}
	if evalValue(t, p, `document.querySelector('#text').value`) != "X" {
		t.Fatal("native editing keys did not replace selection")
	}
	if evalValue(t, p, `document.activeElement.id`) != "text" {
		t.Fatal("Tab/Shift+Tab did not move focus")
	}
	if evalValue(t, p, `document.querySelector('textarea').value`) != "\nold notes" {
		t.Fatal("Enter did not edit the focused textarea")
	}
	for _, key := range []string{"", "DefinitelyNotAKey", "Control+Control+A", "Unknown+A"} {
		_, err := p.Press(testContext(t), key)
		var e *cdp.Error
		if !errors.As(err, &e) || e.Code != "invalid_input" {
			t.Fatalf("invalid key %q: %v", key, err)
		}
	}
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
		{"Email", "new@example.test", `document.querySelector('[type=email]').value`},
		{"Number", "42.5", `document.querySelector('[type=number]').value`},
		{"Number", "", `document.querySelector('[type=number]').value`},
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
