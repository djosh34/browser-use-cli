package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type ReadResult struct {
	Page   PageInfo `json:"page"`
	Text   string   `json:"text"`
	Frames int      `json:"omittedFrames"`
}

func (r ReadResult) String() string {
	text := r.Page.String() + "\n\n" + r.Text
	if r.Frames > 0 {
		text += fmt.Sprintf("\n\n[%d child frames not read]", r.Frames)
	}
	return text
}

func (p *Page) Read(ctx context.Context) (ReadResult, error) {
	var r ReadResult
	err := p.Eval(ctx, `({page:{url:location.href,title:document.title},text:document.body?.innerText||'',omittedFrames:document.querySelectorAll('iframe,frame').length})`, &r)
	r.Page.ID = p.id
	return r, err
}

type Control struct {
	Target    string         `json:"target"`
	Role      string         `json:"role"`
	Name      string         `json:"name"`
	Source    string         `json:"source"`
	Offscreen bool           `json:"offscreen"`
	State     map[string]any `json:"state,omitempty"`
	Context   string         `json:"context,omitempty"`
	Href      string         `json:"href,omitempty"`
	Options   []Option       `json:"options,omitempty"`
}

type Option struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled,omitempty"`
}

type ControlsResult struct {
	Page       PageInfo       `json:"page"`
	Items      []Control      `json:"controls"`
	Candidates int            `json:"candidates"`
	Rejected   map[string]int `json:"rejected,omitempty"`
}

func (r ControlsResult) String() string {
	var b strings.Builder
	fmt.Fprintln(&b, r.Page.String())
	names := map[string]int{}
	for _, c := range r.Items {
		names[c.Name]++
	}
	for _, c := range r.Items {
		fmt.Fprintf(&b, "[%s] %s %q", c.Target, c.Role, c.Name)
		if c.Offscreen {
			fmt.Fprint(&b, " offscreen")
		}
		if c.Source == "dom" {
			fmt.Fprint(&b, " custom")
		}
		if len(c.State) > 0 {
			value, _ := json.Marshal(c.State)
			fmt.Fprintf(&b, " %s", value)
		}
		fmt.Fprintln(&b)
		if c.Name == "" || names[c.Name] > 1 {
			if c.Context != "" {
				fmt.Fprintf(&b, "  near: %s\n", c.Context)
			}
			if c.Href != "" {
				fmt.Fprintf(&b, "  href: %s\n", c.Href)
			}
		}
		if len(c.Options) > 0 {
			options, _ := json.Marshal(c.Options)
			fmt.Fprintf(&b, "  options: %s\n", options)
		}
	}
	fmt.Fprintf(&b, "%d controls from %d candidates", len(r.Items), r.Candidates)
	if len(r.Rejected) > 0 {
		value, _ := json.Marshal(r.Rejected)
		fmt.Fprintf(&b, "; rejected %s", value)
	}
	return b.String()
}

type axValue struct {
	Value any `json:"value"`
}
type axNode struct {
	Ignored    bool    `json:"ignored"`
	Backend    int     `json:"backendDOMNodeId"`
	Role       axValue `json:"role"`
	Name       axValue `json:"name"`
	Properties []struct {
		Name  string  `json:"name"`
		Value axValue `json:"value"`
	} `json:"properties"`
}

type probeResult struct {
	OK        bool     `json:"ok"`
	Reason    string   `json:"reason"`
	Tag       string   `json:"tag"`
	Name      string   `json:"name"`
	Offscreen bool     `json:"offscreen"`
	X         float64  `json:"x"`
	Y         float64  `json:"y"`
	Context   string   `json:"context"`
	Href      string   `json:"href"`
	Value     string   `json:"value"`
	Options   []Option `json:"options"`
	Redundant bool     `json:"redundant"`
}

