//go:build integration

package cdp_test

import (
	"fmt"
	"testing"
)

func TestElementFillFullReplacement(t *testing.T) {
	elementDrivers(t, func(t *testing.T, d elementDriver) {
		for _, tc := range []struct{ name, field, value string }{
			{"text", `<input id="field" aria-label="Field" value="old text">`, "Nieuw 日本語 😀"},
			{"textarea", `<textarea id="field" aria-label="Field">old notes</textarea>`, "line one\nline two"},
			{"contenteditable", `<div id="field" aria-label="Field" contenteditable>old <b>rich</b> text</div>`, "replacement"},
			{"email", `<input id="field" aria-label="Field" type="email" value="old@example.test">`, "new@example.test"},
			{"number", `<input id="field" aria-label="Field" type="number" value="12345">`, "42.5"},
			{"empty text", `<input id="field" aria-label="Field" value="old text">`, ""},
			{"empty email", `<input id="field" aria-label="Field" type="email" value="old@example.test">`, ""},
			{"empty number", `<input id="field" aria-label="Field" type="number" value="12345">`, ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fixture := elementFixture(t, tc.field+`<input aria-label="Other" value="untouched"><button onclick="document.title=JSON.stringify([field.isContentEditable?field.textContent:field.value,events.length>0,events.every(e=>e==='input:true')])">Inspect</button>
<style>#field{pointer-events:none}</style><div style="position:fixed;inset:0;background:white"></div>
<script>const events=[];field.addEventListener('input',e=>events.push(e.type+':'+e.isTrusted));</script>`)
				d.open(t, fixture.URL)
				d.act(t, "fill", d.target(t, "Field"), tc.value, "")
				d.value(t, "Other", "untouched")
				d.act(t, "click", d.target(t, "Inspect"), "", "")
				d.title(t, fmt.Sprintf(`[%q,true,true]`, tc.value))
			})
		}
	})
}

func TestElementFillRejectsRedirectedFocusAndSelection(t *testing.T) {
	elementDrivers(t, func(t *testing.T, d elementDriver) {
		for _, tc := range []struct{ name, field, script, old string }{
			{"focus", `<input id="field" aria-label="Field" value="old" onfocus="other.focus()">`, "", "old"},
			{"selection text", `<input id="field" aria-label="Field" value="old">`, `field.addEventListener('select',()=>field.setSelectionRange(0,0),{once:true});`, "old"},
			{"selection email", `<input id="field" aria-label="Field" type="email" value="old@example.test">`, `field.addEventListener('select',()=>getSelection().collapseToEnd(),{once:true});`, "old@example.test"},
			{"selection number", `<input id="field" aria-label="Field" type="number" value="12345">`, `field.addEventListener('select',()=>getSelection().collapseToEnd(),{once:true});`, "12345"},
			{"selection contenteditable", `<div id="field" aria-label="Field" contenteditable>old <b>rich</b> text</div>`, `document.addEventListener('selectionchange',()=>{if(document.activeElement===field)getSelection().collapse(field,0)},{once:true});`, "old rich text"},
			{"selection redirects focus", `<input id="field" aria-label="Field" value="old">`, `field.addEventListener('select',()=>other.focus(),{once:true});`, "old"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fixture := elementFixture(t, tc.field+`<input id="other" aria-label="Other" value="untouched"><button onclick="document.title=JSON.stringify([field.isContentEditable?field.textContent:field.value,other.value,inputs])">Inspect</button>
<script>let inputs=0;document.addEventListener('input',()=>inputs++);`+tc.script+`</script>`)
				d.open(t, fixture.URL)
				d.act(t, "fill", d.target(t, "Field"), "replacement", "blocked")
				d.act(t, "click", d.target(t, "Inspect"), "", "")
				d.title(t, fmt.Sprintf(`[%q,"untouched",0]`, tc.old))
			})
		}
	})
}

