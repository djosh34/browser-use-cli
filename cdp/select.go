package cdp

import (
	"context"
	"unicode/utf8"
)

// Select chooses one native select option by exact value or label. Matches
// across both fields must be unique. Custom comboboxes use Click/Fill/Press.
func (p *Page) Select(ctx context.Context, reference ControlRef, value string) (ActionResult, error) {
	if !utf8.ValidString(value) {
		return ActionResult{}, failure("invalid_input", "option value must be valid UTF-8")
	}
	ref, err := reference.decode()
	if err != nil {
		return ActionResult{}, err
	}
	if ref.Page != p.id {
		return ActionResult{}, failure("invalid_input", "control reference belongs to another page")
	}
	if err := p.lock(ctx); err != nil {
		return ActionResult{}, err
	}
	defer p.unlock()
	if err := p.attach(ctx); err != nil {
		return ActionResult{}, err
	}
	observation, err := p.beginInput(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	defer p.endInput(observation)
	target, err := p.resolveTarget(ctx, ref, observation.documents)
	defer p.releaseTargets(ctx, target.chain)
	if err != nil {
		return ActionResult{}, err
	}
	if _, err := p.inputPoint(ctx, target); err != nil {
		return ActionResult{}, err
	}
	if err := observation.startInput(ctx); err != nil {
		return ActionResult{}, err
	}
	if err := p.client.call(ctx, target.doc.session, "DOM.focus", map[string]string{"objectId": target.object}, nil); err != nil {
		return ActionResult{}, err
	}
	if err := p.settleFrame(ctx, target.doc); err != nil {
		return ActionResult{}, err
	}
	if _, err := p.inputPoint(ctx, target); err != nil {
		return ActionResult{}, err
	}
	var status string
	if err := p.nodeCall(ctx, target.doc, target.object, selectOption, []any{value}, &status); err != nil {
		return ActionResult{}, err
	}
	switch status {
	case "not_select":
		return ActionResult{}, failure("invalid_input", "control is not a native select")
	case "missing":
		return ActionResult{}, failure("invalid_input", "no option has that exact value or label")
	case "ambiguous":
		return ActionResult{}, failure("ambiguous", "more than one option has that exact value or label")
	case "option_disabled":
		return ActionResult{}, failure("blocked", "matching option is disabled or hidden")
	case "focus":
		return ActionResult{}, failure("blocked", "select lost focus before input")
	}
	if err := targetStatus(status); err != nil {
		return ActionResult{}, err
	}
	return p.completeInput(ctx, observation, target.doc)
}

const selectOption = `function(value){` + inputHelpers + `
 const s=state(this);if(s) return s;
 if(!(this instanceof HTMLSelectElement)) return 'not_select';
 if(this.getAttribute('aria-readonly')==='true') return 'blocked';
 if(this.getRootNode().activeElement!==this) return 'focus';
 const matches=Array.from(this.options).filter(option=>option.value===value || option.label===value);
 if(matches.length===0) return 'missing';if(matches.length!==1) return 'ambiguous';
 const option=matches[0];
 if(option.matches(':disabled') || option.hidden || getComputedStyle(option).display==='none' || option.parentElement.hidden || getComputedStyle(option.parentElement).display==='none') return 'option_disabled';
 if(this.selectedIndex===option.index && this.selectedOptions.length===1) return '';
 this.selectedIndex=option.index;
 this.dispatchEvent(new Event('input',{bubbles:true,composed:true}));
 this.dispatchEvent(new Event('change',{bubbles:true}));
 return '';
}`
