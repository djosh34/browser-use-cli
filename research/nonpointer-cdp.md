# Coordinate-free CDP activation: decision evidence

**2026-09-25 · investigation #31 · planning only · baseline `8767db6`.** Read [#28](https://github.com/djosh34/browser-use-cli/issues/28), [#30 and its confirmed decisions](https://github.com/djosh34/browser-use-cli/issues/30), and [#31 including comments](https://github.com/djosh34/browser-use-cli/issues/31). Claimed #31; leave closure to the coordinator. No product code changes, no subagents, no changes to execution draft #32.

## The human compatibility decision

**Recommendation: accept focus + DOM activation, with user activation, as the CLI's click contract—not exact Chrome accessibility activation.** It satisfies the confirmed focus-before-activation requirement and works without pointer-location gates. The meaningful price is compatibility with sites that require **trusted clicks or pointer-down/up handlers**, and some native picker/option actions.

The remaining human question is:

> Is this semantic activation contract sufficient, with those limitations explicitly documented, or is exact accessibility-action fidelity required for specific essential workflows?

If sufficient, CDP already supplies the necessary basic mechanism; a platform accessibility bridge is **not established as necessary**. If not, name the essential workflows requiring the missing behavior before commissioning another backend. Keyboard activation helps particular controls but does **not** close the generic gap. Do not silently retry activation with a different mechanism: a first action may already have submitted or navigated.

Already settled, not questions to reopen: Chrome's exposed AX state is the eligibility authority; overlays are not vetoes; attempt focus without requiring success or restoring site-directed focus; keep the CLI interface. Go API migration remains unapproved. Fill replacement/selection semantics and operation-specific unsupported outcomes still need product agreement; this investigation does not decide them.

## Observed evidence

Chrome for Testing **153.0.8010.52**, Linux ARM64, headless, CDP `1.3`, Chromium revision **`78e5e45d4bb41035e17ea4da2cc257f496416ac9`**. Two runs of **114 generated cases**; same focus/effects/activation/value/event-sequence outcomes in the comparison (scroll animation positions and URLs were not compared). No recorded protocol/JavaScript exceptions in the final cases.

Each case used a new owned browser context/page, initially focused `#prior`. All 114 final cases began with `isSecureContext=true` and `navigator.userActivation.{isActive,hasBeenActive}=false`. The fixture had a fixed full-viewport overlay. Actions used a backend node resolved into a frame-local isolated world, not coordinates. Table notation: **B** = `this.click()` without user gesture; **FG** = `this.focus(); this.click()` with `userGesture:true`; **T/F** = event `isTrusted`.