// This deliberately inspects only the main document and its current layout.
const probeFunction = `function(scroll) {
 const e=this;
 const no=reason=>({ok:false,reason});
 if (!(e instanceof Element) || !e.isConnected) return no('detached');
 if (e===document.body || e===document.documentElement) return no('document container');
 if (e.matches(':disabled') || e.closest('[inert],[aria-disabled="true"]')) return no('disabled/inert');
 if (e.closest('[aria-hidden="true"]')) return no('aria-hidden');
 const style=getComputedStyle(e);
 if (!e.checkVisibility({checkOpacity:true,checkVisibilityCSS:true}) || style.visibility!=='visible') return no('hidden');
 if (style.pointerEvents==='none') return no('pointer-events');
 if (scroll) e.scrollIntoView({block:'center',inline:'center',behavior:'instant'});
 const rects=[...e.getClientRects()].filter(r=>r.width>0&&r.height>0);
 if (!rects.length) return no('no rectangle');
 const intersect=rects.map(r=>({left:Math.max(0,r.left),top:Math.max(0,r.top),right:Math.min(innerWidth,r.right),bottom:Math.min(innerHeight,r.bottom)})).filter(r=>r.right>r.left&&r.bottom>r.top);
 const offscreen=!intersect.length;
 if (scroll && offscreen) return no('unreachable');
 let x=0,y=0;
 if (!offscreen) {
   let hit=false;
   for (const r of intersect) {
     const px=(r.left+r.right)/2,py=(r.top+r.bottom)/2;
     const top=document.elementFromPoint(px,py);
     if (top && (top===e || e.contains(top))) { x=px;y=py;hit=true;break; }
   }
   if (!hit) return no('covered/clipped');
 }
 const name=(e.getAttribute('aria-label')||e.innerText||e.getAttribute('title')||e.getAttribute('alt')||e.getAttribute('placeholder')||'').replace(/\s+/g,' ').trim().slice(0,150);
 const context=(e.closest('p,li,td,article,section')?.innerText||e.parentElement?.innerText||'').replace(/\s+/g,' ').trim().slice(0,220);
 const href=e.closest('a')?.href||'';
 const value=e.type==='password'?'':e.value||'';
 const options=e.tagName==='SELECT'?[...e.options].map(o=>({value:o.value,label:o.text,disabled:o.disabled||!!o.closest('optgroup[disabled]')})):[];
 const redundant=e.tagName==='FORM'||!!(e.tagName==='LABEL'&&e.control)||!!e.parentElement?.closest('a[href],button,[role="button"],[role="link"]');
 return {ok:true,tag:e.tagName.toLowerCase(),name,offscreen,x,y,context,href,value,options,redundant};
}`

func (p *Page) Controls(ctx context.Context, verbose bool) (ControlsResult, error) {
	state, err := p.State(ctx)
	if err != nil {
		return ControlsResult{}, err
	}
	loader, err := p.document(ctx)
	if err != nil {
		return ControlsResult{}, err
	}
	result := ControlsResult{Page: state.Page, Items: []Control{}, Rejected: map[string]int{}}
	var tree struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := p.call(ctx, "Accessibility.getFullAXTree", map[string]any{}, &tree); err != nil {
		return result, err
	}
	roles := map[string]bool{}
	for _, s := range strings.Fields("button link textbox searchbox checkbox radio combobox listbox option slider spinbutton switch tab menuitem menuitemcheckbox menuitemradio treeitem DisclosureTriangle") {
		roles[s] = true
	}
	type candidate struct {
		node               int
		role, name, source string
		state              map[string]any
	}
	candidates := []candidate{}
	seen := map[int]bool{}
	for _, n := range tree.Nodes {
		role, _ := n.Role.Value.(string)
		name, _ := n.Name.Value.(string)
		if n.Ignored || n.Backend == 0 || !roles[role] {
			continue
		}
		props := map[string]any{}
		for _, prop := range n.Properties {
			switch prop.Name {
			case "checked", "expanded", "selected", "required", "disabled", "readonly":
				props[prop.Name] = prop.Value.Value
			}
		}
		candidates = append(candidates, candidate{n.Backend, role, name, "accessibility", props})
		seen[n.Backend] = true
	}
	if verbose {
		var snapshot struct {
			Documents []struct {
				Nodes struct {
					Backend   []int `json:"backendNodeId"`
					Clickable struct {
						Index []int `json:"index"`
					} `json:"isClickable"`
				} `json:"nodes"`
			} `json:"documents"`
		}
		if err := p.call(ctx, "DOMSnapshot.captureSnapshot", map[string]any{"computedStyles": []string{}}, &snapshot); err != nil {
			return result, err
		}
		if len(snapshot.Documents) > 0 {
			nodes := snapshot.Documents[0].Nodes
			for _, index := range nodes.Clickable.Index {
				id := nodes.Backend[index]
				if seen[id] {
					continue
				}
				seen[id] = true
				candidates = append(candidates, candidate{node: id, source: "dom"})
			}
		}
	}
	result.Candidates = len(candidates)
	for _, c := range candidates {
		object, err := p.object(ctx, c.node)
		if err != nil {
			result.Rejected["unresolved"]++
			continue
		}
		var probe probeResult
		if err := p.invoke(ctx, object, probeFunction, []any{false}, &probe); err != nil {
			return result, err
		}
		if !probe.OK {
			result.Rejected[probe.Reason]++
			continue
		}
		if c.name == "" {
			c.name = probe.Name
		}
		if c.role == "" {
			c.role = probe.Tag
		}
		if c.source == "dom" && probe.Redundant {
			result.Rejected["duplicate/container"]++
			continue
		}
		if probe.Value != "" && (c.role == "textbox" || c.role == "combobox" || c.role == "searchbox") {
			if c.state == nil {
				c.state = map[string]any{}
			}
			c.state["value"] = probe.Value
		}
		result.Items = append(result.Items, Control{Target: fmt.Sprintf("%s:%s:%d", p.id, loader, c.node), Role: c.role, Name: c.name, Source: c.source, Offscreen: probe.Offscreen, State: c.state, Context: probe.Context, Href: probe.Href, Options: probe.Options})
	}
	return result, nil
}

