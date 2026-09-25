//go:build integration

package cdp_test

import (
	"strings"
	"testing"
)

func TestElementClickFocusAndActivation(t *testing.T) {
	elementDrivers(t, func(t *testing.T, d elementDriver) {
		for _, tc := range []struct{ name, target, want string }{
			{"focus before click", `<button id="target">Target</button>`, "focus:target:true|click:target:false:true"},
			{"nonfocusable", `<div role="button" id="target">Target</div>`, "click:prior:false:true"},
			{"focus redirect", `<button id="target" onfocus="sink.focus()">Target</button>`, "focus:sink:true|click:sink:false:true"},
			{"click redirect", `<button id="target" onclick="sink.focus()">Target</button>`, "focus:target:true|click:sink:false:true"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fixture := elementFixture(t, `<input id="prior" aria-label="Prior"><input id="sink" aria-label="Sink" oninput="document.title='sink:'+this.value">`+tc.target+`
<script>
 const log=[];
 for(const type of ['mouseenter','pointerdown','mousedown','pointerup','mouseup']) target.addEventListener(type,()=>log.push(type));
 target.addEventListener('focus',()=>log.push('focus:'+document.activeElement.id+':'+navigator.userActivation.isActive));
 target.addEventListener('click',e=>{log.push('click:'+document.activeElement.id+':'+e.isTrusted+':'+navigator.userActivation.isActive);document.title=log.join('|')});
 prior.focus();
</script>`)
				d.open(t, fixture.URL)
				d.act(t, "click", d.target(t, "Target"), "", "")
				d.title(t, tc.want)
				if strings.Contains(tc.name, "redirect") {
					d.act(t, "press", 0, "x", "")
					d.value(t, "Sink", "x")
					d.value(t, "Prior", "")
					d.title(t, "sink:x")
				}
			})
		}
	})
}

func TestElementActionsUseAXEligibility(t *testing.T) {
	fixture := elementFixture(t, `
 <button disabled onclick="document.title='wrong'">Native disabled</button>
 <button aria-disabled="true" onclick="document.title='wrong'">ARIA disabled</button>
 <fieldset disabled><button onclick="document.title='wrong'">Fieldset disabled</button></fieldset>
 <div aria-disabled="true"><button onclick="document.title='wrong'">Inherited disabled</button>
 <button aria-disabled="false" onclick="document.title='override'">Enabled override</button></div>
 <input aria-label="Native readonly" readonly value="native" onclick="document.title='native click'">
 <input aria-label="ARIA readonly" aria-readonly="true" value="aria" onclick="document.title='aria click'">
 <input aria-label="Role ignores readonly" role="button" aria-readonly="true" value="editable">
 <button onclick="document.title=document.querySelector('[aria-label=&quot;Role ignores readonly&quot;]').value">Inspect role value</button>
 `)
	elementDrivers(t, func(t *testing.T, d elementDriver) {
		for _, name := range []string{"Native disabled", "ARIA disabled", "Fieldset disabled", "Inherited disabled"} {
			t.Run(name, func(t *testing.T) {
				d.open(t, fixture.URL)
				r := d.read(t)
				n := axNamed(t, r, name)
				if n.State["disabled"] != true {
					t.Fatalf("fixture must expose AX disabled: %+v", n)
				}
				for _, action := range []string{"click", "fill", "select"} {
					d.act(t, action, n.ID, "must not act", "blocked")
				}
				d.title(t, "Untouched")
			})
		}
		t.Run("explicit enabled descendant", func(t *testing.T) {
			d.open(t, fixture.URL)
			n := axNamed(t, d.read(t), "Enabled override")
			if n.State["disabled"] == true {
				t.Fatalf("fixture must expose an enabled override: %+v", n)
			}
			d.act(t, "click", n.ID, "", "")
			d.title(t, "override")
		})
		for _, tc := range []struct{ name, value, title string }{{"Native readonly", "native", "native click"}, {"ARIA readonly", "aria", "aria click"}} {
			t.Run(tc.name, func(t *testing.T) {
				d.open(t, fixture.URL)
				n := axNamed(t, d.read(t), tc.name)
				if n.State["readonly"] != true {
					t.Fatalf("fixture must expose AX readonly: %+v", n)
				}
				d.act(t, "fill", n.ID, "wrong", "blocked")
				d.value(t, tc.name, tc.value)
				d.act(t, "click", n.ID, "", "")
				d.title(t, tc.title)
			})
		}
		t.Run("absent AX readonly is not a DOM veto", func(t *testing.T) {
			d.open(t, fixture.URL)
			n := axNamed(t, d.read(t), "Role ignores readonly")
			if n.State["readonly"] == true {
				t.Fatalf("fixture must not expose readonly for button role: %+v", n)
			}
			d.act(t, "fill", n.ID, "replacement", "")
			d.act(t, "click", d.target(t, "Inspect role value"), "", "")
			d.title(t, "replacement")
		})
	})
}

func TestElementActionsDoNotRetargetDisappearance(t *testing.T) {
	elementDrivers(t, func(t *testing.T, d elementDriver) {
		for _, tc := range []struct{ action, control, value string }{
			{"click", `<button aria-label="Target" onfocus="replace(this)" onclick="document.title='wrong click'">Target</button>`, ""},
			{"fill", `<input aria-label="Target" value="old" onfocus="replace(this)">`, "replacement"},
			{"select", `<select aria-label="Target" onfocus="replace(this)"><option value="old">Old</option><option value="new">New</option></select>`, "new"},
		} {
			t.Run(tc.action, func(t *testing.T) {
				fixture := elementFixture(t, tc.control+`<button onclick="document.title='replacement:'+document.querySelector('[aria-label=Target]').value">Inspect</button>
<script>function replace(el){const replacement=el.cloneNode(true);replacement.removeAttribute('onfocus');el.replaceWith(replacement);replacement.focus()}</script>`)
				d.open(t, fixture.URL)
				d.act(t, tc.action, d.target(t, "Target"), tc.value, "stale")
				d.title(t, "Untouched")
				if tc.action != "click" {
					d.act(t, "click", d.target(t, "Inspect"), "", "")
					d.title(t, "replacement:old")
				}
			})
		}
		t.Run("completed self removal is not replayed", func(t *testing.T) {
			fixture := elementFixture(t, `<button onclick="document.title='once';this.remove()">Remove self</button><button onclick="document.title='wrong replacement'">Next ordinal</button>`)
			d.open(t, fixture.URL)
			d.act(t, "click", d.target(t, "Remove self"), "", "")
			d.title(t, "once")
			if id := d.target(t, "Next ordinal"); id != 1 {
				t.Fatalf("remaining ordinal = %d, want 1", id)
			}
		})
	})
}
