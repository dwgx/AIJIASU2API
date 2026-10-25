package gate

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTP CONNECT listener constants. The head budget is generous for a proxy
// request (one request line plus a handful of headers) and small enough that a
// client cannot pin a handler by streaming headers forever.
const (
	httpMaxHeaderBytes = 8 << 10
	httpMaxHeaderLines = 64

	// httpProxyRealm is the Basic realm of the 407 challenge. It names the
	// panel, never a user or a node.
	httpProxyRealm = "apc"

	// httpConnectEstablished is the complete success response. CONNECT has no
	// response body: the bytes after it are the tunnel.
	httpConnectEstablished = "HTTP/1.1 200 Connection Established\r\n\r\n"
)

// httpRequest is one parsed proxy request. hasAuth reports whether the client
// sent a Proxy-Authorization header at all; a header that cannot be parsed as
// Basic sets hasAuth and leaves the credential empty, so it takes the same
// generic 407 path as a wrong secret.
type httpRequest struct {
	method  string
	target  target
	hasAuth bool
	user    string
	pass    string
}

// httpRefusal is a request refusal the handler answers with one HTTP status.
// Method is the parsed method when the request line was valid; Class is the
// credential-free FlowRecord class; Err never carries a credential.
type httpRefusal struct {
	status int
	method string
	class  string
	err    error
}

func (e *httpRefusal) Error() string { return e.err.Error() }

func (e *httpRefusal) Unwrap() error { return e.err }

