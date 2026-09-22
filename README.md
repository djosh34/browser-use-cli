# browser-use-cli

## Install

Requires Go 1.26 or newer. Targets Linux and macOS on amd64 and arm64.

```sh
go install github.com/djosh34/browser-use-cli@latest
```

The executable goes into `GOBIN`, or `$(go env GOPATH)/bin` when `GOBIN` is unset. Add that directory to `PATH`.

## Connect to Chrome

Start Chrome separately with remote debugging and a dedicated, non-default profile. For example, on Linux:

```sh
google-chrome --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=9222 --user-data-dir="$(mktemp -d)"
export BROWSER_CDP_URL=http://127.0.0.1:9222
browser-use-cli pages
```

Use your Chrome executable path on macOS. Keep the sandbox enabled. The CLI does not launch Chrome, find profiles, or close the browser or its tabs. You own the browser process and profile cleanup.

CDP grants control of the browser. Do not expose an unauthenticated debugging port to the internet or use a personal profile for untrusted automation.

`--endpoint URL` overrides `BROWSER_CDP_URL`. Supply a browser-level HTTP/HTTPS discovery URL or WS/WSS URL, not a page-level WebSocket URL. Discovery appends `/json/version` to the supplied base path and preserves its query. Direct WebSocket paths and queries stay unchanged. TLS verification remains enabled. Cross-origin redirects, TLS downgrades and credential forwarding to another origin are rejected. If a proxy returns an unusable discovery address, supply its exact browser WebSocket URL instead.

## Commands

```sh
browser-use-cli pages
browser-use-cli open https://example.com
browser-use-cli open --page PAGE_ID https://example.com/path
browser-use-cli read --page PAGE_ID
browser-use-cli read --page PAGE_ID --selector main --json
browser-use-cli controls --page PAGE_ID --verbose
browser-use-cli click CONTROL_REF
browser-use-cli fill CONTROL_REF 'new value'
browser-use-cli fill CONTROL_REF ''
browser-use-cli press --page PAGE_ID Enter
browser-use-cli press --page PAGE_ID Control+A
browser-use-cli select CONTROL_REF 'Visible option label'
browser-use-cli eval --page PAGE_ID 'document.title'
```

Replace `PAGE_ID` with an ID from `pages`. Copy `CONTROL_REF` from a control's `target` in `controls` output. References identify a specific node and document, including its frame. Keep them opaque. After navigation or node replacement, collect fresh controls rather than substituting a similar name.

`open` without `--page` reuses the sole eligible tab. With zero or multiple eligible tabs, it creates a tab. An explicit page ID never falls back to another tab. `read`, `controls`, `press` and `eval` require `--page` unless exactly one eligible tab exists. Ordinary page targets and `about:blank` are eligible; browser settings, devtools and extension targets are not. `click`, `fill` and `select` use the reference's page and reject a conflicting `--page`. There is no saved current tab.

Shared flags are `--endpoint`, `--page`, `--json` and `--timeout`. The default deadline is 30 seconds. Use a Go duration such as `--timeout 10s`. Put flags before the command or between the command and its positional arguments. Use `--` before positional arguments that start with a dash. `--help` and `--version` need no browser.

Text is the default output. `--json` serializes the same captured result, without another browser read. Successful output goes to stdout. Diagnostics go to stderr, with stable `code` and `message` fields under `--json`. Exit codes are 0 for success, 2 for invalid or ambiguous input, 1 for operational failure and 130 for SIGINT. Diagnostics are best-effort when stderr is blocked.

`press` accepts one character, Enter, Tab, Escape, Space, arrow keys, Home, End, PageUp, PageDown, Backspace, Delete and Insert. Combine keys with Control, Alt, Meta or Shift, such as `Shift+Tab`. Ctrl and Cmd are modifier aliases. `fill` replaces supported input, textarea or contenteditable text through browser input. `select` requires a native select and an exact option value or label. Missing, ambiguous and disabled choices fail. Use click/fill/press for custom comboboxes.

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
    page, err := client.Page(ctx, "") // Select the sole eligible tab.
    if err != nil { log.Fatal(err) }
    result, err := page.Read(ctx, cdp.ReadOptions{})
    if err != nil { log.Fatal(err) }
    fmt.Println(result)
}
```

`Client` also provides `Pages` and `Open`. `Page` provides `Info`, `Navigate`, `Controls`, `Click`, `Fill`, `Press`, `Select` and `Eval`. `ControlsOptions{Verbose: true}` adds DOM-evidenced custom targets. Pass a chosen control's `Target` to an action. For a stored reference, `ControlRef.PageID()` extracts its page locally; the action still validates the live node and document.

Results are typed captured values. Their `String` methods are pure; `encoding/json` uses the same data. Contexts bound operations, without a library-wide 30-second default. Canceling the Connect context after connection does not close the client. `Close` releases the connection, not browser tabs. Use `errors.Is` for context cancellation/deadlines and `errors.As` with `*cdp.Error` for errors such as `stale`, `blocked`, `dialog`, `unavailable` and `overflow`.

## Behavior and limits

- Read includes offscreen rendered text, headings, lists and tables. It does not scroll to trigger lazy loading or extract text from images/canvas. `--selector` applies in reachable document and shadow scopes. Read and Controls preserve frame context and report unavailable frames explicitly. They traverse same-origin and cross-origin frames and shadow content where Chrome exposes it.
- Controls includes semantic/native controls by default. Verbose discovery is conservative, not a guarantee of every custom handler. A pointer cursor or delegated page-wide listener alone is insufficient. Context helps distinguish repeated links and unnamed fields, but complex cards can still be repetitive or poorly named. Disabled, hidden and inert controls are excluded. Password values are omitted from ordinary Read/Controls output.
- Observation does not prove actionability. Actions recheck identity and state, scroll as needed, and hit-test through frame/shadow and overlay chains. A blocked or stale target fails rather than selecting a nearby control. Readonly fields reject Fill. Native select changes use the DOM selection setter and input/change events; those select events are not trusted browser input.
- Open/Navigate wait for the requested main-frame document load. Input waits for observed action-triggered navigation and immediate rendering/tasks. A successful ActionResult is not application business success. Later timers, suggestions, fetches and SPA title/content changes may need a later Read/Controls. Observed new tabs appear in `newPages`; the CLI does not switch a saved selection.
- Operations on one page serialize within a Client. Separate clients/processes are not globally exclusive. Actions never automatically replay uncertain input. Native dialogs interrupt with an error and are not accepted automatically. Private resource bounds fail explicitly rather than silently dropping events or truncating results.
- Eval runs caller-supplied page JavaScript and can mutate the page. It copies supported values, preserves undefined, NaN, infinities, negative zero and BigInt, and rejects functions, symbols, cyclic or otherwise lossy values. Exceptions are errors; private remote handles do not escape.
- Sites may be unavailable or restrict automation. The CLI does not bypass verification, login walls or access blocks. There are no cookie, permission, screenshot, network-capture or download-management commands.

## License

[MIT](LICENSE). Dependency and Go runtime notices are in [THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt).
