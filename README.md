# browser-use-cli

A small Chrome automation CLI and reusable Go `cdp` package. Read Chrome's native accessibility tree, then act using numeric control IDs.

## Install

Requires Go 1.26 or newer. Build targets: Linux and macOS, amd64 and arm64.

```sh
go install github.com/djosh34/browser-use-cli@v0.2.0
# Or follow the latest release:
go install github.com/djosh34/browser-use-cli@latest
```

The executable goes into `GOBIN`, or `$(go env GOPATH)/bin`. Add that directory to `PATH`.

## Connect to Chrome

Start Chrome separately with remote debugging and a dedicated, non-default profile. For example, on Linux:

```sh
google-chrome --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=9222 --user-data-dir="$(mktemp -d)"
export BROWSER_CDP_URL=http://127.0.0.1:9222
browser-use-cli pages
```

Use your Chrome executable path on macOS. Keep the sandbox enabled. The CLI does not launch Chrome, change its viewport, find profiles, or close the browser or its tabs. You own process and profile cleanup.

CDP grants control of the browser. Do not expose an unauthenticated debugging port to the internet or use a personal profile for untrusted automation.

`--endpoint URL` overrides `BROWSER_CDP_URL`. Supply a browser-level HTTP/HTTPS discovery URL or WS/WSS URL, not a page-level WebSocket URL. Discovery appends `/json/version` to the base path and preserves its query. Direct WebSocket paths and queries stay unchanged. TLS verification remains enabled. Cross-origin redirects, TLS downgrades and credential forwarding to another origin are rejected. If a proxy returns an unusable discovery address, supply its exact browser WebSocket URL instead.

## Commands

```sh
browser-use-cli pages
browser-use-cli open https://example.com
browser-use-cli read --page 1
browser-use-cli read --page 1 --controls-only
browser-use-cli read --page 1 --selector main --json
browser-use-cli click --page 1 4
browser-use-cli fill --page 1 2 'new text'
browser-use-cli fill --page 1 2 ''
browser-use-cli select --page 1 3 'Visible option label'
browser-use-cli press --page 1 Enter
browser-use-cli press --page 1 Control+A
browser-use-cli eval --page 1 'document.title'
```

Use page numbers from `pages` and control numbers from `read`. Both start at 1 and are JSON numbers. **Numbers are current positions, not durable references.** Every command freshly enumerates eligible tabs; every targeted action freshly enumerates controls. Page changes can make the same number refer to a different target. Read again when needed. Within an action, the resolved actual node/tab stays bound: disappearance fails rather than silently retargeting a replacement.

Omit `--page` only when exactly one eligible tab exists. `open` without `--page` reuses that sole tab; with zero or multiple tabs it creates one. `open --page 1 URL` navigates the explicit tab. An unavailable explicit page never falls back. Ordinary web pages and `about:blank` are eligible; browser settings, devtools and extension targets are not. There is no saved current tab, automatic read after actions, or wait command.

Shared flags are `--endpoint`, `--page`, `--json` and `--timeout`. The default deadline is 30 seconds; use a Go duration such as `--timeout 10s`. Put flags before the command or between the command and its positional arguments. Use `--` before positional arguments that start with a dash. `--help` and `--version` need no browser. The former `controls` command, opaque references and `--verbose` discovery are removed.

Text is the default output. `--json` serializes the same captured result without another browser read. Successful output goes to stdout. Diagnostics go to stderr, with `code` and `message` fields under `--json`. Exit codes: 0 success, 2 invalid/ambiguous input, 1 operational failure, 130 SIGINT. Diagnostics are best-effort when stderr is blocked.

### Reading

`read` preserves Chrome's accessible hierarchy and distinct content: text, headings, names, descriptions, values and states, including exposed offscreen, unnamed and disabled controls. Same-process frames, out-of-process frames and exposed open/closed shadow content participate in one tree and one preorder control sequence. Native select options remain present when Chrome exposes them, even while collapsed. There is no DOM/layout-based control discovery, relevance ranking, modal-only pruning or automatic output truncation.

Context nodes have no ID. Direct interactive roles and editable roots receive IDs; static text and collection-only groups do not. Password/protected values are omitted entirely, including masked values. Ordinary field values remain visible. Mechanical inline text repetition and an exact sole plain-text child matching its parent's name are removed; distinct semantic children and descriptions remain.

```text
Page 1 · Example · https://example.com/

document "Example"
└─ dialog "Cookies"
   ├─ text "Choose which cookies to allow."
   ├─ [1] button "Accept"
   └─ [2] button "Reject optional"
```

JSON uses `page`, `complete`, `tree` and optional `warnings`. Nodes contain `role`, optional numeric `id`, `children` and applicable `name`, `text`, `description`, `value`, `url`, `level`, `state` fields. Unknown state differs from explicit false; mixed checked/pressed and invalid grammar/spelling states are preserved. Unavailable child documents produce `complete:false` with warnings; main-document failure is an error. No controls is not an error.