func TargetPage(target string) (string, error) {
	parts := strings.Split(target, ":")
	if len(parts) != 3 {
		return "", errors.New("expected a target copied from controls")
	}
	return parts[0], nil
}

func (p *Page) target(ctx context.Context, target string) (object string, probe probeResult, err error) {
	parts := strings.Split(target, ":")
	if len(parts) != 3 || parts[0] != p.id {
		return "", probe, errors.New("target belongs to a different page or has an invalid format")
	}
	loader, err := p.document(ctx)
	if err != nil {
		return "", probe, err
	}
	if loader != parts[1] {
		return "", probe, errors.New("stale target: the page navigated; run controls again")
	}
	node, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", probe, err
	}
	object, err = p.object(ctx, node)
	if err != nil {
		return "", probe, fmt.Errorf("stale target: %w", err)
	}
	if err := p.invoke(ctx, object, probeFunction, []any{true}, &probe); err != nil {
		return "", probe, err
	}
	if !probe.OK {
		return "", probe, fmt.Errorf("target is not actionable: %s", probe.Reason)
	}
	return object, probe, nil
}

func (p *Page) Click(ctx context.Context, target string) error {
	_, probe, err := p.target(ctx, target)
	if err != nil {
		return err
	}
	for _, kind := range []string{"mousePressed", "mouseReleased"} {
		if err := p.call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": kind, "x": probe.X, "y": probe.Y, "button": "left", "clickCount": 1}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (p *Page) Fill(ctx context.Context, target, text string) error {
	object, _, err := p.target(ctx, target)
	if err != nil {
		return err
	}
	const focus = `function(){if(this.readOnly)throw Error('readonly');this.focus();if(this instanceof HTMLInputElement||this instanceof HTMLTextAreaElement)this.select();else if(this.isContentEditable){const r=document.createRange();r.selectNodeContents(this);const s=getSelection();s.removeAllRanges();s.addRange(r)}else throw Error('not an editable field');return true}`
	if err := p.invoke(ctx, object, focus, nil, nil); err != nil {
		return err
	}
	return p.call(ctx, "Input.insertText", map[string]any{"text": text}, nil)
}

func (p *Page) Press(ctx context.Context, key string) error {
	codes := map[string]int{"Enter": 13, "Tab": 9, "Escape": 27, "ArrowDown": 40, "ArrowUp": 38, "ArrowLeft": 37, "ArrowRight": 39, "Backspace": 8, "Space": 32}
	code, ok := codes[key]
	if !ok {
		return errors.New("prototype supports Enter, Tab, Escape, arrows, Backspace, and Space")
	}
	for _, kind := range []string{"keyDown", "keyUp"} {
		params := map[string]any{"type": kind, "key": key, "code": key, "windowsVirtualKeyCode": code}
		if kind == "keyDown" && key == "Enter" {
			params["text"] = "\r"
		}
		if key == "Space" {
			params["key"] = " "
			if kind == "keyDown" {
				params["text"] = " "
			}
		}
		if err := p.call(ctx, "Input.dispatchKeyEvent", params, nil); err != nil {
			return err
		}
	}
	return nil
}

func (p *Page) Select(ctx context.Context, target, value string) error {
	object, _, err := p.target(ctx, target)
	if err != nil {
		return err
	}
	return p.invoke(ctx, object, `function(value){if(this.tagName!=='SELECT')throw Error('not a native select');const matches=[...this.options].filter(o=>o.value===value||o.text.trim()===value);if(matches.length!==1)throw Error('option missing or ambiguous');if(matches[0].disabled||matches[0].closest('optgroup[disabled]'))throw Error('disabled option');this.value=matches[0].value;this.dispatchEvent(new Event('input',{bubbles:true}));this.dispatchEvent(new Event('change',{bubbles:true}));return this.value}`, []any{value}, nil)
}
