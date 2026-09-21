# Browser CLI prototype

A disposable Go CLI that connects to an existing Chrome CDP endpoint.

Run commands from this directory:

```sh
export BROWSER_CDP_URL=http://127.0.0.1:9222
go run . pages
go run . open https://www.weather.gov/
go run . read --page PAGE_ID
go run . controls --page PAGE_ID
go run . controls --page PAGE_ID --verbose
go run . click 'TARGET_FROM_CONTROLS'
go run . fill 'TARGET_FROM_CONTROLS' 'Seattle, WA'
go run . press --page PAGE_ID Enter
```

`open` creates a tab. With `--page`, it navigates that tab instead. `click`, `fill`, and `select` take the complete target printed by `controls`. Targets include the page and document identity. A navigation invalidates them.

`read` and `controls` return Go values that implement `fmt.Stringer`. Add `--json` to serialize the same result as JSON. Put flags before positional arguments.

`read` currently returns rendered body text without child-frame contents. Control discovery and actions currently operate on the main document. Offscreen controls appear in both modes. Actions scroll their target into view and check a point before dispatch. This does not guarantee that a page will remain unchanged between the check and the input event.

`press` sends a key to the focused element. `select` changes a native dropdown value or option label. `eval` runs a JavaScript expression in the page.

The protocol client is sequential and discards unsolicited events. There is no daemon, saved current page, automatic retry, or production reliability claim.

Browser downloads, profiles, binaries, and captured output belong in the ignored `.runtime/` directory. They are not part of the source.