func TestElementSelectExactMatchingAndEventsWithoutFocus(t *testing.T) {
	fixture := elementFixture(t, `<input id="sink" aria-label="Sink" value="untouched">
<select id="choice" aria-label="Choice" aria-readonly="true" onfocus="focuses++;sink.focus()">
<option value="a">Alpha</option><option value="b">Beta</option>
<option value="x">Duplicate</option><option value="y">Duplicate</option>
<option value="Collision">Other label</option><option value="z">Collision</option>
<option value="d" disabled>Native disabled option</option>
<option value="r" aria-disabled="true">ARIA disabled option</option>
<option value="h" hidden>DOM hidden option</option>
<option value="n" style="display:none">DOM display none option</option>
<option value="q" aria-readonly="true">Ignored readonly option</option>
<optgroup hidden label="Hidden group"><option value="j">Hidden group option</option></optgroup>
<optgroup disabled label="Disabled group"><option value="g">Group disabled option</option></optgroup>
<optgroup aria-disabled="true" label="ARIA group"><option value="i">Inherited disabled option</option><option value="e" aria-disabled="false">Enabled option override</option></optgroup>
</select>
<button onclick="document.title=JSON.stringify([choice.value,events,focuses])">Inspect</button>
<script>let focuses=0;const events=[];for(const type of ['input','change'])choice.addEventListener(type,e=>events.push(e.type+':'+e.isTrusted+':'+e.bubbles+':'+e.composed+':'+document.activeElement.id));</script>`)
	elementDrivers(t, func(t *testing.T, d elementDriver) {
		for _, tc := range []struct{ name, value, code, selected, events string }{
			{"exact value", "b", "", "b", `"input:false:true:true:sink","change:false:true:false:sink"`},
			{"exact label", "Beta", "", "b", `"input:false:true:true:sink","change:false:true:false:sink"`},
			{"unchanged", "Alpha", "", "a", ""},
			{"ambiguous label", "Duplicate", "ambiguous", "a", ""},
			{"value label collision", "Collision", "ambiguous", "a", ""},
			{"missing", "absent", "invalid_input", "a", ""},
			{"case sensitive", "beta", "invalid_input", "a", ""},
			{"no trimming", " Beta ", "invalid_input", "a", ""},
			{"native disabled", "d", "blocked", "a", ""},
			{"ARIA disabled", "r", "blocked", "a", ""},
			{"disabled optgroup", "g", "blocked", "a", ""},
			{"inherited AX disabled", "i", "blocked", "a", ""},
			{"AX enabled override", "e", "", "e", `"input:false:true:true:sink","change:false:true:false:sink"`},
			{"AX exposed hidden option", "h", "", "h", `"input:false:true:true:sink","change:false:true:false:sink"`},
			{"AX exposed display none option", "n", "", "n", `"input:false:true:true:sink","change:false:true:false:sink"`},
			{"AX ignores option readonly", "q", "", "q", `"input:false:true:true:sink","change:false:true:false:sink"`},
			{"AX exposed hidden group option", "j", "", "j", `"input:false:true:true:sink","change:false:true:false:sink"`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				d.open(t, fixture.URL)
				r := d.read(t)
				// Chrome exposes these native options even though DOM layout hides
				// them, and does not expose readonly for this select/option role.
				for _, name := range []string{"Choice", "DOM hidden option", "DOM display none option", "Hidden group option", "Ignored readonly option"} {
					if n := axNamed(t, r, name); n.State["readonly"] == true || n.State["disabled"] == true {
						t.Fatalf("fixture must expose enabled option/select without readonly: %+v", n)
					}
				}
				for _, name := range []string{"Native disabled option", "ARIA disabled option", "Group disabled option", "Inherited disabled option"} {
					if n := axNamed(t, r, name); n.State["disabled"] != true {
						t.Fatalf("fixture option not AX disabled: %+v", n)
					}
				}
				if n := axNamed(t, r, "Enabled option override"); n.State["disabled"] == true {
					t.Fatalf("fixture override not AX enabled: %+v", n)
				}
				d.act(t, "select", d.target(t, "Choice"), tc.value, tc.code)
				// Read the sink before inspecting: inspection itself is allowed to focus.
				d.value(t, "Sink", "untouched")
				d.act(t, "click", d.target(t, "Inspect"), "", "")
				// Rejected matches may be rejected before focus; success must attempt it.
				if tc.code == "" {
					d.title(t, fmt.Sprintf(`[%q,[%s],1]`, tc.selected, tc.events))
				} else {
					title := d.read(t).Page.Title
					if title != fmt.Sprintf(`[%q,[],0]`, tc.selected) && title != fmt.Sprintf(`[%q,[],1]`, tc.selected) {
						t.Fatalf("rejected selection changed state: %s", title)
					}
				}
			})
		}
	})
}