// handleHTTP serves one HTTP CONNECT connection: request head, authentication,
// the shared admission pipeline, OpenFlow and relay. Every exit path closes the
// client connection.
func (s *Server) handleHTTP(client net.Conn) {
	s.track(client)
	defer s.untrack(client)
	defer client.Close()

	started := s.cfg.Now()

	// A connection queued in the accept backlog can be handed to a handler
	// after Close took its client snapshot; refuse it here.
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		s.rejected.Add(1)
		s.recordHTTPReject(started, "", target{}, Principal{}, FlowErrorClosed)
		return
	}

	// The listener is loopback-only for the same reason the SOCKS5 one is:
	// authentication never makes a remote peer acceptable.
	if !loopbackPeer(client.RemoteAddr()) {
		s.rejected.Add(1)
		s.recordHTTPReject(started, "", target{}, Principal{}, FlowErrorNonLoopbackPeer)
		s.logger.Warn("gate peer rejected",
			"node_id", s.cfg.NodeID,
			"protocol", FlowProtocolHTTP,
			"reason", FlowErrorNonLoopbackPeer,
		)
		return
	}

	_ = client.SetDeadline(time.Now().Add(handshakeTimeout))

	// Count what the peer sends so an idle probe (dial and close) can be told
	// apart from a truncated request head.
	head := &countingConn{Conn: client}
	reader := bufio.NewReader(head)
	request, err := readHTTPConnect(reader)
	if err != nil {
		if head.read == 0 && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
			s.probes.Add(1)
			s.logger.Debug("gate probe closed", "node_id", s.cfg.NodeID, "protocol", FlowProtocolHTTP)
			return
		}
		s.refuseHTTP(client, started, err)
		return
	}
	_ = client.SetDeadline(time.Time{})

	// Credentials are verified only in userpass mode; loopback mode ignores
	// Proxy-Authorization exactly as the SOCKS5 no-auth method ignores it.
	principal := Principal{}
	if s.cfg.AuthMode == AuthUserPass {
		if !request.hasAuth {
			s.refuseHTTPAuth(client, started, request, FlowErrorAuthFailed, "no Proxy-Authorization credentials")
			return
		}
		if s.cfg.Authenticator == nil {
			s.refuseHTTPAuth(client, started, request, FlowErrorAuthUnavailable, "no authenticator is configured")
			return
		}
		authenticated, ok := s.cfg.Authenticator.Authenticate(request.user, request.pass)
		if !ok || !authenticated.Authenticated() {
			s.refuseHTTPAuth(client, started, request, FlowErrorAuthFailed, "authentication failed")
			return
		}
		principal = authenticated
	}

	// The same two policy stages as SOCKS5: user enabled/allowlist/quota, then
	// the account quota. A refusal never takes a slot or opens upstream
	// traffic. 403 is the allowlist/identity refusal; an exhausted quota is
	// 429 with a Retry-After pointing at the monthly reset.
	verdict := s.admit(principal, request.target)
	if !verdict.ok {
		s.rejected.Add(1)
		s.recordHTTPReject(started, request.method, request.target, principal, verdict.errorClass)
		status := http.StatusForbidden
		var headers []string
		switch verdict.errorClass {
		case FlowErrorQuota, FlowErrorUserQuota:
			status = http.StatusTooManyRequests
			headers = append(headers, "Retry-After: "+strconv.Itoa(s.retryAfterSeconds()))
		case FlowErrorUserFlowLimit:
			status = http.StatusTooManyRequests
			headers = append(headers, "Retry-After: 1")
		}
		_ = writeHTTPStatus(client, status, headers...)
		s.logger.Warn("gate flow rejected",
			"node_id", s.cfg.NodeID,
			"protocol", FlowProtocolHTTP,
			"account_id", s.cfg.AccountID,
			"user_id", principal.UserID,
			"target", request.target.String(),
			"reason", verdict.errorClass,
			"used_bytes", verdict.used,
			"budget_bytes", verdict.budget,
		)
		return
	}
	if verdict.release != nil {
		defer verdict.release()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release, err := s.acquireFlow(ctx)
	if err != nil {
		s.rejected.Add(1)
		errorClass := FlowErrorClosed
		var headers []string
		switch {
		case errors.Is(err, errAdmissionTimeout):
			errorClass = FlowErrorAdmissionTimeout
			headers = append(headers, "Retry-After: 1")
		case errors.Is(err, errDraining):
			errorClass = FlowErrorDraining
		}
		s.recordHTTPReject(started, request.method, request.target, principal, errorClass)
		_ = writeHTTPStatus(client, http.StatusServiceUnavailable, headers...)
		s.logger.Warn("gate flow rejected",
			"node_id", s.cfg.NodeID,
			"protocol", FlowProtocolHTTP,
			"user_id", principal.UserID,
			"target", request.target.String(),
			"reason", err,
		)
		return
	}
	defer release()

	f, errorClass := s.beginFlow(client, cancel)
	if errorClass != "" {
		s.rejected.Add(1)
		s.recordHTTPReject(started, request.method, request.target, principal, errorClass)
		_ = writeHTTPStatus(client, http.StatusServiceUnavailable)
		return
	}
	defer s.finishFlow(f)

	remote, err := s.session.OpenFlow(ctx, request.target.host, request.target.port)
	if err != nil {
		s.rejected.Add(1)
		s.recordHTTPReject(started, request.method, request.target, principal, FlowErrorOpenFlow)
		_ = writeHTTPStatus(client, http.StatusBadGateway)
		s.logger.Warn("gate open flow failed",
			"node_id", s.cfg.NodeID,
			"protocol", FlowProtocolHTTP,
			"target", request.target.String(),
			"error", err,
		)
		return
	}
	s.attachRemote(f, remote)

	if _, err := io.WriteString(client, httpConnectEstablished); err != nil {
		s.rejected.Add(1)
		s.recordHTTPReject(started, request.method, request.target, principal, FlowErrorReply)
		s.logger.Warn("gate reply failed",
			"node_id", s.cfg.NodeID,
			"protocol", FlowProtocolHTTP,
			"target", request.target.String(),
			"error", err,
		)
		return
	}
	s.accepted.Add(1)

	// The relay reads through the same buffered reader: a client that
	// pipelined its first tunnel bytes behind the head must not lose them.
	tunnel := &bufferedClient{Conn: client, r: reader}
	in, out, quotaStopped := relay(tunnel, remote, &s.ingress, &s.egress, s.meterProgress(principal.UserID))
	record := FlowRecord{
		StartedAt:  started,
		UserID:     principal.UserID,
		UserName:   principal.UserName,
		Method:     request.method,
		Protocol:   FlowProtocolHTTP,
		TargetHost: request.target.host,
		TargetPort: request.target.port,
		Status:     FlowStatusOK,
		BytesIn:    in,
		BytesOut:   out,
	}
	if quotaStopped {
		record.ErrorClass = FlowErrorQuota
	}
	s.recordFlow(record)
	s.logger.Info("gate flow closed",
		"node_id", s.cfg.NodeID,
		"protocol", FlowProtocolHTTP,
		"user_id", principal.UserID,
		"target", request.target.String(),
		"ingress_bytes", in,
		"egress_bytes", out,
		"duration", s.cfg.Now().Sub(started),
		"error_class", record.ErrorClass,
	)
}

// refuseHTTP answers a request-head refusal. A non-CONNECT method gets 405 with
// an Allow header; everything else maps to the status the parser chose (400).
func (s *Server) refuseHTTP(client net.Conn, started time.Time, err error) {
	status := http.StatusBadRequest
	class := FlowErrorHandshake
	method := ""
	var refusal *httpRefusal
	if errors.As(err, &refusal) {
		status = refusal.status
		class = refusal.class
		method = refusal.method
	}
	var headers []string
	if status == http.StatusMethodNotAllowed {
		headers = append(headers, "Allow: CONNECT")
	}
	s.rejected.Add(1)
	s.recordHTTPReject(started, method, target{}, Principal{}, class)
	_ = writeHTTPStatus(client, status, headers...)
	s.logger.Warn("gate handshake rejected",
		"node_id", s.cfg.NodeID,
		"protocol", FlowProtocolHTTP,
		"reason", class,
		"error", err,
	)
}

