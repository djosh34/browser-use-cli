package cdp

import (
	"context"
	"unicode/utf8"
)

// Select chooses one native select option by exact value or label. Matches
// across both fields must be unique. Custom comboboxes use Click/Fill/Press.
func (p *Page) Select(ctx context.Context, id ControlID, value string) (_ ActionResult, err error) {
	if !utf8.ValidString(value) {
		return ActionResult{}, failure("invalid_input", "option value must be valid UTF-8")
	}
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
	observation, err := p.beginInput(ctx, "select", id)
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
	if err := p.checkTarget(ctx, target, true); err != nil {
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
	if err := p.settleFrame(ctx, target.doc); err != nil {
		return ActionResult{}, err
	}
	if err := p.checkTarget(ctx, target, true); err != nil {
		return ActionResult{}, err
	}
	// Native values/labels identify the requested option; only AX decides its
	// eligibility. Retain the actual option object across that AX query, so a
	// replacement at the same index/value never silently receives the action.
	option, err := p.callTarget(ctx, target, findOption, []any{value}, false)
	if err != nil {
		return ActionResult{}, err
	}
	if option.ID == "" {
		return ActionResult{}, targetResult(option)
	}
	var described struct {
		Node domNode `json:"node"`
	}
	if err := p.client.call(ctx, target.doc.session, "DOM.describeNode", map[string]string{"objectId": option.ID}, &described); err != nil {
		return ActionResult{}, err
	}
	if described.Node.Backend == 0 {
		return ActionResult{}, failure("protocol", "option has no native identity")
	}
	boundOption := resolvedTarget{doc: target.doc, chain: target.chain, backend: described.Node.Backend, object: option.ID}
	if err := p.checkTarget(ctx, boundOption, true); err != nil {
		return ActionResult{}, err
	}
	if err := p.runTarget(ctx, target, selectOption, []any{option, value}, true); err != nil {
		return ActionResult{}, err
	}
	return p.completeInput(ctx, observation, target.doc)
}

const optionHelpers = targetHelpers + `
 function matchOption(el,value){
  const status=connected(el);if(status) return status;
  if(!(el instanceof HTMLSelectElement)) return 'not_select';
  const matches=Array.from(el.options).filter(option=>option.value===value || option.label===value);
  if(matches.length===0) return 'missing';
  if(matches.length!==1) return 'ambiguous';
  return matches[0];
 }
`
const findOption = `function(value){` + optionHelpers + `return matchOption(this,value);}`
const selectOption = `function(option,value){` + optionHelpers + `
 const status=connected(this) || connected(option);if(status) return status;
 const match=matchOption(this,value);
 if(typeof match==='string') return match;
 if(match!==option) return 'stale';
 if(this.selectedIndex===option.index && this.selectedOptions.length===1) return '';
 this.selectedIndex=option.index;
 this.dispatchEvent(new Event('input',{bubbles:true,composed:true}));
 this.dispatchEvent(new Event('change',{bubbles:true}));
 return '';
}`