| Comparison | Actual result | Product significance |
|---|---|---|
| Plain button, B | `click:F`; focus stays `prior`; activation false | Bare click does not meet the approved focus requirement. |
| Plain button, FG | `blur:T → focusout:T → focus:T → focusin:T → click:F`; focus becomes button; activation true | Focus works, but `userGesture` does **not** make the click trusted. No pointer/mouse-down/up events. |
| Button, focus without gesture / bare click with gesture | Respectively focused + activation false / previous focus + activation true; both clicks F | Focus, trust, and activation are independent. |
| Button Enter / Space after focus | Enter: `keydown:T → keypress:T → click:T → keyup:T`; Space: `keydown:T → keypress:T → keyup:T → click:T` | Trusted keyboard-derived click, but a different event contract—not AX activation. |
| Trust-gated button / pointerdown-only handler | FG invoked neither effect; Enter/Space passed trust gate but not pointerdown handler | A successful CDP reply is not application success. |
| Overlay, `pointer-events:none`, transformed button | B/FG delivered click; FG focused each | No hit testing or visual gates needed. |
| Offscreen button at y=3000 | B activated without scrolling; FG activated and browser focus scrolled to `scrollY=2264` | Browser-managed focus may scroll. Scrolling is not an eligibility test. |
| Nonfocusable exposed `role=button` | FG still clicked; focus stayed `prior`. Following Tab reached first button, **not** the button after the target | Failed focus must not block click; subsequent keyboard navigation is not fully AX-equivalent. |
| Focus-handler or click-handler redirects focus | FG still clicked the target; final focus was `text`, not restored | Matches the approved focus contract. |
| Native disabled / disabled fieldset descendant | AX `disabled:true`; B/FG produced no click or effect | Native disabled behavior remains browser behavior. |
| `aria-disabled` button / descendant of `aria-disabled` container | Chrome exposed `disabled:true` in both; direct B/FG nevertheless executed handlers | AX eligibility must actually be honored; DOM click alone does not enforce ARIA. Harness intentionally bypassed eligibility to expose this distinction. |
| Inert / `display:none` button | AX `ignored:true`; direct JS click still ran handler; focus did not move | JS-callability is not AX exposure. Do not rebuild a DOM-ancestor policy. |
| Native readonly / `aria-readonly` text input | Both AX `readonly:true`; both focus/click. `Input.insertText` left native readonly `old`, but changed ARIA-only readonly to `7` | Readonly is operation-specific, not a universal click/focus ban; editing must honor AX state. |
| Checkbox / radio | B/FG changed checked state; `click:F → input:T → change:T`. Space toggled with trusted click; Enter did not | Synthetic initial click can produce trusted native consequence events. Enter is not a universal activation key. |
| Link / form submit | B/FG navigated fragment / submitted local GET to `/done?q=owned`; submit event T | Untrusted click still invokes important native defaults. |
| `_blank` link / handler `window.open` | Without gesture: no new page, popup returned false. With gesture: one new owned page, popup true | `userGesture:true` materially affects compatibility. Popup blocking was not disabled. |
| `<summary>` | B/FG opened details; click F, toggle T | Representative native activation works. |
| Native select / listbox / option | B/FG clicked but did not change selection; option focus stayed `prior`. Select `:open` false after FG | DOM click is not generic native selection or picker opening. AX even reported the tested option `focusable:true`; actual focus attempt remained a no-op. |
| Select keyboard / explicit mutation | Enter/Space made select `:open=true`; ArrowDown selected `b` with T input/change. Direct value mutation + dispatch produced F input/change | Product must choose selection/event fidelity, not assume click solves it. |
| Range / number / date | Click did not adjust value; ArrowDown changed `3→2`, `2→1`, `2026-01-01→2026-12-01` respectively | Keyboard behavior is control- and focused-subfield-specific. |
| File / color input | FG caused one intercepted `Page.fileChooserOpened` / color `:open=true`; bare click did neither | Gesture-gated native behavior works here; no file was supplied and no picker selection was completed. |
| Text / textarea / contenteditable / number | Focus + replacement selection + `Input.insertText("7")` replaced content, with T beforeinput/input | A coordinate-free editing path exists; not a complete fill contract or IME investigation. |
| Same-origin iframe / cross-site OOPIF | FG focused/clicked correct frame button; Enter dispatched through root session produced trusted click there | Frame-local object/session resolution works; no cross-origin DOM traversal required. |
| Open / closed shadow-root button | Backend-node resolution + FG and Enter worked; document focus/events retargeted to host, open composed path exposed inner button | Do not validate deep focus solely against top-level `document.activeElement`. Closed-root logging was outside the root. |

Ordinary FG activation remained active at the 120 ms post-action sample. New tabs, file and color pickers consumed transient activation (`isActive=false`, `hasBeenActive=true` after action). We did not establish an activation lifetime guarantee. FG grants activation **before focus handlers**, not just before click. The deliberately ineligible native-disabled probe also received activation despite doing nothing—another reason eligibility belongs before action, not after it.

## What Chrome accessibility activation actually does

