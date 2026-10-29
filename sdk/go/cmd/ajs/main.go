// Command ajs is a small, source-built operator CLI for ajs-accountd.
//
// It is deliberately read-only: it probes the health endpoints, lists nodes
// and leases, shows one lease with the gate connection it currently points at,
// and runs a doctor pass over those same reads. It never mutates daemon state,
// never prints a credential, and has no local proxy mode.
//
// The command is part of the ajssdk module and uses the standard library plus
// that module only, so `go build ./cmd/ajs` needs no network and no release
// artefact.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	ajs "ajssdk"
)

const (
	// version is the CLI version. It tracks the SDK contract the binary is
	// built against; there is no separate release artefact for this command.
	version = "0.1.0"

	envURL = "AJS_URL"
	envKey = "AJS_API_KEY"

	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2

	// probeTimeout bounds one health request; apiTimeout bounds a whole
	// command that talks to the versioned API.
	probeTimeout = 5 * time.Second
	apiTimeout   = 20 * time.Second

	maxProbeBody = 1 << 20
	maxDetail    = 200
	maxBodyText  = 4096
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches one invocation. It is the whole command surface, so a test
// can drive every branch without spawning a process.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		printUsage(stdout)
		return exitOK
	case "-v", "--version", "version":
		fmt.Fprintf(stdout, "ajs %s\n", version)
		return exitOK
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "nodes":
		return runNodes(args[1:], stdout, stderr)
	case "leases":
		return runLeases(args[1:], stdout, stderr)
	case "lease":
		return runLease(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ajs: unknown command %q\n", args[0])
		printUsage(stderr)
		return exitUsage
	}
}

const usageText = `ajs — read-only operator CLI for ajs-accountd

usage:
  ajs <command> [flags]

commands:
  status   probe GET /healthz and GET /readyz (no key needed)
  nodes    list proxy nodes
  leases   list leases
  lease    show one lease and its gate connection
  doctor   run the health probes plus one authenticated read

flags:
  --help      show this help (every command has its own --help)
  --version   print the CLI version

environment:
  AJS_URL      daemon base URL, used when --url is absent
  AJS_API_KEY  API key, used when --key is absent; never printed

exit codes:
  0 success   1 runtime failure   2 usage error
`

func printUsage(w io.Writer) {
	fmt.Fprint(w, usageText)
}

// commonFlags is the endpoint and credential surface shared by the commands.
// Each command registers only the flags it documents.
type commonFlags struct {
	url  string
	key  string
	json bool
}

// config is a resolved endpoint and credential. dropped records that the URL
// carried userinfo, which is discarded before any request is built.
type config struct {
	baseURL string
	key     string
	dropped bool
}

// resolve applies flag-then-environment precedence. The key is only required
// when the command cannot run without one.
func (cf *commonFlags) resolve(needKey bool) (config, error) {
	raw := strings.TrimSpace(cf.url)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(envURL))
	}
	if raw == "" {
		return config{}, fmt.Errorf("no daemon URL: pass --url or set %s", envURL)
	}
	base, dropped, err := normalizeBaseURL(raw)
	if err != nil {
		return config{}, err
	}
	key := strings.TrimSpace(cf.key)
	if key == "" {
		key = strings.TrimSpace(os.Getenv(envKey))
	}
	if needKey && key == "" {
		return config{}, fmt.Errorf("no API key: pass --key or set %s", envKey)
	}
	return config{baseURL: base, key: key, dropped: dropped}, nil
}

// announce reports a discarded URL credential on stderr, keeping stdout clean
// for --json. The credential itself is never echoed.
func (c config) announce(w io.Writer) {
	if c.dropped {
		fmt.Fprintln(w, "ajs: ignoring credentials embedded in the URL")
	}
}

// normalizeBaseURL validates a base URL and strips userinfo. The raw value is
// never echoed: a parse error must not copy an embedded password into stderr.
func normalizeBaseURL(raw string) (string, bool, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", false, errors.New("invalid URL: use an absolute http(s) URL with a host")
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return "", false, errors.New("invalid URL: scheme must be http or https")
	}
	if u.Host == "" {
		return "", false, errors.New("invalid URL: no host")
	}
	// ForceQuery catches a bare trailing "?", which url.Parse records as an
	// empty RawQuery; without it "https://host:2055?" would yield
	// "https://host:2055?/healthz" once a path is appended.
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", false, errors.New("invalid URL: drop the query or fragment")
	}
	dropped := u.User != nil
	if dropped {
		u.User = nil
	}
	return strings.TrimRight(u.String(), "/"), dropped, nil
}

