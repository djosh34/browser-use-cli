package cdp

import (
	"context"
	"unicode/utf8"
)

// Fill replaces text in text-like inputs, textarea, and contenteditable elements
// using the browser's editing command. It emits native input, but not beforeinput,
// and does not submit the field or await application timers.
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
	if err := p.checkTarget(ctx, target, true); err != nil {
		return ActionResult{}, err
	}
	if err := p.client.call(ctx, p.state.session, "Page.bringToFront", nil, nil); err != nil {
		return ActionResult{}, err
	}
	if err := observation.startInput(ctx); err != nil {
		return ActionResult{}, err
	}
	if err := p.runTarget(ctx, target, prepareFill, nil, true); err != nil {
		return ActionResult{}, err
	}
	// Allow queued focus/selection handlers to run before checking AX and the
	// replacement selection again. Never restore site-directed focus changes.
	if err := p.settleFrame(ctx, target.doc); err != nil {
		return ActionResult{}, err
	}
	if err := p.checkTarget(ctx, target, true); err != nil {
		return ActionResult{}, err
	}
	// Keep focus/selection validation and insertion in one JS turn. A separate
	// Input.insertText call would target whatever gained focus in between.
	if err := p.runTarget(ctx, target, replaceFill, []any{text}, true); err != nil {
		return ActionResult{}, err
	}
	return p.completeInput(ctx, observation, target.doc)
}

const fillHelpers = targetHelpers + `
 function editable(el){
  return el instanceof HTMLTextAreaElement || el instanceof HTMLInputElement && ['text','search','email','tel','url','password','number'].includes(el.type) || el.isContentEditable;
 }
 function selectionFor(el){
  const root=el.getRootNode();
  return typeof root.getSelection==='function' ? root.getSelection() : getSelection();
 }
 function focused(el){
  return document.hasFocus() && el.getRootNode().activeElement===el;
 }
 // ShadowRoot.getSelection() normalizes an element-content range to text
 // endpoints. Accept equivalent DOM edges, not just equal selected text: a
 // partial range containing duplicated text must never pass as replacement.
 function atContentEdge(node,offset,el,end){
  while(node!==el){
   const length=node instanceof CharacterData ? node.length : node.childNodes.length;
   if(offset!==(end ? length : 0)) return false;
   const parent=node.parentNode;if(!parent) return false;
   offset=Array.prototype.indexOf.call(parent.childNodes,node)+(end ? 1 : 0);
   node=parent;
  }
  return offset===(end ? el.childNodes.length : 0);
 }
 function fillState(el){
  const status=connected(el);if(status) return status;
  if(!editable(el)) return 'not_editable';
  if(!focused(el)) return 'focus';
  if(el.isContentEditable){
   const selection=selectionFor(el);if(!selection || selection.rangeCount!==1) return 'selection';
   let range=selection.getRangeAt(0);
   const root=el.getRootNode(),documentSelection=getSelection();
   if(root instanceof ShadowRoot && typeof documentSelection.getComposedRanges==='function'){
    // Legacy shadow ranges can even collapse across empty text children.
    // An explicitly permitted shadow root exposes the underlying DOM range,
    // including in closed roots, without weakening full-replacement proof.
    const ranges=documentSelection.getComposedRanges({shadowRoots:[root]});
    if(ranges.length!==1) return 'selection';
    range=ranges[0];
   }
   if(!atContentEdge(range.startContainer,range.startOffset,el,false) || !atContentEdge(range.endContainer,range.endOffset,el,true)) return 'selection';
  }else if(el.selectionStart!==null){
   if(el.selectionStart!==0 || el.selectionEnd!==el.value.length) return 'selection';
  }else{
   // Email and number have no selectionStart/End API. Chrome still exposes
   // their selected text. Refuse an unprovable replacement, including native
   // number display text that differs from its sanitized machine value.
   const selection=selectionFor(el);
   if(!selection || selection.toString()!==el.value) return 'selection';
  }
  return '';
 }
`
const prepareFill = `function(){` + fillHelpers + `
 const status=connected(this);if(status) return status;
 if(!editable(this)) return 'not_editable';
 this.focus();
 const after=connected(this);if(after) return after;
 if(!focused(this)) return 'focus';
 if(this.isContentEditable){
  const range=document.createRange();range.selectNodeContents(this);
  const selection=selectionFor(this);selection.removeAllRanges();selection.addRange(range);
 }else this.select();
 return fillState(this);
}`
const replaceFill = `function(text){` + fillHelpers + `
 const status=fillState(this);if(status) return status;
 return document.execCommand('insertText',false,text) ? '' : 'unsupported';
}`
