package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ActionResult reports completed browser input, not application business success.
// NewPages contains newly observed tabs; it does not change a saved selection.
type ActionResult struct {
	Action   string     `json:"action"`
	Page     PageInfo   `json:"page"`
	Target   ControlID  `json:"target,omitempty"`
	NewPages []PageInfo `json:"newPages"`
	Warnings []string   `json:"warnings,omitempty"`
}

func (r ActionResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\t%s", r.Action, r.Page)
	if r.Target != 0 {
		fmt.Fprintf(&b, "\t[%d]", r.Target)
	}
	for _, warning := range r.Warnings {
		fmt.Fprintf(&b, "\nWarning: %s", warning)
	}
	for _, page := range r.NewPages {
		fmt.Fprintf(&b, "\nOpened: %s", page)
	}
	return b.String()
}

type resolvedTarget struct {
	doc     documentCapture
	chain   []documentCapture
	backend int64
	object  string
}
type point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

func (p *Page) resolveTarget(ctx context.Context, binding controlTarget) (resolvedTarget, error) {
	target := resolvedTarget{doc: binding.doc, chain: binding.chain, backend: binding.backend}
	if target.backend == 0 {
		return target, failure("unsupported", "control has no native input target")
	}
	if err := p.validateDocuments(ctx, target.chain); err != nil {
		return target, err
	}
	var err error
	target.object, err = p.resolveNode(ctx, target.doc, target.backend)
	return target, err
}
func (p *Page) resolveNode(ctx context.Context, doc documentCapture, backend int64) (string, error) {
	world, err := p.isolatedWorld(ctx, doc)
	if err != nil {
		return "", err
	}
	var result struct {
		Object remoteObject `json:"object"`
	}
	err = p.client.call(ctx, doc.session, "DOM.resolveNode", map[string]any{"backendNodeId": backend, "executionContextId": world, "objectGroup": "browser-use-cli-action"}, &result)
	if err != nil {
		return "", err
	}
	if result.Object.ID == "" {
		return "", failure("stale", "control node could not be resolved")
	}
	return result.Object.ID, nil
}
func (p *Page) nodeCall(ctx context.Context, doc documentCapture, object, function string, args []any, out any) error {
	arguments := make([]map[string]any, len(args))
	for i, arg := range args {
		arguments[i] = map[string]any{"value": arg}
	}
	var result runtimeResult
	if err := p.client.call(ctx, doc.session, "Runtime.callFunctionOn", map[string]any{"objectId": object, "functionDeclaration": function, "arguments": arguments, "returnByValue": true}, &result); err != nil {
		return err
	}
	if result.Exception != nil {
		return failure("stale", "target context changed while checking the control")
	}
	if out != nil && json.Unmarshal(result.Result.Value, out) != nil {
		return failure("protocol", "invalid control check result")
	}
	return nil
}
func (p *Page) releaseTargets(ctx context.Context, chain []documentCapture) {
	seen := map[string]bool{}
	for _, doc := range chain {
		if !seen[doc.session] {
			seen[doc.session] = true
			p.client.call(ctx, doc.session, "Runtime.releaseObjectGroup", map[string]string{"objectGroup": "browser-use-cli-action"}, nil)
		}
	}
}
func targetStatus(status string) error {
	if status == "" {
		return nil
	}
	if status == "stale" {
		return failure("stale", "control node is detached or in another document")
	}
	return failure("blocked", "control is hidden, disabled, covered, or has no supported input point")
}

