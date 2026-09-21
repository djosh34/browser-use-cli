// browser-cli-prototype is an experimental Chrome CDP command line program.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"browser-cli-prototype/cdp"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

const usage = `Usage: go run . COMMAND [flags] [arguments]

  pages
  open URL
  read
  controls [--verbose]
  click TARGET
  fill TARGET TEXT
  press KEY
  select TARGET VALUE
  eval EXPRESSION

Flags precede positional arguments:
  --endpoint URL   existing Chrome CDP endpoint, or BROWSER_CDP_URL
  --page ID        page from pages, or BROWSER_CDP_PAGE
  --json           JSON instead of readable text
  --timeout 30s    command deadline

Click, fill, and select use a target copied from controls.
Open creates a tab unless --page is supplied.
`

type actionResult struct {
	Action           string     `json:"action"`
	Dispatched       bool       `json:"dispatched"`
	State            *cdp.State `json:"state,omitempty"`
	ObservationError string     `json:"observationError,omitempty"`
}

func (r actionResult) String() string {
	s := r.Action + " dispatched"
	if r.State != nil {
		return s + "\n" + r.State.String()
	}
	return s + "\nstate unavailable: " + r.ObservationError
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	command := args[0]
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	endpoint := f.String("endpoint", os.Getenv("BROWSER_CDP_URL"), "Chrome endpoint")
	pageID := f.String("page", os.Getenv("BROWSER_CDP_PAGE"), "page ID")
	asJSON := f.Bool("json", false, "JSON output")
	verbose := f.Bool("verbose", false, "include custom click targets")
	timeout := f.Duration("timeout", 30*time.Second, "command deadline")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *endpoint == "" {
		return errors.New("set BROWSER_CDP_URL or pass --endpoint; this prototype does not launch Chrome")
	}
	pos := f.Args()
	arity := map[string]int{"pages": 0, "open": 1, "read": 0, "controls": 0, "click": 1, "fill": 2, "press": 1, "select": 2, "eval": 1}
	n, ok := arity[command]
	if !ok {
		return fmt.Errorf("unknown command %q\n%s", command, usage)
	}
	if len(pos) != n {
		return fmt.Errorf("%s requires %d arguments\n%s", command, n, usage)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client, err := cdp.Connect(ctx, *endpoint)
	if err != nil {
		return err
	}
	defer client.Close()
	var result any
	if command == "pages" {
		result, err = client.Pages(ctx)
	} else {
		var page *cdp.Page
		if command == "open" && *pageID == "" {
			page, err = client.Open(ctx, pos[0])
		} else {
			if command == "click" || command == "fill" || command == "select" {
				id, e := cdp.TargetPage(pos[0])
				if e != nil {
					return e
				}
				if *pageID != "" && *pageID != id {
					return errors.New("target and --page disagree")
				}
				*pageID = id
			}
			page, err = client.Page(ctx, *pageID)
		}
		if err != nil {
			return err
		}
		switch command {
		case "open":
			if *pageID != "" {
				err = page.Navigate(ctx, pos[0])
			}
		case "read":
			result, err = page.Read(ctx)
		case "controls":
			result, err = page.Controls(ctx, *verbose)
		case "click":
			err = page.Click(ctx, pos[0])
		case "fill":
			err = page.Fill(ctx, pos[0], pos[1])
		case "press":
			err = page.Press(ctx, pos[0])
		case "select":
			err = page.Select(ctx, pos[0], pos[1])
		case "eval":
			var value json.RawMessage
			err = page.Eval(ctx, pos[0], &value)
			result = value
		}
		if err == nil && result == nil {
			a := actionResult{Action: command, Dispatched: true}
			state, e := page.State(ctx)
			if e != nil {
				a.ObservationError = e.Error()
			} else {
				a.State = &state
			}
			result = a
		}
	}
	if err != nil {
		return err
	}
	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	switch r := result.(type) {
	case fmt.Stringer:
		_, err = fmt.Fprintln(os.Stdout, r.String())
	case []cdp.PageInfo:
		var lines []string
		for _, p := range r {
			lines = append(lines, p.String())
		}
		_, err = fmt.Fprintln(os.Stdout, strings.Join(lines, "\n"))
	case json.RawMessage:
		_, err = fmt.Fprintln(os.Stdout, string(r))
	default:
		err = fmt.Errorf("no text representation for %T", result)
	}
	return err
}
