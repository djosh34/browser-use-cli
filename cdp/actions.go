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

// Calls stay in the bound node's isolated world. Object arguments preserve native
// identity (not a selector or an ordinal to resolve again). No call is replayed.
func (p *Page) callTarget(ctx context.Context, target resolvedTarget, function string, args []any, gesture bool) (remoteObject, error) {
	arguments := make([]map[string]any, len(args))
	for i, arg := range args {
		if object, ok := arg.(remoteObject); ok {
			arguments[i] = map[string]any{"objectId": object.ID}
		} else {
			arguments[i] = map[string]any{"value": arg}
		}
	}
	var result runtimeResult
	if err := p.client.call(ctx, target.doc.session, "Runtime.callFunctionOn", map[string]any{
		"objectId": target.object, "functionDeclaration": function, "arguments": arguments,
		"userGesture": gesture, "objectGroup": "browser-use-cli-action",
	}, &result); err != nil {
		return remoteObject{}, err
	}
	if result.Exception != nil {
		return remoteObject{}, failure("javascript", "control operation threw an exception")
	}
	return result.Result, nil
}
func (p *Page) runTarget(ctx context.Context, target resolvedTarget, function string, args []any, gesture bool) error {
	result, err := p.callTarget(ctx, target, function, args, gesture)
	if err != nil {
		return err
	}
	return targetResult(result)
}
func targetResult(result remoteObject) error {
	var status string
	if result.Type != "string" || json.Unmarshal(result.Value, &status) != nil {
		return failure("protocol", "invalid control operation result")
	}
	switch status {
	case "":
		return nil
	case "stale":
		return failure("stale", "control node is detached or in another document")
	case "unsupported":
		return failure("unsupported", "control does not support this operation")
	case "not_editable":
		return failure("invalid_input", "control is not a supported editable element")
	case "focus":
		return failure("blocked", "control lost focus before input")
	case "selection":
		return failure("blocked", "field selection changed before text replacement")
	case "not_select":
		return failure("invalid_input", "control is not a native select")
	case "missing":
		return failure("invalid_input", "no option has that exact value or label")
	case "ambiguous":
		return failure("ambiguous", "more than one option has that exact value or label")
	default:
		return failure("protocol", "unknown control operation result")
	}
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

// AX is the only semantic eligibility policy, including for native options.
// DOM checks here establish identity/liveness only. Absent AX properties do not
// authorize a competing DOM/ancestor-derived disabled or readonly decision.
func (p *Page) checkTarget(ctx context.Context, target resolvedTarget, editing bool) error {
	if err := p.validateDocuments(ctx, target.chain); err != nil {
		return err
	}
	if err := p.runTarget(ctx, target, `function(){`+targetHelpers+`return connected(this)}`, nil, false); err != nil {
		return err
	}
	var result struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := p.client.call(ctx, target.doc.session, "Accessibility.getPartialAXTree", map[string]any{"backendNodeId": target.backend, "fetchRelatives": false}, &result); err != nil {
		return err
	}
	for _, node := range result.Nodes {
		if node.Backend != target.backend {
			continue
		}
		if node.Ignored {
			return failure("blocked", "control is no longer exposed in the accessibility tree")
		}
		if node.property("disabled").boolean() {
			return failure("blocked", "control is disabled")
		}
		if editing && node.property("readonly").boolean() {
			return failure("blocked", "control is readonly")
		}
		return nil
	}
	return failure("unavailable", "control is unavailable in the accessibility tree")
}

// Click attempts browser-managed focus, then performs one coordinate-free DOM
// activation with user activation. Focus need not stick; click is not trusted
// pointer input and successful dispatch does not prove application success.
func (p *Page) Click(ctx context.Context, id ControlID) (_ ActionResult, err error) {
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
	defer func() { err = inputError(err, observation.warnings) }()
	target, err := p.resolveTarget(ctx, observation.target)
	defer p.releaseTargets(ctx, target.chain)
	if err != nil {
		return ActionResult{}, err
	}
	if err := p.checkTarget(ctx, target, false); err != nil {
		return ActionResult{}, err
	}
	if err := p.client.call(ctx, p.state.session, "Page.bringToFront", nil, nil); err != nil {
		return ActionResult{}, err
	}
	if err := observation.startInput(ctx); err != nil {
		return ActionResult{}, err
	}
	if err := p.runTarget(ctx, target, focusTarget, nil, true); err != nil {
		return ActionResult{}, err
	}
	if err := p.checkTarget(ctx, target, false); err != nil {
		return ActionResult{}, err
	}
	if err := p.runTarget(ctx, target, activateTarget, nil, true); err != nil {
		return ActionResult{}, err
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

const targetHelpers = `
 function connected(el){
  return el instanceof Element && el.isConnected && el.ownerDocument===document ? '' : 'stale';
 }
`
const focusTarget = `function(){` + targetHelpers + `
 const status=connected(this);if(status) return status;
 if(typeof this.focus==='function') this.focus();
 return connected(this);
}`
const activateTarget = `function(){` + targetHelpers + `
 const status=connected(this);if(status) return status;
 // Choose one activation before dispatch; never retry after a handler or
 // native default may have run. SVG exposes no HTMLElement.click() method.
 if(typeof this.click==='function') this.click();
 else if(this instanceof SVGElement) this.dispatchEvent(new MouseEvent('click',{bubbles:true,cancelable:true,composed:true,view:window}));
 else return 'unsupported';
 return '';
}`