The [tested-revision `AXObject::OnNativeClickAction`](https://github.com/chromium/chromium/blob/78e5e45d4bb41035e17ea4da2cc257f496416ac9/third_party/blink/renderer/modules/accessibility/ax_object.cc#L7877-L7925) grants user activation, sets the sequential focus navigation starting point even for nonfocusable elements, attempts focus with mouse-oriented focus parameters, and invokes `AccessKeyAction(kFromAccessibility)`. This is not `HTMLElement.click()`.

For ordinary elements, [the dispatcher](https://github.com/chromium/chromium/blob/78e5e45d4bb41035e17ea4da2cc257f496416ac9/third_party/blink/renderer/core/dom/events/event_dispatcher.cc#L95-L157) generates pointerdown/mousedown/pointerup/mouseup/click (mousedown/up suppression depends on pointerdown cancellation). [Simulated-event creation](https://github.com/chromium/chromium/blob/78e5e45d4bb41035e17ea4da2cc257f496416ac9/third_party/blink/renderer/core/events/simulated_event_util.cc#L135-L183) marks the accessibility events trusted and sets mouse-like click properties. [Option `AccessKeyAction`](https://github.com/chromium/chromium/blob/78e5e45d4bb41035e17ea4da2cc257f496416ac9/third_party/blink/renderer/core/html/forms/html_option_element.cc#L280-L284) specifically selects the option. Ordinary native [select handling](https://github.com/chromium/chromium/blob/78e5e45d4bb41035e17ea4da2cc257f496416ac9/third_party/blink/renderer/core/html/forms/select_type.cc#L363-L507) includes keyboard and mousedown paths absent from DOM click.

Also inspected current Chromium main at **`fc3d4c569bf9fdce95ec33ab1ee38366006b554c`**: [AX action](https://github.com/chromium/chromium/blob/fc3d4c569bf9fdce95ec33ab1ee38366006b554c/third_party/blink/renderer/modules/accessibility/ax_object.cc#L7933), [dispatcher](https://github.com/chromium/chromium/blob/fc3d4c569bf9fdce95ec33ab1ee38366006b554c/third_party/blink/renderer/core/dom/events/event_dispatcher.cc#L95), and [event trust](https://github.com/chromium/chromium/blob/fc3d4c569bf9fdce95ec33ab1ee38366006b554c/third_party/blink/renderer/core/events/simulated_event_util.cc#L180) retain those material distinctions.

This comparison is **source evidence, not an executed OS accessibility differential**. No platform bridge or assistive technology was invoked. There is no evidence here that exact AX fidelity is essential to this CLI, nor that ordinary JS can manufacture trusted pointer events.

## Protocol/spec facts and engineering conclusions

- [Runtime.callFunctionOn](https://chromedevtools.github.io/devtools-protocol/tot/Runtime/#method-callFunctionOn) documents `userGesture` as treating execution as initiated by a user in the UI. It does not promise trusted DOM events. [HTML click](https://html.spec.whatwg.org/multipage/interaction.html#dom-click) explicitly uses the not-trusted flag and returns for disabled native controls; [DOM event trust](https://dom.spec.whatwg.org/#dom-event-istrusted) documents the click exception. [User activation](https://html.spec.whatwg.org/multipage/interaction.html#tracking-user-activation) is separate, transient/consumable state.
- [Accessibility](https://chromedevtools.github.io/devtools-protocol/tot/Accessibility/) exposes tree/state queries, not a default-action command. The running browser's `/json/protocol` agreed: `disable`, `enable`, `getPartialAXTree`, `getFullAXTree`, `getRootAXNode`, `getAXNodeAndAncestors`, `getChildAXNodes`, `queryAXTree`. Its AX property vocabulary contains `disabled` and `readonly`. [ARIA disabled](https://www.w3.org/TR/wai-aria-1.2/#aria-disabled) and [readonly](https://www.w3.org/TR/wai-aria-1.2/#aria-readonly) express semantics; the measured Chrome AX output, not parallel DOM inference, is the project's chosen authority.
- [DOM.resolveNode](https://chromedevtools.github.io/devtools-protocol/tot/DOM/#method-resolveNode) and [Page.createIsolatedWorld](https://chromedevtools.github.io/devtools-protocol/tot/Page/#method-createIsolatedWorld) support frame-local backend-node resolution. [Input.dispatchKeyEvent](https://chromedevtools.github.io/devtools-protocol/tot/Input/#method-dispatchKeyEvent) targets the page's focused input, not a node. The nonfocusable Enter/Space probes actually went to `prior`; Space edited it. Thus keyboard/editing needs correct-target focus/selection validation, unlike click's nonmandatory focus success.

**Engineering choices, not further human decisions:** isolated-world/session/object lifetime management; fresh AX queries and stale-document handling; avoiding wrong-field input; navigation/new-page bookkeeping; preserving result shapes and reporting dispatch rather than business success. These can be specified after the fidelity decision. Removing coordinate checks does not mean discarding target-identity or editing-safety checks. This report is not an implementation design.

## Replay and retained evidence

- [Harness](evidence/nonpointer-cdp/harness.mjs): standalone Node 24 script, built-in WebSocket/HTTPS only; no product imports.
- [Final raw results](evidence/nonpointer-cdp/results.json): all 114 cases, AX properties, event details/trust/activation, focus, values and targets.
- [First-run raw results, gzip](evidence/nonpointer-cdp/results-first.json.gz).
- [Security observations](evidence/nonpointer-cdp/security.json): sandbox page and Chrome TLS state. No private key retained.

Actual commands used (temporary root `/tmp/nonpointer-cdp.KsfOdr`):

```sh
CHROME=/home/joshazimullah.linux/work_mounts/browser-use-cli/prototypes/browser-cli/.runtime/chrome-linux-arm64/chrome
"$CHROME" --version
# Google Chrome for Testing 153.0.8010.52
node /tmp/nonpointer-cdp.KsfOdr/harness.mjs > /tmp/nonpointer-cdp.KsfOdr/run.log 2>&1
# Exit 0; final line: DONE 114
# Repeated after adding sandbox/TLS and :open observations; exit 0, DONE 114.
```

Representative commands **actually sent** (object/session IDs omitted):

```json
{"method":"Runtime.callFunctionOn","params":{"objectId":"<resolved isolated-world target>","functionDeclaration":"function(){this.focus();this.click();return {active:document.activeElement.id,ua:{active:navigator.userActivation.isActive,ever:navigator.userActivation.hasBeenActive}}}","userGesture":true,"returnByValue":true}}
{"method":"Input.dispatchKeyEvent","params":{"type":"keyDown","key":"Enter","code":"Enter","windowsVirtualKeyCode":13,"text":"\r"}}
{"method":"Input.dispatchKeyEvent","params":{"type":"keyUp","key":"Enter","code":"Enter","windowsVirtualKeyCode":13,"text":""}}
```

Button FG returned `{"active":"button","ua":{"active":true,"ever":true}}`; its click log was `trusted:false, detail:0, pointerType:"", active:"button"`. Button Enter's click was `trusted:true, detail:0, pointerType:""`. Complete results are linked above.

Reproduce from this branch without putting generated profiles in the repo:

```sh
D=$(mktemp -d /tmp/nonpointer-cdp.XXXXXX)
cp research/evidence/nonpointer-cdp/harness.mjs "$D/"
mkdir -p "$D/home/.pki/nssdb"
openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout "$D/key.pem" -out "$D/cert.pem" -days 2 \
  -subj '/CN=nonpointer-cdp-owned-fixture' \
  -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1'
certutil -N -d "sql:$D/home/.pki/nssdb" --empty-password
certutil -A -d "sql:$D/home/.pki/nssdb" -n nonpointer-owned-fixture \
  -t 'C,,' -i "$D/cert.pem"
node "$D/harness.mjs" > "$D/run.log" 2>&1
# Inspect $D/results.json, $D/security.json, $D/run.log; then remove owned $D.
```

The harness uses a fresh Chrome profile and temporary HOME/NSS trust store, closes its contexts/process, and serves only owned fixture/destination pages. Final replay binds HTTPS to loopback. The first run bound the fixture server to all interfaces; browser requests were still only localhost/127.0.0.1. Chrome flags: `--headless=new --window-size=1200,900 --remote-debugging-address=127.0.0.1 --remote-debugging-port=0 --user-data-dir=<owned> --no-first-run --no-default-browser-check --no-proxy-server --site-per-process about:blank`. No `--no-sandbox`, certificate bypass, web-security bypass, or popup-blocking bypass.

Observed `chrome://sandbox`: **Namespace**, PID/network namespaces **Yes**, Seccomp-BPF/TSYNC **Yes**, “You are adequately sandboxed.” Chrome security state: **secure**, **TLS 1.3**, `AES_256_GCM`, no security issue IDs. Root trust existed only in the disposable HOME; no personal profiles or system trust changes. No owned Chrome processes remained after the runs.

Final `results.json` SHA-256: `e51024dffec78415c35e4ff3c54a2457bbc25ae76f57d72bbd3680e01c902457`. Harness SHA-256: `24cea323ee99dc841f70eabca9a2e17bfb9e526952ab20239f6713ad19a78c2d`.

## Limitations

One headless Chrome build/platform and controlled fixtures, not a cross-platform compatibility suite. No Workday reproduction, personal site/profile, public submission, native file upload, full picker interaction, permission prompt, IME, framework widget, sandboxed-frame permission-policy matrix, focus-removal race, or timing guarantee. Frame/OOPIF routing was measured with site isolation forced; shadow targets were seeded by fixture references before backend-node resolution, not discovered by a production AX-ID pipeline. Main-world listeners observed events while actions ran in isolated worlds. Closed-shadow internal events were retargeted at the outside logger. Headless `:open` and file-chooser interception establish browser state/requests, not visible OS UI equivalence. Synthetic fixture handlers demonstrate failure modes, not their prevalence on real sites. **No automatic fallback or new product behavior has been implemented or approved.**
