package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// Egress check contract values. The check reads a plain-text echo through the
// node's own SOCKS5 gate: no vendor host, no credential and no store write.
const (
	defaultEgressCheckURL      = "https://ifconfig.me/ip"
	defaultEgressCheckTimeout  = 10 * time.Second
	defaultEgressCheckInterval = 5 * time.Second
	egressCheckSource          = "echo"
	egressCheckBodyLimit       = 256
)

var (
	// ErrEgressNoGate is returned when the node has no running gate. The API
	// maps it to 409 CONFLICT.
	ErrEgressNoGate = errors.New("runtime: node has no running gate")
	// ErrEgressRateLimited is returned when the node was checked less than
	// one interval ago. The API maps it to 429 QUOTA_EXCEEDED.
	ErrEgressRateLimited = errors.New("runtime: egress check rate limited")
)

// CheckEgress performs one claimed-vs-verified check for a running node. It
// dials the node's loopback gate with SOCKS5 (no auth), requests the HTTPS echo
// through that flow and parses the body as an IP.
//
// A failed probe is not an error return: the record carries Error plus the
// latency and checkedAt of the attempt, so the operator sees that the check
// ran and failed. Errors are reserved for the request-level guards (no running
// gate, per-node cooldown).
func (v *LabView) CheckEgress(ctx context.Context, nodeID string) (model.EgressCheckView, error) {
	nodeID = strings.TrimSpace(nodeID)
	var running *NodeRuntime
	for _, item := range v.snapshot() {
		if item.NodeID == nodeID {
			copied := item
			running = &copied
			break
		}
	}
	if running == nil || running.Port <= 0 {
		return model.EgressCheckView{}, fmt.Errorf("%w: node %q", ErrEgressNoGate, nodeID)
	}

	start := v.now()
	v.egressMu.Lock()
	if last, ok := v.egressLast[nodeID]; ok {
		elapsed := start.Sub(last)
		if elapsed < 0 {
			elapsed = 0
		}
		if elapsed < v.cfg.EgressCheckInterval {
			v.egressMu.Unlock()
			return model.EgressCheckView{}, fmt.Errorf("%w: node %q, retry in %s",
				ErrEgressRateLimited, nodeID, (v.cfg.EgressCheckInterval - elapsed).Round(time.Millisecond))
		}
	}
	v.egressLast[nodeID] = start
	v.egressMu.Unlock()

	probeStart := time.Now()
	verified, probeErr := v.probeEgress(ctx, running.Port)
	view := model.EgressCheckView{
		NodeID:    nodeID,
		Claimed:   running.EgressIP,
		Verified:  verified,
		LatencyMs: time.Since(probeStart).Milliseconds(),
		CheckedAt: formatTime(v.now()),
		Source:    egressCheckSource,
	}
	if probeErr != nil {
		view.Error = probeErr.Error()
		return view, nil
	}
	if view.Claimed != "" && view.Verified != "" {
		match := view.Claimed == view.Verified
		view.Match = &match
	}
	return view, nil
}

// probeEgress requests the configured echo URL through the gate on port. The
// whole dial, TLS handshake and response are bounded by one timeout context.
func (v *LabView) probeEgress(ctx context.Context, port int) (string, error) {
	target, err := url.Parse(v.cfg.EgressCheckURL)
	if err != nil {
		return "", fmt.Errorf("egress probe: invalid echo URL: %w", err)
	}
	if target.Scheme != "https" || target.Host == "" {
		return "", errors.New("egress probe: echo URL must be an absolute https URL")
	}

	ctx, cancel := context.WithTimeout(ctx, v.cfg.EgressCheckTimeout)
	defer cancel()

	dialer := &socks5Dialer{proxy: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	transport := &http.Transport{
		// Never inherit an environment proxy: the only path out is the gate.
		Proxy:               nil,
		DialContext:         dialer.DialContext,
		TLSClientConfig:     v.cfg.EgressTLSConfig,
		TLSHandshakeTimeout: v.cfg.EgressCheckTimeout,
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   false,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		// A redirect would move the check to a host the allowlisted echo URL
		// did not name; treat it as a failed probe instead.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", fmt.Errorf("egress probe: build request: %w", err)
	}
	req.Header.Set("User-Agent", "ajs-accountd-egress-check")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("egress probe: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("egress probe: echo answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, egressCheckBodyLimit))
	if err != nil {
		return "", fmt.Errorf("egress probe: read echo body: %w", err)
	}
	ip := net.ParseIP(strings.TrimSpace(string(body)))
	if ip == nil {
		return "", errors.New("egress probe: echo body is not an IP address")
	}
	return ip.String(), nil
}

// socks5Dialer dials the node's gate and issues a SOCKS5 CONNECT for the
// address the HTTP transport asked for. It implements only the no-auth
// CONNECT method the gate serves.
type socks5Dialer struct {
	proxy string
}

func (d *socks5Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("egress probe: unsupported network %q", network)
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", d.proxy)
	if err != nil {
		return nil, fmt.Errorf("egress probe: dial gate: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		// The deadline covers the handshake and the HTTP exchange; the
		// transport closes the connection when the request context ends.
		_ = conn.SetDeadline(deadline)
	}
	if err := socks5Connect(conn, address); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// socks5Connect performs the no-auth greeting and one CONNECT request on an
// established connection, then consumes the bound-address reply.
func socks5Connect(conn net.Conn, address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("egress probe: split target %q: %w", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("egress probe: invalid target port %q", portText)
	}

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return fmt.Errorf("egress probe: socks5 greeting: %w", err)
	}
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return fmt.Errorf("egress probe: socks5 greeting reply: %w", err)
	}
	if greeting[0] != 0x05 || greeting[1] != 0x00 {
		return fmt.Errorf("egress probe: socks5 proxy rejected no-auth method (version %d, method %d)", greeting[0], greeting[1])
	}

	request := make([]byte, 0, 262)
	request = append(request, 0x05, 0x01, 0x00)
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			request = append(request, 0x01)
			request = append(request, ip4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	} else {
		if host == "" || len(host) > 255 {
			return fmt.Errorf("egress probe: invalid target host %q", host)
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, host...)
	}
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		return fmt.Errorf("egress probe: socks5 connect request: %w", err)
	}

	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return fmt.Errorf("egress probe: socks5 connect reply: %w", err)
	}
	if reply[0] != 0x05 {
		return fmt.Errorf("egress probe: socks5 reply version %d, want 5", reply[0])
	}
	if reply[1] != 0x00 {
		return fmt.Errorf("egress probe: socks5 connect failed (reply %d)", reply[1])
	}
	var skip int
	switch reply[3] {
	case 0x01:
		skip = 4
	case 0x04:
		skip = 16
	case 0x03:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return fmt.Errorf("egress probe: socks5 bound address: %w", err)
		}
		skip = int(length[0])
	default:
		return fmt.Errorf("egress probe: socks5 bound address type %d", reply[3])
	}
	if _, err := io.CopyN(io.Discard, conn, int64(skip+2)); err != nil {
		return fmt.Errorf("egress probe: socks5 bound address: %w", err)
	}
	return nil
}
