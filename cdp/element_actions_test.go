//go:build integration

package cdp_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/djosh34/browser-use-cli/cdp"
)

// Exercise the same contract through the exported library and a freshly
// installed CLI. Every CLI action runs in a separate process, using observed
// numeric IDs; fixture scripts only arrange and report effects, never act on
// behalf of the caller.
type elementDriver struct {
	open     func(*testing.T, string)
	read     func(*testing.T) cdp.ReadResult
	readPage func(*testing.T, cdp.PageID) cdp.ReadResult
	act      func(*testing.T, string, cdp.ControlID, string, string) cdp.ActionResult
}

func elementDrivers(t *testing.T, test func(*testing.T, elementDriver)) {
	t.Helper()
	for _, seam := range []string{"Go", "CLI"} {
		t.Run(seam, func(t *testing.T) {
			endpoint := chrome(t, "--site-per-process", "about:blank")
			if seam == "Go" {
				c := browserClient(t, endpoint)
				var p *cdp.Page
				d := elementDriver{}
				d.open = func(t *testing.T, url string) {
					t.Helper()
					var err error
					p, err = c.Open(testContext(t), url)
					if err != nil {
						t.Fatal(err)
					}
				}
				d.read = func(t *testing.T) cdp.ReadResult {
					t.Helper()
					r, err := p.Read(testContext(t), cdp.ReadOptions{})
					if err != nil {
						t.Fatal(err)
					}
					return r
				}
				d.readPage = func(t *testing.T, id cdp.PageID) cdp.ReadResult {
					t.Helper()
					page, err := c.Page(testContext(t), id)
					if err != nil {
						t.Fatal(err)
					}
					r, err := page.Read(testContext(t), cdp.ReadOptions{})
					if err != nil {
						t.Fatal(err)
					}
					return r
				}
				d.act = func(t *testing.T, action string, id cdp.ControlID, value, code string) cdp.ActionResult {
					t.Helper()
					var r cdp.ActionResult
					var err error
					switch action {
					case "click":
						r, err = p.Click(testContext(t), id)
					case "fill":
						r, err = p.Fill(testContext(t), id, value)
					case "select":
						r, err = p.Select(testContext(t), id, value)
					case "press":
						r, err = p.Press(testContext(t), value)
					default:
						t.Fatalf("unknown fixture action %q", action)
					}
					if code == "" {
						if err != nil {
							t.Fatal(err)
						}
					} else {
						var typed *cdp.Error
						if !errors.As(err, &typed) || typed.Code != code {
							t.Fatalf("%s: want %s, got %v", action, code, err)
						}
					}
					return r
				}
				test(t, d)
				return
			}
			binary := cliBinary(t)
			d := elementDriver{}
			d.open = func(t *testing.T, url string) { t.Helper(); runCLI(t, binary, endpoint, 0, "open", url) }
			d.read = func(t *testing.T) cdp.ReadResult { t.Helper(); return cliRead(t, binary, endpoint, "--page", "1") }
			d.readPage = func(t *testing.T, id cdp.PageID) cdp.ReadResult {
				t.Helper()
				return cliRead(t, binary, endpoint, "--page", strconv.Itoa(int(id)))
			}
			d.act = func(t *testing.T, action string, id cdp.ControlID, value, code string) cdp.ActionResult {
				t.Helper()
				args := []string{action, "--json", "--page", "1"}
				if action != "press" {
					args = append(args, strconv.Itoa(int(id)))
				}
				if action != "click" {
					args = append(args, value)
				}
				exit := 0
				if code != "" {
					exit = 1
					if code == "ambiguous" || code == "invalid_input" {
						exit = 2
					}
				}
				output := runCLI(t, binary, endpoint, exit, args...)
				if code != "" {
					if !strings.Contains(output, `"code":"`+code+`"`) {
						t.Fatalf("%s: want %s, got %s", action, code, output)
					}
					return cdp.ActionResult{}
				}
				var r cdp.ActionResult
				if err := json.Unmarshal([]byte(output), &r); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(output, `"tree"`) {
					t.Fatalf("action performed an automatic read: %s", output)
				}
				return r
			}
			test(t, d)
		})
	}
}

func (d elementDriver) target(t *testing.T, name string) cdp.ControlID {
	t.Helper()
	r := d.read(t)
	for _, n := range controlNodes(r.Tree) {
		if n.Name == name {
			return n.ID
		}
	}
	t.Fatalf("missing control %q: %s", name, r)
	return 0
}
func (d elementDriver) title(t *testing.T, want string) {
	t.Helper()
	if r := d.read(t); r.Page.Title != want {
		t.Fatalf("effect: title = %q, want %q; %s", r.Page.Title, want, r)
	}
}
func (d elementDriver) value(t *testing.T, name, want string) {
	t.Helper()
	r := d.read(t)
	for _, n := range controlNodes(r.Tree) {
		if n.Name == name {
			if (n.Value == nil && want != "") || (n.Value != nil && *n.Value != want) {
				t.Fatalf("%s value = %v, want %q; %s", name, n.Value, want, r)
			}
			return
		}
	}
	t.Fatalf("missing %q: %s", name, r)
}

func elementFixture(t *testing.T, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>Untouched</title>`+body)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestElementActionsIgnorePointerGeometry(t *testing.T) {
	elementDrivers(t, func(t *testing.T, d elementDriver) {
		for _, geometry := range []struct{ name, prefix, suffix string }{
			{"covered", "", `<div style="position:fixed;inset:0;z-index:999;background:white"></div>`},
			{"pointer none", `<div style="pointer-events:none">`, `</div>`},
			{"offscreen", `<div style="position:fixed;left:-2000px;top:-2000px">`, `</div>`},
			{"clipped", `<div style="width:0;height:0;overflow:clip">`, `</div>`},
		} {
			for _, tc := range []struct{ action, body, value, title string }{
				{"click", `<button aria-label="Target" onclick="document.title='clicked'">Target</button>`, "", "clicked"},
				{"fill", `<input aria-label="Target" value="old" oninput="document.title='filled'">`, "replacement 日本語", "filled"},
				{"select", `<select aria-label="Target" onchange="document.title='selected:'+this.value"><option value="a">Alpha</option><option value="b">Beta</option></select>`, "Beta", "selected:b"},
			} {
				t.Run(geometry.name+"/"+tc.action, func(t *testing.T) {
					fixture := elementFixture(t, geometry.prefix+tc.body+geometry.suffix)
					d.open(t, fixture.URL)
					id := d.target(t, "Target")
					result := d.act(t, tc.action, id, tc.value, "")
					if result.Action != tc.action || result.Target != id || result.Page.ID != 1 || len(result.NewPages) != 0 {
						t.Fatalf("compact outcome changed: %+v", result)
					}
					d.title(t, tc.title)
					if tc.action == "fill" || tc.action == "select" {
						d.value(t, "Target", tc.value)
					}
				})
			}
		}
		t.Run("no layout box", func(t *testing.T) {
			fixture := elementFixture(t, `<button style="display:contents" onclick="document.title='clicked'">Target</button>`)
			d.open(t, fixture.URL)
			d.act(t, "click", d.target(t, "Target"), "", "")
			d.title(t, "clicked")
		})
	})
}
