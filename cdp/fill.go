package cdp

import (
	"context"
	"unicode/utf8"
)

// Fill replaces text in text-like inputs, textarea, and contenteditable elements
// with trusted browser input. It does not submit the field or await app timers.
func (p *Page) Fill(ctx context.Context, id ControlID, text string) (_ ActionResult, err error) {
	if !utf8.ValidString(text) {
		return ActionResult{}, failure("invalid_input", "fill text must be valid UTF-8")
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
	observation, err := p.beginInput(ctx, "fill", id)
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
	if _, err := p.inputPoint(ctx, target); err != nil {
		return ActionResult{}, err
	}
	if err := observation.startInput(ctx); err != nil {
		return ActionResult{}, err
	}
	var status string
	if err := p.nodeCall(ctx, target.doc, target.object, prepareFill, nil, &status); err != nil {
		return ActionResult{}, err
	}
	if err := fillStatus(status); err != nil {
		return ActionResult{}, err
	}
	if err := p.settleFrame(ctx, target.doc); err != nil {
		return ActionResult{}, err
	}
	// Focus/selection handlers can replace, disable, cover, or redirect the field.
	if _, err := p.inputPoint(ctx, target); err != nil {
		return ActionResult{}, err
	}
	if err := p.nodeCall(ctx, target.doc, target.object, "function(){"+fillHelpers+"return fillState(this)}", nil, &status); err != nil {
		return ActionResult{}, err
	}
	if err := fillStatus(status); err != nil {
		return ActionResult{}, err
	}
	if err := p.client.call(ctx, target.doc.session, "Input.insertText", map[string]string{"text": text}, nil); err != nil {
		return ActionResult{}, err
	}
	return p.completeInput(ctx, observation, target.doc)
}
func fillStatus(status string) error {
	switch status {
	case "readonly":
		return failure("blocked", "control is readonly")
	case "not_editable":
		return failure("invalid_input", "control is not a supported editable element")
	case "focus":
		return failure("blocked", "control lost focus before input")
	case "selection":
		return failure("blocked", "field selection changed before text replacement")
	}
	return targetStatus(status)
}

const fillHelpers = inputHelpers + `
 function fillState(el){
  const s=state(el);if(s) return s;
  if(el.readOnly || el.getAttribute('aria-readonly')==='true') return 'readonly';
  if(!(el instanceof HTMLTextAreaElement) && !(el instanceof HTMLInputElement && ['text','search','email','tel','url','password','number'].includes(el.type)) && !el.isContentEditable) return 'not_editable';
  if(el.getRootNode().activeElement!==el) return 'focus';
  if(el.isContentEditable){
   const selection=getSelection();if(selection.rangeCount!==1) return 'selection';
   const range=selection.getRangeAt(0);
   if(range.startContainer!==el || range.startOffset!==0 || range.endContainer!==el || range.endOffset!==el.childNodes.length) return 'selection';
  }else if(el.selectionStart!==null && (el.selectionStart!==0 || el.selectionEnd!==el.value.length)) return 'selection';
  return '';
 }
`
const prepareFill = `function(){` + fillHelpers + `
 const s=state(this);if(s) return s;
 if(this.readOnly || this.getAttribute('aria-readonly')==='true') return 'readonly';
 if(this instanceof HTMLTextAreaElement || this instanceof HTMLInputElement && ['text','search','email','tel','url','password','number'].includes(this.type)){
  this.focus();this.select();
 }else if(this.isContentEditable){
  this.focus();const range=document.createRange();range.selectNodeContents(this);
  const selection=getSelection();selection.removeAllRanges();selection.addRange(range);
 }else return 'not_editable';
 return fillState(this);
}`
