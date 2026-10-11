// Command cpsctl administers a running Cloud Print Service over its HTTP API.
//
//	cpsctl token issue  --client-id wms --role client --ttl 720h --note "pilot"
//	cpsctl token list
//	cpsctl token rotate tok_1_abc
//	cpsctl token revoke tok_1_abc
//	cpsctl printers
//	cpsctl status
//	cpsctl test wh1-pack-03
//
// Credentials come from flags or the environment:
//
//	CPSCTL_URL, CPSCTL_ADMIN_KEY, CPSCTL_TOKEN (for read-only commands)
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "token":
		err = tokenCmd(args)
	case "agent":
		err = agentCmd(args)
	case "discover":
		err = discoverCmd(args)
	case "printers":
		err = printersCmd(args)
	case "status":
		err = statusCmd(args)
	case "test":
		err = testCmd(args)
	case "help", "-h", "--help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `cpsctl — administer Cloud Print Service

Commands:
  token issue   --client-id <id> [--role client|admin] [--ttl 720h] [--note ...]
  token list    [--client-id <id>]
  token rotate  <token-id>
  token revoke  <token-id>
  agent enroll  [--id pc7] [--site wh1] [--ttl 8760h]     one-time token for an edge agent
  agent list                                          enrolled agents and health
  agent revoke   <agent-id>                             drop an agent and its printers
  discover list                                        discovered-but-not-adopted devices
  discover adopt  <device-key> --printer-id wh1-pack-04 --site wh1
  discover dismiss <device-key>
  printers                       list the fleet with status
  status                         service health
  test <printer-id>              print a test label

Common flags:
  --url <base>       default $CPSCTL_URL or http://localhost:8080
  --key <secret>     admin credential, default $CPSCTL_ADMIN_KEY
