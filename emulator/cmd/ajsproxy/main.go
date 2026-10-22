// Command ajsproxy is the Tier-2 AJiaSu emulator CLI.
//
// It speaks the control API (login/list), the FV authentication exchange, and
// one proxied flow per local SOCKS5 or HTTP proxy connection. It never contacts
// a live service unless the operator runs it against real credentials.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"text/tabwriter"
	"time"

	"ajsproxy/internal/api"
	"ajsproxy/internal/config"
	"ajsproxy/internal/fv"
	"ajsproxy/internal/proxy"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ajsproxy: "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `ajsproxy — offline-first AJiaSu (FV) emulator

Usage:
  ajsproxy login       [-config file] [-username u] [-password p]
  ajsproxy list        [-config file] [-username u] [-password p]
  ajsproxy connect     [-config file] [-server id] [-transport rawtcp|httpupgrade] [-target host:port]
  ajsproxy serve-socks [-config file] [-server id] [-transport ...] [-listen addr]
  ajsproxy serve-http  [-config file] [-server id] [-transport ...] [-listen addr]

The config file holds hosts, account, transport, listen addresses, and optional
device overrides. See emulator/README.md. Credentials are read from the config
or the flags; they are never printed.
`)
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "login":
		return runLogin(args[1:])
	case "list":
		return runList(args[1:])
	case "connect":
		return runConnect(args[1:])
	case "serve-socks":
		return runServe("socks", args[1:])
	case "serve-http":
		return runServe("http", args[1:])
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

type commonOptions struct {
	configPath string
	username   string
	password   string
}

func addCommonFlags(fs *flag.FlagSet, o *commonOptions) {
	fs.StringVar(&o.configPath, "config", "", "JSON config file (optional for login with flags)")
	fs.StringVar(&o.username, "username", "", "account name (overrides config)")
	fs.StringVar(&o.password, "password", "", "account password (overrides config)")
}

func loadClient(ctx context.Context, o *commonOptions) (*config.Config, *api.Client, *api.LoginResult, error) {
	var (
		cfg *config.Config
		err error
	)
	if o.configPath != "" {
		cfg, err = config.Load(o.configPath)
		if err != nil {
			return nil, nil, nil, err
		}
	} else {
		cfg = config.Default()
	}

	device := api.DefaultDevice()
	if err := cfg.ApplyDevice(&device); err != nil {
		return nil, nil, nil, err
	}
	if err := device.EnsureIDs(); err != nil {
		return nil, nil, nil, err
	}
	client := api.NewClient(cfg.Hosts, device)

	username := o.username
	if username == "" {
		username = cfg.Account.Username
	}
	password := o.password
	if password == "" {
		password = cfg.Account.Password
	}
	if username == "" || password == "" {
		return nil, nil, nil, errors.New("credentials required: set account.username/account.password in the config or pass -username/-password")
	}
	login, err := client.Login(ctx, username, password)
	if err != nil {
		return nil, nil, nil, err
	}
	if !login.OK() {
		return nil, nil, nil, fmt.Errorf("login rejected: AuthResult=%q AuthMessage=%q", login.AuthResult, login.AuthMessage)
	}
	return cfg, client, login, nil
}

func runLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var opts commonOptions
	addCommonFlags(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _, login, err := loadClient(ctx, &opts)
	if err != nil {
		return err
	}
	fmt.Printf("AuthResult=%s UserName=%s MembershipStatus=%s GroupTitle=%s Expired=%t\n",
		login.AuthResult, login.UserName, login.MembershipStatus, login.GroupTitle, login.Expired)
	if login.AuthMessage != "" {
		fmt.Printf("AuthMessage=%s\n", login.AuthMessage)
	}
	return nil
}

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var opts commonOptions
	addCommonFlags(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, client, _, err := loadClient(ctx, &opts)
	if err != nil {
		return err
	}
	servers, err := client.Servers(ctx)
	if err != nil {
		return err
	}
	printServers(servers)
	return nil
}

func printServers(servers map[string]api.Server) {
	ids := make([]string, 0, len(servers))
	for id := range servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tADDR\tSTATUS\tTITLE")
	for _, id := range ids {
		s := servers[id]
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", id, s.Addr(), s.Sts, s.Tit)
	}
	w.Flush()
}

func pickServer(servers map[string]api.Server, id string) (api.Server, error) {
	if id != "" {
		s, ok := servers[id]
		if !ok {
			return api.Server{}, fmt.Errorf("server %q is not in the list", id)
		}
		if s.Addr() == "" {
			return api.Server{}, fmt.Errorf("server %q has no svr/par.fv.p endpoint", id)
		}
		return s, nil
	}
	ids := make([]string, 0, len(servers))
	for serverID := range servers {
		ids = append(ids, serverID)
	}
	sort.Strings(ids)
	for _, serverID := range ids {
		s := servers[serverID]
		if s.OK() && s.Addr() != "" {
			return s, nil
		}
	}
	for _, serverID := range ids {
		if s := servers[serverID]; s.Addr() != "" {
			return s, nil
		}
	}
	return api.Server{}, errors.New("no usable server in the list; pass -server <id>")
}

func openSession(ctx context.Context, cfg *config.Config, client *api.Client, transportName, serverID string) (*fv.Session, api.Server, error) {
	if serverID == "" {
		serverID = cfg.Server
	}
	servers, err := client.Servers(ctx)
	if err != nil {
		return nil, api.Server{}, err
	}
	server, err := pickServer(servers, serverID)
	if err != nil {
		return nil, api.Server{}, err
	}
	if transportName == "" {
		transportName = cfg.Transport
	}
	transport, err := fv.DefaultRegistry().Get(transportName)
	if err != nil {
		return nil, api.Server{}, err
	}
	authCfg := fv.DefaultAuthConfig(client.UserName, client.ConnectPassword, client.Device.DeviceID)
	if cfg.CoreVersion != "" {
		authCfg.CoreVersion = cfg.CoreVersion
	}
	opts := fv.AuthOptions{}
	if cfg.OAEPHash != "" {
		hash, err := fv.HashByName(cfg.OAEPHash)
		if err != nil {
			return nil, api.Server{}, err
		}
		opts.OAEPHash = hash
	}
	if cfg.Timeouts.HandshakeSeconds > 0 {
		opts.HandshakeTimeout = time.Duration(cfg.Timeouts.HandshakeSeconds) * time.Second
	}
	if cfg.Timeouts.IdleMilliseconds > 0 {
		opts.IdleTimeout = time.Duration(cfg.Timeouts.IdleMilliseconds) * time.Millisecond
	}
	session, err := fv.Authenticate(ctx, authCfg, server.Addr(), transport, opts)
	if err != nil {
		return nil, api.Server{}, err
	}
	return session, server, nil
}

func runConnect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var opts commonOptions
	addCommonFlags(fs, &opts)
	var transportName, serverID, target string
	fs.StringVar(&transportName, "transport", "", "rawtcp or httpupgrade (default from config)")
	fs.StringVar(&serverID, "server", "", "server id from the list")
	fs.StringVar(&target, "target", "", "optional host:port to open one stream to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cfg, client, _, err := loadClient(ctx, &opts)
	if err != nil {
		return err
	}
	session, server, err := openSession(ctx, cfg, client, transportName, serverID)
	if err != nil {
		return err
	}
	defer session.Close()
	fmt.Printf("connected %s (%s) transport=%s sid=%s sip=%s cip=%s\n",
		server.ID, server.Addr(), session.Transport.Name(), session.SID,
		session.Field("sip"), session.Field("cip"))
	if target == "" {
		return nil
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("-target must be host:port: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 0xFFFF {
		return fmt.Errorf("-target port %q is invalid", portText)
	}
	stream, err := session.OpenStream(host, port)
	if err != nil {
		return err
	}
	fmt.Printf("stream %s:%d opened\n", host, port)
	return stream.Close()
}

func runServe(kind string, args []string) error {
	fs := flag.NewFlagSet("serve-"+kind, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var opts commonOptions
	addCommonFlags(fs, &opts)
	var transportName, serverID, listen string
	fs.StringVar(&transportName, "transport", "", "rawtcp or httpupgrade (default from config)")
	fs.StringVar(&serverID, "server", "", "server id from the list")
	fs.StringVar(&listen, "listen", "", "listen address (default from config)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	loginCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cfg, client, _, err := loadClient(loginCtx, &opts)
	if err != nil {
		return err
	}
	session, server, err := openSession(loginCtx, cfg, client, transportName, serverID)
	if err != nil {
		return err
	}
	defer session.Close()

	addr := listen
	if addr == "" {
		if kind == "socks" {
			addr = cfg.Listen.SOCKS5
		} else {
			addr = cfg.Listen.HTTP
		}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	logf := func(format string, logArgs ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", logArgs...)
	}
	fmt.Printf("listening on %s via %s (%s, %s), sid=%s\n", addr, session.Transport.Name(), server.ID, server.Addr(), session.SID)

	go func() {
		<-ctx.Done()
		ln.Close()
		session.Close()
	}()

	if kind == "socks" {
		err = (&proxy.SOCKS5{Opener: session, Logf: logf}).Serve(ln)
	} else {
		err = (&proxy.HTTPProxy{Opener: session, Logf: logf}).Serve(ln)
	}
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