// refuseHTTPAuth answers one credential rejection with the Basic challenge.
// Every failure is one 407, so an unknown user, a wrong secret, a revoked key
// and a malformed header are indistinguishable to the client.
func (s *Server) refuseHTTPAuth(client net.Conn, started time.Time, request httpRequest, class, reason string) {
	s.rejected.Add(1)
	s.recordHTTPReject(started, request.method, request.target, Principal{}, class)
	_ = writeHTTPStatus(client, http.StatusProxyAuthRequired,
		`Proxy-Authenticate: Basic realm="`+httpProxyRealm+`"`)
	s.logger.Warn("gate handshake rejected",
		"node_id", s.cfg.NodeID,
		"protocol", FlowProtocolHTTP,
		"reason", class,
		"error", reason,
	)
}

// recordHTTPReject records a pre-relay refusal from the HTTP CONNECT listener.
// It mirrors recordReject but stamps the protocol and keeps the parsed method
// (a GET is not a CONNECT), so the Lab window never mislabels the request.
func (s *Server) recordHTTPReject(started time.Time, method string, t target, principal Principal, errorClass string) {
	s.recordFlow(FlowRecord{
		StartedAt:  started,
		UserID:     principal.UserID,
		UserName:   principal.UserName,
		Method:     method,
		Protocol:   FlowProtocolHTTP,
		TargetHost: t.host,
		TargetPort: t.port,
		Status:     FlowStatusRejected,
		ErrorClass: errorClass,
	})
}

