package proxy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPProxy is an HTTP proxy supporting CONNECT and absolute-URI requests.
// CONNECT tunnels raw bytes. Absolute-URI requests are rewritten to
// origin-form and relayed; the response is passed through unchanged.
type HTTPProxy struct {
	// Addr is the listen address, e.g. "127.0.0.1:8080".
	Addr string
	// Opener opens the remote flow. Required.
	Opener StreamOpener
	// Logf, when non-nil, receives per-connection diagnostics.
	Logf func(format string, args ...any)
}

// ListenAndServe listens on Addr and serves until the listener closes.
func (p *HTTPProxy) ListenAndServe() error {
	ln, err := net.Listen("tcp", p.Addr)
	if err != nil {
		return fmt.Errorf("proxy: HTTP listen %s: %w", p.Addr, err)
	}
	return p.Serve(ln)
}

// Serve accepts connections on ln until it is closed.
func (p *HTTPProxy) Serve(ln net.Listener) error {
	if p.Opener == nil {
		return errors.New("proxy: HTTP proxy has no stream opener")
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go p.handle(conn)
	}
}

func (p *HTTPProxy) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

func (p *HTTPProxy) handle(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(handshakeTimeout))
	br := bufio.NewReader(client)
	req, err := http.ReadRequest(br)
	if err != nil {
		p.logf("http: read request: %v", err)
		writeHTTPError(client, http.StatusBadRequest, "bad request")
		return
	}

	if req.Method == http.MethodConnect {
		p.handleConnect(client, br, req)
		return
	}
	p.handleAbsolute(client, br, req)
}

func (p *HTTPProxy) handleConnect(client net.Conn, br *bufio.Reader, req *http.Request) {
	host, port, err := splitHostPort(req.Host, 443)
	if err != nil {
		writeHTTPError(client, http.StatusBadRequest, "bad CONNECT authority")
		return
	}
	upstream, err := p.Opener.OpenStream(host, port)
	if err != nil {
		p.logf("http: CONNECT %s:%d: %v", host, port, err)
		writeHTTPError(client, http.StatusBadGateway, "bad gateway")
		return
	}
	defer upstream.Close()
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	relay(&bufferedConn{Conn: client, Reader: br}, upstream)
}

func (p *HTTPProxy) handleAbsolute(client net.Conn, br *bufio.Reader, req *http.Request) {
	if !req.URL.IsAbs() || req.URL.Host == "" {
		writeHTTPError(client, http.StatusBadRequest, "absolute URI required")
		return
	}
	if req.URL.Scheme != "http" {
		writeHTTPError(client, http.StatusNotImplemented, "only http absolute URIs are supported")
		return
	}
	host := req.URL.Hostname()
	port := 80
	if portText := req.URL.Port(); portText != "" {
		parsed, err := strconv.Atoi(portText)
		if err != nil || parsed <= 0 || parsed > 0xFFFF {
			writeHTTPError(client, http.StatusBadRequest, "bad port")
			return
		}
		port = parsed
	}
	upstream, err := p.Opener.OpenStream(host, port)
	if err != nil {
		p.logf("http: %s %s:%d: %v", req.Method, host, port, err)
		writeHTTPError(client, http.StatusBadGateway, "bad gateway")
		return
	}
	defer upstream.Close()

	var head bytes.Buffer
	fmt.Fprintf(&head, "%s %s HTTP/1.1\r\n", req.Method, req.URL.RequestURI())
	fmt.Fprintf(&head, "Host: %s\r\n", req.Host)
	req.Header.Del("Proxy-Connection")
	req.Header.Del("Proxy-Authorization")
	if err := req.Header.Write(&head); err != nil {
		writeHTTPError(client, http.StatusBadRequest, "bad headers")
		return
	}
	head.WriteString("\r\n")
	if _, err := upstream.Write(head.Bytes()); err != nil {
		p.logf("http: write upstream request: %v", err)
		return
	}
	_ = client.SetDeadline(time.Time{})
	relay(&bufferedConn{Conn: client, Reader: br}, upstream)
}

// bufferedConn reads from the bufio.Reader (which may hold bytes past the
// request headers) and writes to the underlying connection.
type bufferedConn struct {
	net.Conn
	*bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.Reader.Read(p) }

// CloseWrite keeps the relay's half-close behaviour on a wrapped connection.
func (c *bufferedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

func splitHostPort(hostport string, defaultPort int) (string, int, error) {
	host, portText, err := net.SplitHostPort(hostport)
	if err != nil {
		if strings.Contains(err.Error(), "missing port") {
			if hostport == "" {
				return "", 0, errors.New("empty host")
			}
			return hostport, defaultPort, nil
		}
		return "", 0, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 0xFFFF {
		return "", 0, fmt.Errorf("bad port %q", portText)
	}
	if host == "" {
		return "", 0, errors.New("empty host")
	}
	return host, port, nil
}

func writeHTTPError(w io.Writer, code int, message string) {
	body := message + "\n"
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), len(body), body)
}