// Click resolves the current control ordinal, scrolls its native target and
// ancestor frames, then hit-tests before one native mouse press/release pair.
func (p *Page) Click(ctx context.Context, id ControlID) (ActionResult, error) {
	if id <= 0 {
		return ActionResult{}, failure("invalid_input", "control ID must be positive")
	}
	if err := p.lock(ctx); err != nil {
		return ActionResult{}, err
	}
	defer p.unlock()
	if err := p.attach(ctx); err != nil {
		return ActionResult{}, err
	}
	observation, err := p.beginInput(ctx, "click", id)
	if err != nil {
		return ActionResult{}, err
	}
	defer p.endInput(observation)
	target, err := p.resolveTarget(ctx, observation.target)
	defer p.releaseTargets(ctx, target.chain)
	if err != nil {
		return ActionResult{}, err
	}
	point, err := p.inputPoint(ctx, target)
	if err != nil {
		return ActionResult{}, err
	}
	if err := observation.startInput(ctx); err != nil {
		return ActionResult{}, err
	}
	if err := p.client.call(ctx, target.doc.session, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": point.X, "y": point.Y, "button": "none"}, nil); err != nil {
		return ActionResult{}, err
	}
	// Hover may expose an overlay, replace the node, or move it. Recheck,
	// but never chase a moving target or repeat a mouse press.
	verified, err := p.inputPoint(ctx, target)
	if err != nil {
		return ActionResult{}, err
	}
	if verified != point {
		return ActionResult{}, failure("blocked", "control moved after pointer hover; observe it again")
	}
	for _, kind := range []string{"mousePressed", "mouseReleased"} {
		if err := p.client.call(ctx, target.doc.session, "Input.dispatchMouseEvent", map[string]any{"type": kind, "x": point.X, "y": point.Y, "button": "left", "clickCount": 1}, nil); err != nil {
			return ActionResult{}, err
		}
	}
	return p.completeInput(ctx, observation, target.doc)
}
func (p *Page) actionResult(ctx context.Context, a *inputObservation) (ActionResult, error) {
	after, err := p.client.pageTargets(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	old := map[string]bool{}
	for _, page := range a.before {
		old[page.ID] = true
	}
	result := ActionResult{Action: a.action, Target: a.id, NewPages: []PageInfo{}, Warnings: a.warnings}
	for i, page := range after {
		info := page.info(PageID(i + 1))
		if page.ID == p.id {
			result.Page = info
		}
		if !old[page.ID] {
			result.NewPages = append(result.NewPages, info)
		}
	}
	if result.Page.ID == 0 {
		return ActionResult{}, failure("page", "the selected page is unavailable or ineligible")
	}
	return result, nil
}

func (p *Page) inputPoint(ctx context.Context, target resolvedTarget) (point, error) {
	if err := p.client.call(ctx, p.state.session, "Page.bringToFront", nil, nil); err != nil {
		return point{}, err
	}
	type owner struct {
		doc     documentCapture
		object  string
		backend int64
	}
	owners := []owner{}
	for i := 1; i < len(target.chain); i++ {
		parent, child := target.chain[i-1], target.chain[i]
		var result struct {
			Backend int64 `json:"backendNodeId"`
		}
		if err := p.client.call(ctx, parent.session, "DOM.getFrameOwner", map[string]string{"frameId": child.frame.ID}, &result); err != nil {
			return point{}, err
		}
		object, err := p.resolveNode(ctx, parent, result.Backend)
		if err != nil {
			return point{}, err
		}
		owners = append(owners, owner{parent, object, result.Backend})
		if err := p.client.call(ctx, parent.session, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": result.Backend}, nil); err != nil {
			return point{}, err
		}
	}
	var state struct {
		Status string `json:"status"`
	}
	if err := p.nodeCall(ctx, target.doc, target.object, "function(){"+inputHelpers+"return {status:state(this)}}", nil, &state); err != nil {
		return point{}, err
	}
	if err := targetStatus(state.Status); err != nil {
		return point{}, err
	}
	if err := p.client.call(ctx, target.doc.session, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": target.backend}, nil); err != nil {
		return point{}, err
	}
	var candidates struct {
		Status string  `json:"status"`
		Points []point `json:"points"`
	}
	if err := p.nodeCall(ctx, target.doc, target.object, clickPoints, nil, &candidates); err != nil {
		return point{}, err
	}
	if err := targetStatus(candidates.Status); err != nil {
		return point{}, err
	}
	for _, candidate := range candidates.Points {
		current := candidate
		// Input coordinates belong to the owning session's root widget, not
		// necessarily the tab's main frame. Still hit-test every ancestor.
		dispatch := candidate
		valid := true
		for i := len(owners) - 1; i >= 0; i-- {
			owner := owners[i]
			var result struct {
				Status string `json:"status"`
				Point  point  `json:"point"`
			}
			if err := p.nodeCall(ctx, owner.doc, owner.object, framePoint, []any{current}, &result); err != nil {
				return point{}, err
			}
			if result.Status != "" {
				valid = false
				break
			}
			current = result.Point
			if owner.doc.session == target.doc.session {
				dispatch = current
			}
		}
		if valid {
			if err := p.validateDocuments(ctx, target.chain); err != nil {
				return point{}, failure("stale", "control document changed before input")
			}
			return dispatch, nil
		}
	}
	return point{}, failure("blocked", "control has no uncovered input point through its frame chain")
}

const inputHelpers = `
 function state(el){
  if (!(el instanceof Element) || !el.isConnected || el.ownerDocument!==document) return 'stale';
  if (el.matches(':disabled') || getComputedStyle(el).visibility!=='visible') return 'blocked';
  for(let n=el;n;n=n.parentElement || n.getRootNode().host){
   const s=getComputedStyle(n);
   if(n.inert || n.getAttribute('aria-disabled')==='true' || s.display==='none' || s.opacity==='0' || s.contentVisibility==='hidden') return 'blocked';
  }
  return '';
 }
 function hit(el,x,y){
  let node=el;
  while(node){
   const root=node.getRootNode(), found=root.elementFromPoint(x,y);
   if(!found || (found!==node && !node.contains(found))) return false;
   if(root instanceof ShadowRoot) node=root.host; else return true;
  }
  return false;
 }
`
const clickPoints = `function(){` + inputHelpers + `
 const status=state(this);if(status) return {status,points:[]};
 const clip={left:0,top:0,right:innerWidth,bottom:innerHeight};
 const rootStyle=getComputedStyle(document.documentElement);
 for(let n=this.parentElement || this.getRootNode().host;n;n=n.parentElement || n.getRootNode().host){
  // Root overflow (and body overflow propagated through a visible root)
  // clips at the viewport, not at its document box shifted by window scroll.
  if(n===document.documentElement || n===document.body && rootStyle.overflowX==='visible' && rootStyle.overflowY==='visible') continue;
  if(!n.getClientRects().length) continue;
  const style=getComputedStyle(n),r=n.getBoundingClientRect(),sx=n.offsetWidth?r.width/n.offsetWidth:1,sy=n.offsetHeight?r.height/n.offsetHeight:1;
  const left=r.left+n.clientLeft*sx,top=r.top+n.clientTop*sy;
  if(style.overflowX!=='visible'){clip.left=Math.max(clip.left,left);clip.right=Math.min(clip.right,left+n.clientWidth*sx)}
  if(style.overflowY!=='visible'){clip.top=Math.max(clip.top,top);clip.bottom=Math.min(clip.bottom,top+n.clientHeight*sy)}
 }
 const points=[];
 for(const r of this.getClientRects()){
  const left=Math.max(clip.left,r.left),top=Math.max(clip.top,r.top),right=Math.min(clip.right,r.right),bottom=Math.min(clip.bottom,r.bottom);
  if(right<=left || bottom<=top) continue;
  const dx=Math.min(2,(right-left)/4),dy=Math.min(2,(bottom-top)/4);
  for(const [x,y] of [[(left+right)/2,(top+bottom)/2],[left+dx,top+dy],[right-dx,top+dy],[left+dx,bottom-dy],[right-dx,bottom-dy]]){
   if(hit(this,x,y)) points.push({x,y});
  }
 }
 return {status:'',points};
}`
const framePoint = `function(point){` + inputHelpers + `
 const status=state(this);if(status) return {status};
 for(let n=this;n;n=n.parentElement || n.getRootNode().host){
  const style=getComputedStyle(n),matrix=new DOMMatrix(style.transform==='none'?undefined:style.transform);
  if(!matrix.is2D || Math.abs(matrix.b)>0.00001 || Math.abs(matrix.c)>0.00001 || matrix.a<=0 || matrix.d<=0) return {status:'blocked'};
 }
 const r=this.getBoundingClientRect();
 if(!this.offsetWidth || !this.offsetHeight || point.x<0 || point.y<0 || point.x>=this.clientWidth || point.y>=this.clientHeight) return {status:'blocked'};
 const x=r.left+(this.clientLeft+point.x)*r.width/this.offsetWidth,y=r.top+(this.clientTop+point.y)*r.height/this.offsetHeight;
 return hit(this,x,y)?{status:'',point:{x,y}}:{status:'blocked'};
}`
