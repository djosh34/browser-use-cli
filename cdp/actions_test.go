//go:build integration

package cdp_test

import (
	"context"
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
func targetNamed(t *testing.T, p *cdp.Page, name string) cdp.ControlID {
	t.Helper()
	result, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, control := range controlNodes(result.Tree) {
		if strings.TrimSpace(control.Name) == name {
			return control.ID
		}
	}
	t.Fatalf("control %q missing: %s", name, result)
	return 0
}
func controlNodes(root *cdp.Node) []*cdp.Node {
	var nodes []*cdp.Node
	var walk func(*cdp.Node)
	walk = func(node *cdp.Node) {
		if node == nil {
			return
		}
		if node.ID != 0 {
			nodes = append(nodes, node)
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(root)
	return nodes
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
func TestChromeControlOrdinalsResolveFreshlyAfterReordering(t *testing.T) {
	fixture := actionFixture(t)
	endpoint := chrome(t, "about:blank")
	c := browserClient(t, endpoint)
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	id := targetNamed(t, p, "Hit")
	if id != 1 {
		t.Fatalf("first control ID = %d", id)
	}
	if _, err := p.Eval(testContext(t), `document.body.insertAdjacentHTML('afterbegin','<button onclick="document.title=\'New first\'">New first</button>')`); err != nil {
		t.Fatal(err)
	}
	c.Close()
	c = browserClient(t, endpoint)
	p, err = c.Page(testContext(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Click(testContext(t), id)
	if err != nil || result.Page.Title != "New first" || result.Action != "click" || result.Target != 1 {
		t.Fatalf("fresh ordinal 1 must bind new first control: %+v %v", result, err)
	}
	if targetNamed(t, p, "Hit") != 2 {
		t.Fatal("controls were not renumbered")
	}
}

func TestChromeInputSurfacesUnrelatedChildCaptureWarning(t *testing.T) {
	for _, method := range []string{"Accessibility.getFullAXTree", "Target.attachToTarget"} {
		t.Run(method, func(t *testing.T) {
			fixture := observationFixture(t)
			endpoint := chrome(t, "about:blank")
			owner := browserClient(t, endpoint)
			observed, err := owner.Open(testContext(t), fixture.URL)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			c := browserClient(t, axFaultEndpoint(t, endpoint, func(current string, _ json.RawMessage) bool {
				if current != method {
					return false
				}
				calls++
				return calls == 2
			}))
			p, err := c.Page(testContext(t), 1)
			if err != nil {
				t.Fatal(err)
			}
			result, err := p.Click(testContext(t), 1)
			if err != nil || result.Action != "click" || len(result.Warnings) == 0 {
				t.Fatalf("unrelated child failure prevented input or lost warning: %+v %v", result, err)
			}
			read, err := observed.Read(testContext(t), cdp.ReadOptions{})
			if err != nil || !strings.Contains(read.String(), "Root button!") {
				t.Fatalf("available target was not clicked: %s %v", read, err)
			}
		})
	}
}

func TestChromeUnavailableControlReportsPartialDiscovery(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Partial lookup</title><button disabled>Available</button><iframe srcdoc="<button>Child</button>"></iframe>`)
	}))
	defer fixture.Close()
	endpoint := chrome(t, "about:blank")
	owner := browserClient(t, endpoint)
	if _, err := owner.Open(testContext(t), fixture.URL); err != nil {
		t.Fatal(err)
	}
	captures := 0
	c := browserClient(t, axFaultEndpoint(t, endpoint, func(method string, _ json.RawMessage) bool {
		if method != "Accessibility.getFullAXTree" {
			return false
		}
		captures++
		return captures%2 == 0
	}))
	p, err := c.Page(testContext(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	read, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil || read.Complete || len(read.Warnings) == 0 {
		t.Fatalf("fixture did not produce partial discovery: %s %v", read, err)
	}
	for _, action := range []struct {
		name string
		call func(cdp.ControlID) (cdp.ActionResult, error)
	}{
		{"click", func(id cdp.ControlID) (cdp.ActionResult, error) { return p.Click(testContext(t), id) }},
		{"fill", func(id cdp.ControlID) (cdp.ActionResult, error) { return p.Fill(testContext(t), id, "private input") }},
		{"select", func(id cdp.ControlID) (cdp.ActionResult, error) {
			return p.Select(testContext(t), id, "private option")
		}},
	} {
		for _, target := range []struct {
			id   cdp.ControlID
			code string
		}{{2, "unavailable"}, {1, "blocked"}} {
			_, err := action.call(target.id)
			var typed *cdp.Error
			if !errors.As(err, &typed) || typed.Code != target.code || !strings.Contains(typed.Message, "incomplete") || !strings.Contains(typed.Message, read.Warnings[0]) || strings.Contains(typed.Message, "private") {
				t.Errorf("%s %d discarded partial discovery warning or changed failure: %v", action.name, target.id, err)
			}
		}
	}
}

func TestChromePartialInputPreservesCancellation(t *testing.T) {
	fixture := observationFixture(t)
	endpoint := chrome(t, "about:blank")
	owner := browserClient(t, endpoint)
	if _, err := owner.Open(testContext(t), fixture.URL); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	captures := 0
	c := browserClient(t, axFaultEndpoint(t, endpoint, func(method string, _ json.RawMessage) bool {
		if method == "Accessibility.getFullAXTree" {
			captures++
			return captures == 2
		}
		return false
	}, func(method string) {
		if method == "DOM.resolveNode" {
			cancel()
		}
	}))
	p, err := c.Page(testContext(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Click(ctx, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("partial warning replaced cancellation: %v", err)
	}
}

func TestChromeInputDoesNotRetargetDisappearingNativeNode(t *testing.T) {
	for _, kind := range []string{"click", "fill", "select"} {
		t.Run(kind, func(t *testing.T) {
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `<title>Untouched</title><button onmouseenter="replace(this)" onclick="document.title='Wrong button'">Button</button><input aria-label="Field" onfocus="replace(this)"><select aria-label="Choice" onfocus="replace(this)"><option>A</option><option>B</option></select><script>function replace(el){const replacement=el.cloneNode(true);replacement.removeAttribute('onfocus');replacement.removeAttribute('onmouseenter');el.replaceWith(replacement);if(replacement.tagName!=='BUTTON')replacement.focus()}</script>`)
			}))
			defer fixture.Close()
			c := browserClient(t, chrome(t, "about:blank"))
			p, err := c.Open(testContext(t), fixture.URL)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "click":
				_, err = p.Click(testContext(t), targetNamed(t, p, "Button"))
			case "fill":
				_, err = p.Fill(testContext(t), targetNamed(t, p, "Field"), "must not leak")
			case "select":
				_, err = p.Select(testContext(t), targetNamed(t, p, "Choice"), "B")
			}
			var typed *cdp.Error
			if !errors.As(err, &typed) || typed.Code != "stale" {
				t.Fatalf("disappearance must fail, not retarget: %v", err)
			}
			if got := evalValue(t, p, `document.title+'|'+document.querySelector('input').value+'|'+document.querySelector('select').value`); got != "Untouched||A" {
				t.Fatalf("replacement received input: %s", got)
			}
		})
	}
}

func TestChromeClickTransparentNativeRadio(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Radio</title><input id="radio" type="radio" style="opacity:0;width:48px;height:48px" onchange="document.title=String(event.isTrusted)"><label for="radio">Nonstop only</label>`)
	}))
	defer fixture.Close()
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Click(testContext(t), targetNamed(t, p, "Nonstop only"))
	if err != nil || result.Page.Title != "true" {
		t.Fatalf("transparent native hit target rejected: %s %v", result, err)
	}
	read, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	for _, n := range controlNodes(read.Tree) {
		if n.Name == "Nonstop only" && n.State["checked"] == true {
			checked = true
		}
	}
	if !checked {
		t.Fatalf("native radio was not checked: %s", read)
	}
	for _, setup := range []string{
		`document.querySelector('input').disabled=true`,
		`document.querySelector('input').disabled=false;document.body.insertAdjacentHTML('beforeend','<div style="position:fixed;inset:0;background:white"></div>')`,
	} {
		if _, err := p.Eval(testContext(t), setup+`;void 0`); err != nil {
			t.Fatal(err)
		}
		_, err := p.Click(testContext(t), targetNamed(t, p, "Nonstop only"))
		var typed *cdp.Error
		if !errors.As(err, &typed) || typed.Code != "blocked" {
			t.Fatalf("transparent control lost disabled/covered guard: %v", err)
		}
	}
}

func TestChromeClickAfterScrollingViewportOverflowBody(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Before</title><style>body{margin:0;height:100vh;overflow-y:scroll}main{height:1500px}button{margin-top:1100px}</style><main><button onclick="document.title='Clicked'">Reject optional</button></main>`)
	}))
	defer fixture.Close()
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Click(testContext(t), targetNamed(t, p, "Reject optional"))
	if err != nil || result.Page.Title != "Clicked" {
		t.Fatalf("viewport-propagated body overflow blocked visible target after scroll: %s %v", result, err)
	}
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
	if _, err := p.Click(testContext(t), ref); err != nil {
		t.Fatalf("frame navigation: %v", err)
	}
	read, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil || !strings.Contains(read.String(), "Inner destination") {
		t.Fatalf("frame destination: %s %v", read, err)
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
	if len(result.NewPages) != 1 || result.NewPages[0].ID == result.Page.ID || result.NewPages[0].URL != fixture.URL+"/popup" {
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

func TestChromeNewPagesUsesNativeIdentityNotReusedOrdinal(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<title>Popup identity</title><button onclick="window.other=window.open('/old')">Open tab</button><button onclick="other.close();window.open('/new')">Replace tab</button>`)
	}))
	defer fixture.Close()
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(testContext(t), targetNamed(t, p, "Open tab")); err != nil {
		t.Fatal(err)
	}
	before, err := c.Pages(testContext(t))
	if err != nil || len(before.Pages) != 2 {
		t.Fatalf("fixture pages: %+v %v", before, err)
	}
	result, err := p.Click(testContext(t), targetNamed(t, p, "Replace tab"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.NewPages) != 1 || result.NewPages[0].URL != fixture.URL+"/new" {
		t.Fatalf("replacement tab lost when ordinals reused: %+v", result)
	}
	after, err := c.Pages(testContext(t))
	if err != nil || len(after.Pages) != 2 {
		t.Fatalf("fixture pages after replacement: %+v %v", after, err)
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

func TestChromePressDoesNotAdoptForegroundRefresh(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("action_replaces_refresh=%t", replacement), func(t *testing.T) {
			started, held, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var startOnce, heldOnce, releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/":
					fmt.Fprint(w, `<title>Before</title><a href="about:blank" target="_blank">Background tab</a><iframe src="/frame"></iframe><script>document.onkeydown=e=>{e.preventDefault();document.title='Typed'};document.onvisibilitychange=()=>{if(!document.hidden)document.querySelector('iframe').src='/refresh'}</script>`)
				case "/frame":
					fmt.Fprint(w, `<p>Initial frame</p>`)
				case "/refresh":
					startOnce.Do(func() { close(started) })
					select {
					case <-release:
					case <-r.Context().Done():
					}
					fmt.Fprint(w, `<p>Background refresh</p>`)
				case "/action":
					fmt.Fprint(w, `<p>Action frame</p><img src="/held">`)
				case "/held":
					heldOnce.Do(func() { close(held) })
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
			ref := targetNamed(t, p, "Background tab")
			if _, err := p.Click(testContext(t), ref); err != nil {
				t.Fatal(err)
			}
			info, err := p.Info(testContext(t))
			if err != nil {
				t.Fatal(err)
			}
			id := info.ID
			if evalValue(t, p, `String(document.hidden)`) != "true" {
				t.Fatal("fixture did not background the original tab")
			}
			if replacement {
				if _, err := p.Eval(testContext(t), `document.onkeydown=e=>{e.preventDefault();document.title='Typed';document.querySelector('iframe').src='/action'};void 0`); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() {
				result, err := p.Press(testContext(t), "Enter")
				if err == nil && result.Page.Title != "Typed" {
					err = fmt.Errorf("wrong page: %s", result)
				}
				done <- err
			}()
			if replacement {
				select {
				case <-held:
				case err := <-done:
					t.Fatalf("action navigation returned before held load: %v", err)
				case <-testContext(t).Done():
					t.Fatal("action navigation did not start")
				}
				observer := browserClient(t, endpoint)
				observed, err := observer.Page(testContext(t), id)
				if err != nil {
					t.Fatal(err)
				}
				if evalValue(t, observed, `new Promise(resolve=>requestAnimationFrame(()=>resolve(document.querySelector('iframe').contentDocument.readyState)))`) == "complete" {
					t.Fatal("fixture failed to hold action load")
				}
				select {
				case err := <-done:
					t.Fatalf("new action navigation mistaken for old refresh: %v", err)
				default:
				}
				releaseOnce.Do(func() { close(release) })
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("foreground refresh completion: %v", err)
				}
			case <-testContext(t).Done():
				t.Fatal("foreground refresh delayed keyboard input")
			}
			select {
			case <-started:
			default:
				t.Fatal("foreground refresh did not begin")
			}
		})
	}
}

