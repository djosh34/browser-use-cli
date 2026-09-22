package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/djosh34/browser-use-cli/cdp"
)

const helpText = `Usage: browser-use-cli [flags] COMMAND [flags] [arguments]

Commands:
  pages
  open URL
  read [--selector CSS]
  controls [--verbose]
  click CONTROL_REF
  fill CONTROL_REF TEXT
  press KEY
  select CONTROL_REF VALUE_OR_LABEL
  eval JAVASCRIPT

Shared flags:
  --endpoint URL      Browser HTTP(S) or WS(S) endpoint; overrides BROWSER_CDP_URL
  --page PAGE_ID      Explicit tab (references already identify their tab)
  --json              Render captured result as JSON
  --timeout DURATION  Overall deadline (default 30s)
  --help, -h          Show this help
  --version           Show build version

Use -- before positional arguments that begin with a dash.
Open reuses the sole eligible tab, or creates one when there are zero or many.
Read, controls, press, and eval require --page unless one eligible tab exists.
`

type options struct {
	endpoint, page, selector     string
	json, verbose, help, version bool
	timeout                      time.Duration
}

func usage(message string) error { return &cdp.Error{Code: "invalid_input", Message: message} }
func flags(o *options, command string) *flag.FlagSet {
	f := flag.NewFlagSet("browser-use-cli", flag.ContinueOnError)
	// Flag's default diagnostics can quote argument values or endpoint defaults.
	f.SetOutput(io.Discard)
	f.StringVar(&o.endpoint, "endpoint", o.endpoint, "")
	f.StringVar(&o.page, "page", o.page, "")
	f.BoolVar(&o.json, "json", o.json, "")
	f.DurationVar(&o.timeout, "timeout", o.timeout, "")
	f.BoolVar(&o.help, "help", o.help, "")
	f.BoolVar(&o.help, "h", o.help, "")
	f.BoolVar(&o.version, "version", o.version, "")
	if command == "read" {
		f.StringVar(&o.selector, "selector", "", "")
	}
	if command == "controls" {
		f.BoolVar(&o.verbose, "verbose", false, "")
	}
	return f
}
func parse(args []string) (options, string, []string, error) {
	o := options{endpoint: os.Getenv("BROWSER_CDP_URL"), timeout: 30 * time.Second}
	global := flags(&o, "")
	if err := global.Parse(args); err != nil {
		return o, "", nil, usage("invalid flags; use --help")
	}
	if o.help || o.version {
		return o, "", nil, nil
	}
	rest := global.Args()
	if len(rest) == 0 {
		return o, "", nil, usage("a command is required; use --help")
	}
	command := rest[0]
	want := 0
	switch command {
	case "pages", "read", "controls":
	case "open", "click", "press", "eval":
		want = 1
	case "fill", "select":
		want = 2
	default:
		return o, "", nil, usage("unknown command; use --help")
	}
	local := flags(&o, command)
	if err := local.Parse(rest[1:]); err != nil {
		return o, "", nil, usage("invalid command flags; use --help")
	}
	positional := local.Args()
	if o.help || o.version {
		return o, command, positional, nil
	}
	if len(positional) != want {
		return o, command, nil, usage("wrong number of command arguments; use --help")
	}
	if o.timeout <= 0 {
		return o, command, nil, usage("timeout must be positive")
	}
	switch command {
	case "click", "fill", "select":
		id, err := cdp.ControlRef(positional[0]).PageID()
		if err != nil {
			return o, command, nil, err
		}
		if o.page != "" && cdp.PageID(o.page) != id {
			return o, command, nil, usage("--page conflicts with the control reference")
		}
		o.page = string(id)
	}
	return o, command, positional, nil
}

func invoke(ctx context.Context, c *cdp.Client, command string, o options, args []string) (fmt.Stringer, error) {
	if command == "pages" {
		return c.Pages(ctx)
	}
	if command == "open" {
		var p *cdp.Page
		var err error
		if o.page == "" {
			p, err = c.Open(ctx, args[0])
		} else {
			p, err = c.Page(ctx, cdp.PageID(o.page))
			if err == nil {
				err = p.Navigate(ctx, args[0])
			}
		}
		if err != nil {
			return nil, err
		}
		return p.Info(ctx)
	}
	p, err := c.Page(ctx, cdp.PageID(o.page))
	if err != nil {
		return nil, err
	}
	switch command {
	case "read":
		return p.Read(ctx, cdp.ReadOptions{Selector: o.selector})
	case "controls":
		return p.Controls(ctx, cdp.ControlsOptions{Verbose: o.verbose})
	case "click":
		return p.Click(ctx, cdp.ControlRef(args[0]))
	case "fill":
		return p.Fill(ctx, cdp.ControlRef(args[0]), args[1])
	case "press":
		return p.Press(ctx, args[0])
	case "select":
		return p.Select(ctx, cdp.ControlRef(args[0]), args[1])
	case "eval":
		return p.Eval(ctx, args[0])
	}
	return nil, usage("unknown command")
}
func diagnostic(w io.Writer, asJSON bool, err error, interrupted bool) int {
	code, message, exit := "operation", "browser operation failed", 1
	var typed *cdp.Error
	switch {
	case interrupted:
		code, message, exit = "interrupted", "interrupted by SIGINT", 130
	case errors.Is(err, context.DeadlineExceeded):
		code, message = "timeout", "operation deadline exceeded"
	case errors.Is(err, context.Canceled):
		code, message = "canceled", "operation canceled"
	case errors.As(err, &typed):
		code, message = typed.Code, typed.Message
		if code == "invalid_input" || code == "ambiguous" {
			exit = 2
		}
	}
	if asJSON {
		json.NewEncoder(w).Encode(struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{code, message})
	} else {
		fmt.Fprintf(w, "%s: %s\n", code, message)
	}
	return exit
}
func run(args []string, stdout, stderr io.Writer) int {
	o, command, positional, err := parse(args)
	if err != nil {
		return diagnostic(stderr, o.json, err, false)
	}
	if o.help {
		if _, err := io.WriteString(stdout, helpText); err != nil {
			return diagnostic(stderr, o.json, &cdp.Error{Code: "output", Message: "cannot write command output"}, false)
		}
		return 0
	}
	if o.version {
		version := "devel"
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
		if _, err := fmt.Fprintln(stdout, "browser-use-cli", version); err != nil {
			return diagnostic(stderr, o.json, &cdp.Error{Code: "output", Message: "cannot write command output"}, false)
		}
		return 0
	}
	if o.endpoint == "" {
		return diagnostic(stderr, o.json, usage("--endpoint or BROWSER_CDP_URL is required"), false)
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, o.timeout)
	defer cancel()
	c, err := cdp.Connect(ctx, o.endpoint)
	if err != nil {
		return diagnostic(stderr, o.json, err, signalCtx.Err() != nil)
	}
	defer c.Close()
	result, err := invoke(ctx, c, command, o, positional)
	if err != nil {
		return diagnostic(stderr, o.json, err, signalCtx.Err() != nil)
	}
	if signalCtx.Err() != nil {
		return diagnostic(stderr, o.json, signalCtx.Err(), true)
	}
	if o.json {
		err = json.NewEncoder(stdout).Encode(result)
	} else {
		_, err = fmt.Fprintln(stdout, result.String())
	}
	if err != nil {
		return diagnostic(stderr, o.json, &cdp.Error{Code: "output", Message: "cannot write command output"}, false)
	}
	return 0
}
func main() {
	signal.Ignore(syscall.SIGPIPE) // A broken pipe becomes an ordinary output error.
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