`)
}

// ---------------------------------------------------------------- plumbing

type client struct {
	base    string
	key     string
	http    *http.Client
	flagURL *string
	flagKey *string
}

// newClient registers the common --url/--key flags; call resolve() after Parse.
func newClient(fs *flag.FlagSet) *client {
	return &client{
		http:    &http.Client{Timeout: 30 * time.Second},
		flagURL: fs.String("url", env("CPSCTL_URL", "http://localhost:8080"), "service base URL"),
		flagKey: fs.String("key", env("CPSCTL_ADMIN_KEY", ""), "admin credential (or $CPSCTL_ADMIN_KEY)"),
	}
}

// resolve applies flag values after parsing.
func (c *client) resolve() {
	if c.flagURL != nil && *c.flagURL != "" {
		c.base = strings.TrimRight(*c.flagURL, "/")
	}
	if c.flagKey != nil {
		c.key = *c.flagKey
	}
}

func (c *client) do(method, path string, body any) (map[string]any, error) {
	if c.key == "" {
		c.key = os.Getenv("CPSCTL_ADMIN_KEY")
	}
	if c.key == "" {
		return nil, fmt.Errorf("no credential: pass --key or set CPSCTL_ADMIN_KEY")
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if resp.StatusCode >= 400 {
		if e, ok := out["error"].(map[string]any); ok {
			return out, fmt.Errorf("%s: %v", e["code"], e["message"])
		}
		return out, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return out, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---------------------------------------------------------------- commands

func tokenCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cpsctl token issue|list|rotate|revoke ...")
	}
	sub := args[0]
	fs := flag.NewFlagSet("token "+sub, flag.ExitOnError)
	c := newClient(fs)

	// All flags must exist before Parse; each subcommand uses the subset it needs.
	clientID := fs.String("client-id", "", "business system identity, e.g. wms")
	role := fs.String("role", "client", "client|admin")
	ttl := fs.String("ttl", "", "lifetime, e.g. 720h or 90d (default: service default)")
	note := fs.String("note", "", "free-form note, e.g. ticket id")

	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	c.resolve()

	switch sub {
	case "issue":
		if *clientID == "" {
			return fmt.Errorf("--client-id is required")
		}
		body := map[string]any{"client_id": *clientID, "role": *role, "note": *note}
		if *ttl != "" {
			body["ttl"] = *ttl
		}
		out, err := c.do("POST", "/api/v1/admin/tokens", body)
		if err != nil {
			return err
		}
		secret, _ := out["token"].(string)
		fmt.Printf("token issued\n")
		fmt.Printf("  id          %v\n", out["id"])
		fmt.Printf("  client      %v (%v)\n", out["client_id"], out["role"])
		fmt.Printf("  expires     %v\n", out["expires_at"])
		fmt.Printf("  prefix      %v\n", out["prefix"])
		fmt.Printf("\n  SECRET (shown once): %s\n\n", secret)
		fmt.Printf("  Hand this to the integration team, then purge it from every log.\n")
		return nil

	case "list":
		q := url.Values{}
		if *clientID != "" {
			q.Set("client_id", *clientID)
		}
		path := "/api/v1/admin/tokens"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		out, err := c.do("GET", path, nil)
		if err != nil {
			return err
		}
		items, _ := out["items"].([]any)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tCLIENT\tROLE\tPREFIX\tEXPIRES\tSTATUS\tUSES")
		for _, it := range items {
			m := it.(map[string]any)
			status := "active"
			if m["revoked_at"] != nil {
				status = "revoked"
			} else if exp, ok := m["expires_at"].(string); ok && exp != "" {
				if t, err := time.Parse(time.RFC3339, exp); err == nil && time.Now().After(t) {
					status = "expired"
				}
			}
			exp := "never"
			if v, ok := m["expires_at"].(string); ok && v != "" {
				exp = v[:min(19, len(v))]
			}
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
				m["id"], m["client_id"], m["role"], m["prefix"], exp, status, m["uses"])
		}
		_ = w.Flush()
		fmt.Printf("\n%d token(s), %v active\n", len(items), out["active"])
		return nil

	case "rotate", "revoke":
		if fs.NArg() < 1 {
			return fmt.Errorf("usage: cpsctl token %s <token-id>", sub)
		}
		id := fs.Arg(0)
		if sub == "rotate" {
			out, err := c.do("POST", "/api/v1/admin/tokens/"+id+"/rotate", nil)
			if err != nil {
				return err
			}
			fmt.Printf("rotated %s -> %v\n", id, out["id"])
			fmt.Printf("  new SECRET (shown once): %v\n", out["token"])
			return nil
		}
		out, err := c.do("DELETE", "/api/v1/admin/tokens/"+id, nil)
		if err != nil {
			return err
		}
		fmt.Printf("revoked %v (client %v)\n", out["id"], out["client_id"])
		return nil
	}
	return fmt.Errorf("unknown token subcommand %q", sub)
}

func agentCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cpsctl agent enroll|list|revoke ...")
	}
	sub := args[0]
	fs := flag.NewFlagSet("agent "+sub, flag.ExitOnError)
	c := newClient(fs)
	agentID := fs.String("id", "", "agent id (default: the hostname you enroll from)")
	site := fs.String("site", "", "site this agent belongs to, e.g. wh1")
	ttl := fs.String("ttl", "8760h", "agent credential lifetime (default: 1 year)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	c.resolve()

	switch sub {
	case "enroll":
		body := map[string]any{"site": *site, "ttl": *ttl}
		if *agentID != "" {
			body["agent_id"] = *agentID
		}
		out, err := c.do("POST", "/api/v1/admin/agents/enroll", body)
		if err != nil {
			return err
		}
		secret, _ := out["token"].(string)
		fmt.Printf("agent %v enrolled at %v\n\n", out["agent_id"], c.base)
		fmt.Printf("  one-time token (shown once): %s\n\n", secret)
		fmt.Printf("  On the packing PC:\n")
		fmt.Printf("    cps-agent -enroll %s -site %v\n", secret, *site)
		fmt.Printf("\n  It stops working once the agent has enrolled.\n")
		return nil

	case "list":
		out, err := c.do("GET", "/api/v1/agents", nil)
		if err != nil {
			return err
		}
		items, _ := out["items"].([]any)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "AGENT\tSITE\tHOST\tOS\tVERSION\tADDRESS\tPRINTERS\tLAST SEEN")
		for _, it := range items {
			m := it.(map[string]any)
			pn := 0
			if l, ok := m["printers"].([]any); ok {
				pn = len(l)
			}
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
				m["id"], m["site"], m["hostname"], m["os"], m["version"], m["agent_addr"],
				pn, ago(fmt.Sprint(m["last_seen"])))
		}
		_ = w.Flush()
		fmt.Printf("\n%v agent(s), %v healthy (last 60s)\n", out["count"], out["healthy"])
		return nil

	case "revoke":
		if fs.NArg() < 1 {
			return fmt.Errorf("usage: cpsctl agent revoke <agent-id>")
		}
		id := fs.Arg(0)
		out, err := c.do("DELETE", "/api/v1/agents/"+id, nil)
		if err != nil {
			return err
		}
		fmt.Printf("revoked %v; %d printer(s) removed from the fleet\n",
			out["agent"], len(toSlice(out["printers_removed"])))
		return nil
	}
	return fmt.Errorf("unknown agent subcommand %q", sub)
}

func discoverCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cpsctl discover list|adopt|dismiss ...")
	}
	sub := args[0]
	fs := flag.NewFlagSet("discover "+sub, flag.ExitOnError)
	c := newClient(fs)
	printerID := fs.String("printer-id", "", "id for the adopted printer, e.g. wh1-pack-04")
	site := fs.String("site", "", "site for the adopted printer")
	name := fs.String("name", "", "display name")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	c.resolve()

	switch sub {
	case "list":
		out, err := c.do("GET", "/api/v1/admin/discovery/candidates", nil)
		if err != nil {
			return err
		}
		items, _ := out["items"].([]any)
		if len(items) == 0 {
			fmt.Println("no candidates: nothing discovered that is not already a printer")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "DEVICE KEY\tSOURCE\tMODEL\tADDRESS\tLAST SEEN")
		for _, it := range items {
			m := it.(map[string]any)
			addr := "-"
			if h, ok := m["host"].(string); ok && h != "" {
				addr = fmt.Sprintf("%v:%v", h, m["port"])
			}
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n",
				m["device_key"], m["source"], m["model"], addr, ago(fmt.Sprint(m["last_seen"])))
		}
		_ = w.Flush()
		return nil

	case "adopt":
		if fs.NArg() < 1 || *printerID == "" {
			return fmt.Errorf("usage: cpsctl discover adopt <device-key> --printer-id <id>")
		}
		body := map[string]any{"printer_id": *printerID, "site": *site, "name": *name}
		out, err := c.do("POST", "/api/v1/admin/discovery/candidates/"+fs.Arg(0)+"/adopt", body)
		if err != nil {
			return err
		}
		fmt.Printf("adopted %s as %v — it is now dispatchable\n", fs.Arg(0), out["printer_id"])
		return nil

	case "dismiss":
		if fs.NArg() < 1 {
			return fmt.Errorf("usage: cpsctl discover dismiss <device-key>")
		}
		if _, err := c.do("POST", "/api/v1/admin/discovery/candidates/"+fs.Arg(0)+"/dismiss", map[string]any{}); err != nil {
			return err
		}
		fmt.Printf("dismissed %s\n", fs.Arg(0))
		return nil
	}
	return fmt.Errorf("unknown discover subcommand %q", sub)
}

func toSlice(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func ago(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}

func printersCmd(args []string) error {
	fs := flag.NewFlagSet("printers", flag.ExitOnError)
	c := newClient(fs)
	usable := fs.Bool("usable", false, "only show printers that cannot be used")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c.resolve()
	path := "/api/v1/printers/status"
	if *usable {
		path = "/api/v1/printers?usable=false"
	}
	out, err := c.do("GET", path, nil)
	if err != nil {
		return err
	}
	items, _ := out["items"].([]any)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PRINTER\tSITE\tMODEL\tUSABLE\tQUEUE\tSTATE\tLAST JOB")
	for _, it := range items {
		m := it.(map[string]any)
		st := m["status"].(map[string]any)
		last := "-"
		if lj, ok := st["last_job"].(map[string]any); ok {
			last = fmt.Sprintf("%v/%v", lj["state"], trunc(fmt.Sprint(lj["job_id"]), 12))
		}
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
			m["printer_id"], m["site"], m["model"], m["usable"], m["queue_depth"], st["state"], last)
	}
	_ = w.Flush()
	return nil
}

func statusCmd(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	c := newClient(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c.resolve()
	out, err := c.do("GET", "/readyz", nil)
	if err != nil {
		fmt.Println("not ready:", err)
		return err
	}
	fmt.Printf("service ready, %v printer(s) registered\n", out["printers"])
	return nil
}

func testCmd(args []string) error {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	c := newClient(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c.resolve()
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: cpsctl test <printer-id>")
	}
	out, err := c.do("POST", "/api/v1/printers/"+fs.Arg(0)+"/test", nil)
	if err != nil {
		return err
	}
	fmt.Printf("test label queued: job %v on %v (%v)\n", out["job_id"], out["printer_id"], out["state"])
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