func TestChromeInputsWaitForTriggeredDocumentLoad(t *testing.T) {
	for _, tc := range []struct{ kind, target string }{{"click", "Navigate"}, {"raf", "Frame navigation"}, {"fill", "Auto"}, {"press", "Query"}, {"select", "Route"}, {"raf-select", "Frame route"}} {
		t.Run(tc.kind, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var startOnce, releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/":
					fmt.Fprint(w, `<title>Before</title><a href="/destination">Navigate</a><button onclick="requestAnimationFrame(()=>location.href='/destination')">Frame navigation</button><input aria-label="Auto" oninput="location.href='/destination'"><form action="/destination"><input aria-label="Query"></form><select aria-label="Route" onchange="location.href=this.value"><option value="">Stay</option><option value="/destination">Go</option></select><select aria-label="Frame route" onchange="requestAnimationFrame(()=>location.href=this.value)"><option value="">Stay</option><option value="/destination">Go</option></select>`)
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
			info, err := p.Info(testContext(t))
			id := info.ID
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
				case "click", "raf":
					result, err = p.Click(testContext(t), ref)
				case "fill":
					result, err = p.Fill(testContext(t), ref, "place")
				case "press":
					result, err = p.Press(testContext(t), "Enter")
				case "select", "raf-select":
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
	info, err := p.Info(testContext(t))
	id := info.ID
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
	result, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, control := range controlNodes(result.Tree) {
		if control.Name == "Inner text" && control.Value != nil && *control.Value == "z" {
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
	controls, err := p.Read(testContext(t), cdp.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	for _, name := range []string{"Nested inner button", "Cross origin button", "Same origin button", "Open shadow button", "Closed shadow button", "Root button"} {
		var ref cdp.ControlID
		for _, control := range controlNodes(controls.Tree) {
			if control.Name == name {
				ref = control.ID
			}
		}
		if ref == 0 {
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
	info, err := p.Info(testContext(t))
	id := info.ID
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
	if _, err := p.Click(testContext(t), ref); err != nil {
		t.Fatalf("fresh action did not resolve replacement ordinal: %v", err)
	}
	if err := p.Navigate(testContext(t), fixture.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Click(testContext(t), ref); err != nil {
		t.Fatalf("fresh action did not resolve new document ordinal: %v", err)
	}
	for _, invalid := range []cdp.ControlID{0, -1} {
		if _, err := p.Click(testContext(t), invalid); !errors.As(err, &e) || e.Code != "invalid_input" {
			t.Fatalf("invalid ordinal: %v", err)
		}
	}
}
