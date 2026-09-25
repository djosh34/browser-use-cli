//go:build integration

package cdp_test

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestElementFillContenteditableInShadowRoots(t *testing.T) {
	elementDrivers(t, func(t *testing.T, d elementDriver) {
		for _, mode := range []string{"open", "closed"} {
			for _, content := range []struct{ name, markup string }{
				{"plain", "old editor"},
				{"rich", "old <b>rich</b> editor"},
			} {
				t.Run(mode+"/"+content.name, func(t *testing.T) {
					markup, err := json.Marshal(`<div contenteditable aria-label="Editor">` + content.markup + `</div><div contenteditable aria-label="Other editor">untouched</div><button>Inspect editors</button>`)
					if err != nil {
						t.Fatal(err)
					}
					fixture := elementFixture(t, fmt.Sprintf(`<div id="host"></div><script>{
 const root=document.getElementById('host').attachShadow({mode:%q});root.innerHTML=%s;
 const editor=root.querySelector('[aria-label="Editor"]');
 const other=root.querySelector('[aria-label="Other editor"]');
 const inputs=[];editor.addEventListener('input',e=>inputs.push(e.isTrusted));
 root.querySelector('button').onclick=()=>{
  document.title=JSON.stringify([editor.textContent,other.textContent,inputs.length>0,inputs.every(Boolean)]);
  inputs.length=0;
 };
}</script>`, mode, markup))
					d.open(t, fixture.URL)
					for _, text := range []string{"replacement 日本語 😀", "", "refilled after clear"} {
						d.act(t, "fill", d.target(t, "Editor"), text, "")
						d.act(t, "click", d.target(t, "Inspect editors"), "", "")
						d.title(t, fmt.Sprintf(`[%q,"untouched",true,true]`, text))
					}
				})
			}
		}
	})
}

func TestElementFillRejectsPartialShadowSelection(t *testing.T) {
	elementDrivers(t, func(t *testing.T, d elementDriver) {
		for _, mode := range []string{"open", "closed"} {
			t.Run(mode, func(t *testing.T) {
				fixture := elementFixture(t, fmt.Sprintf(`<div id="host"></div><script>{
 const root=document.getElementById('host').attachShadow({mode:%q});
 root.innerHTML='<div contenteditable aria-label="Editor"><span>repeat</span><span>repeat</span></div><div contenteditable aria-label="Other editor">untouched</div><button>Inspect editors</button>';
 const editor=root.querySelector('[aria-label="Editor"]');
 const other=root.querySelector('[aria-label="Other editor"]');
 let redirected=false,inputs=0;
 editor.addEventListener('input',()=>inputs++);
 document.addEventListener('selectionchange',()=>{
  if(redirected || root.activeElement!==editor)return;
  redirected=true;
  const range=document.createRange();range.selectNodeContents(editor.firstChild);
  const selection=root.getSelection();selection.removeAllRanges();selection.addRange(range);
 });
 root.querySelector('button').onclick=()=>document.title=JSON.stringify([editor.textContent,other.textContent,redirected,inputs]);
}</script>`, mode))
				d.open(t, fixture.URL)
				d.act(t, "fill", d.target(t, "Editor"), "replacement", "blocked")
				d.act(t, "click", d.target(t, "Inspect editors"), "", "")
				d.title(t, `["repeatrepeat","untouched",true,0]`)
			})
		}
	})
}