// parseExit maps a flag.Parse error to an exit code. The flag package already
// printed the cause and, for -h/--help, the command usage.
func parseExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	return exitUsage
}

func usageError(w io.Writer, command, msg string) int {
	fmt.Fprintf(w, "ajs: %s\nrun 'ajs %s --help' for usage\n", msg, command)
	return exitUsage
}

func runtimeError(w io.Writer, err error) int {
	fmt.Fprintf(w, "ajs: %s\n", errorText(err))
	return exitRuntime
}

// errorText drops the SDK's own "ajs: " prefix so the CLI prints exactly one.
func errorText(err error) string {
	return strings.TrimPrefix(strings.TrimSpace(err.Error()), "ajs: ")
}

func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return "-"
}

func portOrDash(port int) string {
	if port <= 0 {
		return "-"
	}
	return strconv.Itoa(port)
}

func result(pass, skipped bool) string {
	switch {
	case skipped:
		return "SKIP"
	case pass:
		return "PASS"
	default:
		return "FAIL"
	}
}

func statusCode(code int) string {
	if code <= 0 {
		return "-"
	}
	return strconv.Itoa(code)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}

// --- status ---------------------------------------------------------------

// statusCheck is one unauthenticated probe. Body keeps the parsed JSON when the
// answer was JSON and a flattened string otherwise. A skipped check is a
// /readyz 404: the edge does not expose that endpoint, which is not a failure.
type statusCheck struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Status  int    `json:"status"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Version string `json:"version,omitempty"`
	Body    any    `json:"body,omitempty"`
	Error   string `json:"error,omitempty"`
}

type statusReport struct {
	URL     string        `json:"url"`
	Version string        `json:"version,omitempty"`
	OK      bool          `json:"ok"`
	Checks  []statusCheck `json:"checks"`
}

func probeClient() *http.Client {
	return &http.Client{
		Timeout: probeTimeout,
		// The API never redirects; refusing one keeps a probe from following
		// an attacker-chosen Location.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// probe fetches one health endpoint. No credential is attached: /healthz and
// /readyz are the unauthenticated surface.
func probe(ctx context.Context, hc *http.Client, base, name string) statusCheck {
	check := statusCheck{Name: name, URL: base + "/" + name}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, check.URL, nil)
	if err != nil {
		check.Error = errorText(err)
		return check
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ajs-cli/"+version)
	resp, err := hc.Do(req)
	if err != nil {
		check.Error = errorText(err)
		return check
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
	if readErr != nil {
		check.Error = errorText(readErr)
	}
	check.Status = resp.StatusCode
	check.OK = resp.StatusCode >= 200 && resp.StatusCode < 300
	// /readyz is portal/daemon-only: a public edge exposes /healthz alone and
	// answers 404 here. That is "not exposed", not "unhealthy". Every other
	// non-2xx answer, and any 404 on /healthz, stays a failure.
	check.Skipped = name == "readyz" && resp.StatusCode == http.StatusNotFound
	if check.Skipped {
		check.OK = true
	}
	check.Version = versionFromJSON(body)
	check.Body = decodeBody(body)
	return check
}

func versionFromJSON(body []byte) string {
	var envelope struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	return strings.TrimSpace(envelope.Version)
}

func decodeBody(body []byte) any {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}
	if json.Valid(trimmed) {
		return json.RawMessage(trimmed)
	}
	return truncate(strings.Join(strings.Fields(string(trimmed)), " "), maxBodyText)
}

func bodyText(body any) string {
	switch v := body.(type) {
	case json.RawMessage:
		var buf bytes.Buffer
		if json.Compact(&buf, v) == nil {
			return buf.String()
		}
		return string(v)
	case string:
		return v
	default:
		return ""
	}
}

// summary extracts the short, stable reason from a JSON health body so the
// table stays readable. The raw body is used only when it carries none; the
// full body is always available through --json.
func summary(body any) string {
	raw, ok := body.(json.RawMessage)
	if !ok {
		return bodyText(body)
	}
	var envelope struct {
		Status  string   `json:"status"`
		Reasons []string `json:"reasons"`
		Error   *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return bodyText(body)
	}
	if len(envelope.Reasons) > 0 {
		return "reasons: " + strings.Join(envelope.Reasons, ", ")
	}
	if envelope.Error != nil && envelope.Error.Code != "" {
		return "code: " + envelope.Error.Code
	}
	if envelope.Status != "" {
		return "status: " + envelope.Status
	}
	return bodyText(body)
}

func failureDetail(c statusCheck) string {
	if c.Error != "" {
		return c.Error
	}
	if c.OK {
		return ""
	}
	return truncate(summary(c.Body), maxDetail)
}

func probeDetail(c statusCheck) string {
	if c.Error != "" {
		return c.Error
	}
	if c.Skipped {
		return "404, not exposed at this edge"
	}
	detail := statusCode(c.Status)
	if c.Version != "" {
		detail += ", version " + c.Version
	}
	if !c.OK {
		if reason := truncate(summary(c.Body), maxDetail); reason != "" {
			detail += ": " + reason
		}
	}
	return detail
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ajs status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cf commonFlags
	fs.StringVar(&cf.url, "url", "", "daemon base URL (default $"+envURL+")")
	fs.BoolVar(&cf.json, "json", false, "print the report as JSON")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: ajs status [--url URL] [--json]\n\n"+
			"Probe GET /healthz and GET /readyz. Both answer without a key.\n"+
			"/healthz is required. A 404 from /readyz means the edge does not\n"+
			"expose it: that prints SKIP and is not a failure. Any other\n"+
			"non-2xx answer, or an unreachable endpoint, exits 1.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() > 0 {
		return usageError(stderr, "status", fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	cfg, err := cf.resolve(false)
	if err != nil {
		return usageError(stderr, "status", err.Error())
	}
	cfg.announce(stderr)

	ctx, cancel := context.WithTimeout(context.Background(), 2*probeTimeout)
	defer cancel()
	hc := probeClient()
	checks := []statusCheck{
		probe(ctx, hc, cfg.baseURL, "healthz"),
		probe(ctx, hc, cfg.baseURL, "readyz"),
	}
	report := statusReport{URL: cfg.baseURL, OK: true, Checks: checks}
	for _, c := range checks {
		if report.Version == "" && c.Version != "" {
			report.Version = c.Version
		}
		if !c.OK && !c.Skipped {
			report.OK = false
		}
	}

	if cf.json {
		if err := writeJSON(stdout, report); err != nil {
			return runtimeError(stderr, err)
		}
	} else {
		writeStatusTable(stdout, report)
	}
	if !report.OK {
		return exitRuntime
	}
	return exitOK
}

func writeStatusTable(w io.Writer, report statusReport) {
	fmt.Fprintf(w, "endpoint %s\n", report.URL)
	tw := newTable(w)
	fmt.Fprintln(tw, "CHECK\tHTTP\tRESULT\tVERSION")
	for _, c := range report.Checks {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Name, statusCode(c.Status), result(c.OK, c.Skipped), dash(c.Version))
	}
	_ = tw.Flush()
	for _, c := range report.Checks {
		if c.Skipped {
			fmt.Fprintf(w, "%s: skipped, not exposed at this edge (HTTP 404)\n", c.Name)
			continue
		}
		if detail := failureDetail(c); detail != "" {
			fmt.Fprintf(w, "%s: %s\n", c.Name, detail)
		}
	}
}

// --- nodes ----------------------------------------------------------------

func validSort(mode string) bool {
	switch mode {
	case "", "latency", "label", "region":
		return true
	default:
		return false
	}
}

// sortNodes orders a page client-side. Every mode breaks ties by node id, so
// the same page always prints in the same order.
func sortNodes(nodes []ajs.ProxyNode, mode string) {
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		switch mode {
		case "latency":
			if la, lb := latencyKey(a), latencyKey(b); la != lb {
				return la < lb
			}
		case "label":
			if al, bl := strings.ToLower(a.Label), strings.ToLower(b.Label); al != bl {
				return al < bl
			}
		case "region":
			if a.Region != b.Region {
				return a.Region < b.Region
			}
			if al, bl := strings.ToLower(a.Label), strings.ToLower(b.Label); al != bl {
				return al < bl
			}
		}
		return a.ID < b.ID
	})
}

// latencyKey puts an unprobed node last, so an unmeasured node never outranks a
// measured one.
func latencyKey(n ajs.ProxyNode) int {
	if !probed(n) || n.LatencyMs <= 0 {
		return math.MaxInt
	}
	return n.LatencyMs
}

// probed reports whether the daemon has measured this node at least once.
func probed(n ajs.ProxyNode) bool {
	return n.LastProbeAt != "" || n.LatencyMs > 0 || n.SuccessRate > 0
}

func runNodes(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ajs nodes", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cf commonFlags
	var region, account, sortBy string
	fs.StringVar(&cf.url, "url", "", "daemon base URL (default $"+envURL+")")
	fs.StringVar(&cf.key, "key", "", "API key (default $"+envKey+"; never printed)")
	fs.StringVar(&region, "region", "", "only nodes in this region")
	fs.StringVar(&account, "account", "", "only nodes of this account id")
	fs.StringVar(&sortBy, "sort", "", "sort by latency, label or region (default: daemon order)")
	fs.BoolVar(&cf.json, "json", false, "print the node array as JSON")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: ajs nodes [--url URL] [--key KEY] [--region R] [--account ID]\n"+
			"                 [--sort latency|label|region] [--json]\n\n"+
			"List one page of proxy nodes through GET /v1/nodes.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() > 0 {
		return usageError(stderr, "nodes", fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	mode := strings.ToLower(strings.TrimSpace(sortBy))
	if !validSort(mode) {
		return usageError(stderr, "nodes", fmt.Sprintf("unknown sort %q: use latency, label or region", sortBy))
	}
	cfg, err := cf.resolve(true)
	if err != nil {
		return usageError(stderr, "nodes", err.Error())
	}
	cfg.announce(stderr)
	client, err := ajs.New(cfg.baseURL, cfg.key)
	if err != nil {
		return usageError(stderr, "nodes", errorText(err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	nodes, err := client.ListNodes(ctx, ajs.ListNodesOptions{AccountID: account, Region: region})
	if err != nil {
		return runtimeError(stderr, err)
	}
	if nodes == nil {
		nodes = []ajs.ProxyNode{}
	}
	if mode != "" {
		sortNodes(nodes, mode)
	}
	if cf.json {
		if err := writeJSON(stdout, nodes); err != nil {
			return runtimeError(stderr, err)
		}
		return exitOK
	}
	writeNodesTable(stdout, nodes)
	return exitOK
}

func writeNodesTable(w io.Writer, nodes []ajs.ProxyNode) {
	tw := newTable(w)
	fmt.Fprintln(tw, "ID\tREGION\tLABEL\tSTATE\tLATENCY\tSUCCESS\tINUSE/CAP\tEGRESS")
	for _, n := range nodes {
		latency, success := "-", "-"
		if probed(n) {
			latency = strconv.Itoa(n.LatencyMs) + "ms"
			success = fmt.Sprintf("%.0f%%", n.SuccessRate*100)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d/%d\t%s\n",
			dash(n.ID), dash(n.Region), dash(n.Label), dash(string(n.State)),
			latency, success, n.InUse, n.Capacity, dash(n.EgressIP))
	}
	_ = tw.Flush()
}

// --- leases ---------------------------------------------------------------

func runLeases(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ajs leases", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cf commonFlags
	var user string
	fs.StringVar(&cf.url, "url", "", "daemon base URL (default $"+envURL+")")
	fs.StringVar(&cf.key, "key", "", "API key (default $"+envKey+"; never printed)")
	fs.StringVar(&user, "user", "", "only leases of this user id")
	fs.BoolVar(&cf.json, "json", false, "print the lease array as JSON")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: ajs leases [--url URL] [--key KEY] [--user ID] [--json]\n\n"+
			"List one page of leases through GET /v1/leases.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() > 0 {
		return usageError(stderr, "leases", fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	cfg, err := cf.resolve(true)
	if err != nil {
		return usageError(stderr, "leases", err.Error())
	}
	cfg.announce(stderr)
	client, err := ajs.New(cfg.baseURL, cfg.key)
	if err != nil {
		return usageError(stderr, "leases", errorText(err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	leases, err := client.ListLeases(ctx, ajs.ListLeasesOptions{UserID: user})
	if err != nil {
		return runtimeError(stderr, err)
	}
	if leases == nil {
		leases = []ajs.Lease{}
	}
	if cf.json {
		if err := writeJSON(stdout, leases); err != nil {
			return runtimeError(stderr, err)
		}
		return exitOK
	}
	writeLeasesTable(stdout, leases)
	return exitOK
}

func writeLeasesTable(w io.Writer, leases []ajs.Lease) {
	tw := newTable(w)
	fmt.Fprintln(tw, "ID\tUSER\tACCOUNT\tREGION\tNODE\tGATE\tSTATUS\tEXPIRES")
	for _, l := range leases {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			dash(l.ID), firstNonEmpty(l.UserName, l.UserID),
			firstNonEmpty(l.AccountLabel, l.AccountID), dash(l.Region),
			firstNonEmpty(l.NodeLabel, l.NodeID), portOrDash(l.GatePort),
			dash(string(l.Status)), dash(l.ExpiresAt))
	}
	_ = tw.Flush()
}

// --- lease ----------------------------------------------------------------

// leaseID reads the positional id. The flag package stops at the first
// positional, so a trailing flag is parsed on a second pass. That pass is
// silenced: runLease reports the cause once, with a usage hint.
func leaseID(fs *flag.FlagSet) (string, error) {
	if fs.NArg() == 0 {
		return "", errors.New("lease id is required")
	}
	id := fs.Arg(0)
	rest := fs.Args()[1:]
	if len(rest) > 0 {
		out := fs.Output()
		fs.SetOutput(io.Discard)
		err := fs.Parse(rest)
		fs.SetOutput(out)
		if errors.Is(err, flag.ErrHelp) {
			fs.Usage()
			return "", err
		}
		if err != nil {
			return "", err
		}
		if fs.NArg() > 0 {
			return "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
		}
	}
	if strings.TrimSpace(id) == "" {
		return "", errors.New("lease id is required")
	}
	return id, nil
}

func runLease(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ajs lease", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cf commonFlags
	fs.StringVar(&cf.url, "url", "", "daemon base URL (default $"+envURL+")")
	fs.StringVar(&cf.key, "key", "", "API key (default $"+envKey+"; never printed)")
	fs.BoolVar(&cf.json, "json", false, "print the lease detail as JSON")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: ajs lease <id> [--url URL] [--key KEY] [--json]\n\n"+
			"Show one lease and the gate connection it currently points at\n"+
			"through GET /v1/leases/{id}. The scoped-key secret is never returned\n"+
			"by the daemon and is never printed here.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	id, err := leaseID(fs)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return usageError(stderr, "lease", err.Error())
	}
	cfg, err := cf.resolve(true)
	if err != nil {
		return usageError(stderr, "lease", err.Error())
	}
	cfg.announce(stderr)
	client, err := ajs.New(cfg.baseURL, cfg.key)
	if err != nil {
		return usageError(stderr, "lease", errorText(err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	detail, err := client.GetLease(ctx, id)
	if err != nil {
		return runtimeError(stderr, err)
	}
	if cf.json {
		if err := writeJSON(stdout, detail); err != nil {
			return runtimeError(stderr, err)
		}
		return exitOK
	}
	writeLeaseDetail(stdout, detail)
	return exitOK
}

func writeLeaseDetail(w io.Writer, d *ajs.LeaseDetail) {
	tw := newTable(w)
	fmt.Fprintln(tw, "lease")
	fmt.Fprintf(tw, "  id\t%s\n", dash(d.Lease.ID))
	fmt.Fprintf(tw, "  user\t%s\n", firstNonEmpty(d.Lease.UserName, d.Lease.UserID))
	fmt.Fprintf(tw, "  account\t%s\n", firstNonEmpty(d.Lease.AccountLabel, d.Lease.AccountID))
	fmt.Fprintf(tw, "  platform\t%s\n", dash(string(d.Lease.Platform)))
	fmt.Fprintf(tw, "  region\t%s\n", dash(d.Lease.Region))
	fmt.Fprintf(tw, "  node\t%s\n", firstNonEmpty(d.Lease.NodeLabel, d.Lease.NodeID))
	fmt.Fprintf(tw, "  status\t%s\n", dash(string(d.Lease.Status)))
	fmt.Fprintf(tw, "  egress\t%s\n", dash(d.Lease.EgressIP))
	fmt.Fprintf(tw, "  ttl\t%s\n", ttlSeconds(d.Lease.TTLSeconds))
	fmt.Fprintf(tw, "  expires\t%s\n", dash(d.Lease.ExpiresAt))
	fmt.Fprintf(tw, "  requests\t%d\n", d.Lease.Requests)
	fmt.Fprintf(tw, "  bytes\t%d\n", d.Lease.Bytes)
	fmt.Fprintln(tw, "connection")
	fmt.Fprintf(tw, "  host\t%s\n", dash(d.Connection.Host))
	fmt.Fprintf(tw, "  port\t%s\n", portOrDash(d.Connection.Port))
	fmt.Fprintf(tw, "  protocol\t%s\n", dash(string(d.Connection.Protocol)))
	fmt.Fprintf(tw, "  username\t%s\n", dash(d.Connection.Username))
	fmt.Fprintf(tw, "  credential\t%s\n", dash(string(d.Connection.CredentialKind)))
	_ = tw.Flush()
}

func ttlSeconds(seconds int) string {
	if seconds <= 0 {
		return "-"
	}
	return strconv.Itoa(seconds) + "s"
}

// --- doctor ---------------------------------------------------------------

type doctorCheck struct {
	Name    string
	Pass    bool
	Skipped bool
	Detail  string
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ajs doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cf commonFlags
	fs.StringVar(&cf.url, "url", "", "daemon base URL (default $"+envURL+")")
	fs.StringVar(&cf.key, "key", "", "API key (default $"+envKey+"; never printed)")
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: ajs doctor [--url URL] [--key KEY]\n\n"+
			"Run the /healthz and /readyz probes, then one authenticated\n"+
			"GET /v1/nodes?limit=1. Prints PASS, FAIL or SKIP per check and\n"+
			"exits 1 when a required check fails. A /readyz 404 prints SKIP (the\n"+
			"edge does not expose it); a missing key fails the auth check, not\n"+
			"the invocation.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() > 0 {
		return usageError(stderr, "doctor", fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	cfg, err := cf.resolve(false)
	if err != nil {
		return usageError(stderr, "doctor", err.Error())
	}
	cfg.announce(stderr)

	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	hc := probeClient()
	health := probe(ctx, hc, cfg.baseURL, "healthz")
	ready := probe(ctx, hc, cfg.baseURL, "readyz")
	auth := doctorAuth(ctx, cfg)

	rows := []doctorCheck{
		{Name: "healthz", Pass: health.OK, Detail: probeDetail(health)},
		{Name: "readyz", Pass: ready.OK, Skipped: ready.Skipped, Detail: probeDetail(ready)},
		auth,
	}
	fmt.Fprintf(stdout, "endpoint %s\n", cfg.baseURL)
	tw := newTable(stdout)
	fmt.Fprintln(tw, "CHECK\tRESULT\tDETAIL")
	ok := true
	for _, row := range rows {
		if !row.Pass && !row.Skipped {
			ok = false
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", row.Name, result(row.Pass, row.Skipped), dash(row.Detail))
	}
	_ = tw.Flush()
	if !ok {
		return exitRuntime
	}
	return exitOK
}

func doctorAuth(ctx context.Context, cfg config) doctorCheck {
	check := doctorCheck{Name: "auth"}
	if cfg.key == "" {
		check.Detail = "no key: pass --key or set " + envKey
		return check
	}
	client, err := ajs.New(cfg.baseURL, cfg.key)
	if err != nil {
		check.Detail = errorText(err)
		return check
	}
	nodes, err := client.ListNodes(ctx, ajs.ListNodesOptions{Limit: 1})
	if err != nil {
		check.Detail = errorText(err)
		return check
	}
	check.Pass = true
	check.Detail = fmt.Sprintf("ListNodes ok (%d returned)", len(nodes))
	return check
}