// retryAfterSeconds is the Retry-After hint for a quota refusal: the number of
// seconds until the next UTC month starts, when the monthly totals reset.
func (s *Server) retryAfterSeconds() int {
	now := s.cfg.Now().UTC()
	next := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	seconds := int(next.Sub(now) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

// readHTTPConnect parses one HTTP CONNECT request head: the request line and
// the headers up to the blank line. The reader it consumed must be used for the
// relay so bytes the client pipelined after the head are not lost.
func readHTTPConnect(r *bufio.Reader) (httpRequest, error) {
	budget := httpMaxHeaderBytes
	line, err := readHTTPLine(r, &budget)
	if err != nil {
		return httpRequest{}, err
	}
	fields := strings.Split(line, " ")
	if len(fields) != 3 || fields[0] == "" || fields[1] == "" || fields[2] == "" {
		drainHTTPHead(r, &budget)
		return httpRequest{}, &httpRefusal{
			status: http.StatusBadRequest,
			class:  FlowErrorHandshake,
			err:    errors.New("malformed request line"),
		}
	}
	method, authority, version := fields[0], fields[1], fields[2]
	if method != http.MethodConnect {
		drainHTTPHead(r, &budget)
		return httpRequest{}, &httpRefusal{
			status: http.StatusMethodNotAllowed,
			method: method,
			class:  FlowErrorHandshake,
			err:    fmt.Errorf("method %q is not CONNECT", method),
		}
	}
	if version != "HTTP/1.1" {
		drainHTTPHead(r, &budget)
		return httpRequest{}, &httpRefusal{
			status: http.StatusBadRequest,
			method: method,
			class:  FlowErrorHandshake,
			err:    fmt.Errorf("version %q is not HTTP/1.1", version),
		}
	}
	host, portText, err := net.SplitHostPort(authority)
	if err != nil {
		drainHTTPHead(r, &budget)
		return httpRequest{}, &httpRefusal{
			status: http.StatusBadRequest,
			method: method,
			class:  FlowErrorHandshake,
			err:    errors.New("malformed CONNECT authority"),
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		drainHTTPHead(r, &budget)
		return httpRequest{}, &httpRefusal{
			status: http.StatusBadRequest,
			method: method,
			class:  FlowErrorHandshake,
			err:    errors.New("CONNECT authority has an invalid port"),
		}
	}
	host = CanonicalHost(host)
	if !validAuthorityHost(host) {
		drainHTTPHead(r, &budget)
		return httpRequest{}, &httpRefusal{
			status: http.StatusBadRequest,
			method: method,
			class:  FlowErrorHandshake,
			err:    errors.New("CONNECT authority has an invalid host"),
		}
	}
	request := httpRequest{method: method, target: target{host: host, port: port}}
	if err := readHTTPHeaders(r, &request, &budget); err != nil {
		return httpRequest{}, err
	}
	return request, nil
}

// drainHTTPHead consumes the rest of a request head after a refusal. Closing a
// socket that still has unread input can turn the response into a reset on
// some platforms, which would discard the refusal the client is waiting for.
// It is bounded by the same head budget as the parser.
func drainHTTPHead(r *bufio.Reader, budget *int) {
	for lines := 0; lines < httpMaxHeaderLines; lines++ {
		line, err := readHTTPLine(r, budget)
		if err != nil || line == "" {
			return
		}
	}
}

// readHTTPHeaders consumes the header block. Only Proxy-Authorization is
// interpreted; every other header is ignored (the tunnel needs none of them).
// A malformed line records the refusal but keeps consuming the head, so the
// client's remaining bytes never race the response.
func readHTTPHeaders(r *bufio.Reader, request *httpRequest, budget *int) error {
	var refusal *httpRefusal
	refuse := func(err error) {
		if refusal == nil {
			refusal = &httpRefusal{
				status: http.StatusBadRequest,
				method: request.method,
				class:  FlowErrorHandshake,
				err:    err,
			}
		}
	}
	for lines := 0; ; lines++ {
		if lines >= httpMaxHeaderLines {
			refuse(errors.New("too many headers"))
			return refusal
		}
		line, err := readHTTPLine(r, budget)
		if err != nil {
			if refusal != nil {
				return refusal
			}
			return err
		}
		if line == "" {
			if refusal != nil {
				return refusal
			}
			return nil
		}
		if refusal != nil {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			refuse(errors.New("obsolete header folding"))
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			refuse(errors.New("malformed header"))
			continue
		}
		if !validHeaderName(name) {
			refuse(fmt.Errorf("malformed header name %q", name))
			continue
		}
		if !strings.EqualFold(name, "Proxy-Authorization") {
			continue
		}
		if request.hasAuth {
			refuse(errors.New("duplicate Proxy-Authorization header"))
			continue
		}
		request.hasAuth = true
		if user, pass, ok := parseBasicAuth(value); ok {
			request.user, request.pass = user, pass
		}
	}
}

// readHTTPLine reads one CRLF-terminated line and charges it to budget. A line
// longer than the bufio buffer or a head over budget is a malformed request,
// not a slow client.
func readHTTPLine(r *bufio.Reader, budget *int) (string, error) {
	raw, err := r.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return "", &httpRefusal{
				status: http.StatusBadRequest,
				class:  FlowErrorHandshake,
				err:    errors.New("request line or header is too long"),
			}
		}
		return "", fmt.Errorf("read request head: %w", err)
	}
	*budget -= len(raw)
	if *budget < 0 {
		return "", &httpRefusal{
			status: http.StatusBadRequest,
			class:  FlowErrorHandshake,
			err:    errors.New("request head is too large"),
		}
	}
	return strings.TrimRight(string(raw), "\r\n"), nil
}

// parseBasicAuth decodes one Proxy-Authorization value. Only the Basic scheme
// with standard base64 and a non-empty user and password is accepted; anything
// else takes the generic 407 path.
func parseBasicAuth(value string) (user, pass string, ok bool) {
	scheme, encoded, found := strings.Cut(strings.TrimSpace(value), " ")
	if !found || !strings.EqualFold(scheme, "Basic") {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", "", false
	}
	defer clear(decoded)
	user, pass, found = strings.Cut(string(decoded), ":")
	if !found || user == "" || pass == "" {
		return "", "", false
	}
	return user, pass, true
}

// validHeaderName reports whether name is an RFC 7230 token. A space before
// the colon is therefore rejected instead of silently trimmed.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)):
		default:
			return false
		}
	}
	return true
}

// validAuthorityHost rejects a CONNECT authority that carries URL syntax. The
// SOCKS5 parser accepts a bare name; an HTTP authority is host:port only, so a
// scheme, path, query, userinfo or whitespace is a malformed request.
func validAuthorityHost(host string) bool {
	if host == "" {
		return false
	}
	for i := range len(host) {
		switch host[i] {
		case '/', '\\', '@', '?', '#', ' ', '\t', '\r', '\n':
			return false
		}
	}
	return true
}

// writeHTTPStatus answers one refusal. The tunnel never started, so the
// connection closes after the response.
func writeHTTPStatus(w io.Writer, status int, headers ...string) error {
	var response strings.Builder
	fmt.Fprintf(&response, "HTTP/1.1 %d %s\r\n", status, http.StatusText(status))
	for _, header := range headers {
		response.WriteString(header)
		response.WriteString("\r\n")
	}
	response.WriteString("Content-Length: 0\r\nConnection: close\r\n\r\n")
	_, err := io.WriteString(w, response.String())
	return err
}

// bufferedClient serves the client connection for relay after the request head
// was read through a buffered reader. Reads drain the buffered bytes first, so
// an optimistically pipelined TLS ClientHello is not lost.
type bufferedClient struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedClient) Read(p []byte) (int, error) { return c.r.Read(p) }

// CloseWrite keeps the relay's half-close behaviour on a wrapped connection.
func (c *bufferedClient) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}