`--controls-only` retains controls and their ancestor paths. `--selector CSS` applies independently in reachable documents and author shadow scopes, unions matching accessible regions and retains ancestor paths. AX subtrees determine content, including accessibility ownership/reparenting. Both filters preserve page-wide control IDs and can be combined. Invalid CSS or no DOM matches is an error; matches with no accessible content yield `tree:null`. Filters do not make inaccessible DOM content accessible.

### Input

`press` accepts one character, Enter, Tab, Escape, Space, arrow keys, Home, End, PageUp, PageDown, Backspace, Delete and Insert. Combine with Control, Alt, Meta or Shift, such as `Shift+Tab`. Ctrl and Cmd are aliases.

`click` performs coordinate-free DOM activation with user activation. It attempts browser-managed focus first, but a non-focusable target can still be activated. Site-directed focus changes are left intact. This is **not an OS accessibility action or mouse-event emulation**: the click is untrusted and has no pointer-down/up sequence. Trust-gated or pointer-only handlers and some native picker/option actions therefore differ. Useful native defaults such as links, form submission and checkbox toggling remain browser-managed. There is no pointer fallback or automatic alternative activation.

`fill` replaces supported input, textarea or contenteditable text through browser input. It must retain the intended target's focus and full replacement selection; it fails rather than typing into another field or knowingly inserting only part of a replacement. `select` requires a native select and an exact option value or label. Missing, ambiguous and AX-disabled choices fail. It attempts focus but operates on the bound select even if the site moves focus elsewhere. An unchanged selection emits no events; changed selection uses the DOM setter and input/change events, which are not trusted browser input. Use click/fill/press for custom widgets. `press` sends keyboard input to actual browser focus.

Chrome's exposed accessibility tree and state are the sole discovery and semantic eligibility authority. AX-disabled controls reject actions; AX-readonly constrains editing, not ordinary activation. Overlays, pointer-events, clipping, offscreen position and frame transforms do not veto element actions. There is no separate scroll-to-click prerequisite, although browser-managed focus may naturally scroll. Native capabilities still determine which operations an element supports. A vanished target or protocol failure is an error, not a visual-actionability rule. Actions never automatically replay uncertain input. Native dialogs interrupt with an error and are not accepted automatically.

Actions return a small result with the action, page metadata, optional target control number, newly observed `newPages` and any discovery warnings. They do not return a page tree or echo filled text. A completed input is **not a claim of application business success**. Open/Navigate wait for the requested document load; input waits for bounded observed navigation and immediate rendering/tasks. Later timers, suggestions, fetches and SPA changes may require a later Read. New tabs do not change a saved selection.

## Go library

Import `github.com/djosh34/browser-use-cli/cdp` without importing the executable:

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "time"

    "github.com/djosh34/browser-use-cli/cdp"
)

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    client, err := cdp.Connect(ctx, os.Getenv("BROWSER_CDP_URL"))
    if err != nil { log.Fatal(err) }
    defer client.Close()
    page, err := client.Page(ctx, 0) // Sole eligible tab; otherwise pass a PageID.
    if err != nil { log.Fatal(err) }
    result, err := page.Read(ctx, cdp.ReadOptions{ControlsOnly: true})
    if err != nil { log.Fatal(err) }
    fmt.Println(result)
}
```

`Client` provides `Pages`, `Page` and `Open`. `Page` provides `Info`, `Navigate`, `Read`, `Click`, `Fill`, `Press`, `Select` and `Eval`. `PageID` and `ControlID` are integer types. Pass a node's `ID` to Click/Fill/Select. A Go `Page` handle stays bound to the actual selected tab; its reported ordinal can change as tabs change.

Results are typed captured values. Their `String` methods do no browser I/O; `encoding/json` uses the same data. Contexts bound operations, without a library-wide 30-second default. Canceling the Connect context after connection does not close the client. `Close` releases the connection, not tabs. Use `errors.Is` for context cancellation/deadlines and `errors.As` with `*cdp.Error` for errors such as `stale`, `blocked`, `dialog`, `unavailable` and `overflow`.

Operations on one page serialize within a Client; separate clients/processes are not globally exclusive. Private resource limits fail explicitly rather than silently dropping events or content.

Eval executes caller-supplied JavaScript and can mutate the page. It copies supported values, preserves undefined, NaN, infinities, negative zero and BigInt, and rejects functions, symbols, cyclic or otherwise lossy values. Exceptions are errors; private remote handles do not escape. Unlike Read and input results, Eval can return sensitive data if explicitly requested.

Sites may be unavailable or restrict automation. The CLI does not bypass verification, login walls or access blocks. There are no cookie, permission, screenshot, network-capture or download-management commands. Read does not trigger lazy loading or claim that inaccessible DOM/canvas content is accessible.

## Tests

```sh
go vet ./...
go test ./...
go test -race ./...
CHROME_BIN=/absolute/path/to/chrome go test -tags=integration -race ./...
```

Integration tests use real disposable Chrome profiles and installed CLI processes, with the sandbox enabled and an explicit desktop viewport. Missing `CHROME_BIN` is a failure, not a skipped pass. Cross-compilation does not establish live platform support.

## License

[MIT](LICENSE). Dependency and Go runtime notices are in [THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt).
