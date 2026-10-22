package proxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type openerFunc func(host string, port int) (net.Conn, error)

func (f openerFunc) OpenStream(host string, port int) (net.Conn, error) { return f(host, port) }

func echoOpener(record func(host string, port int)) StreamOpener {
	return openerFunc(func(host string, port int) (net.Conn, error) {
		record(host, port)
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			_, _ = io.Copy(server, server)
		}()
		return client, nil
	})
}

func startListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

func TestSOCKS5Handshake(t *testing.T) {
	var mu sync.Mutex
	var gotHost string
	var gotPort int
	opener := echoOpener(func(host string, port int) {
		mu.Lock()
		gotHost, gotPort = host, port
		mu.Unlock()
	})

	ln := startListener(t)
	srv := &SOCKS5{Opener: opener}
	go func() {
		if err := srv.Serve(ln); err != nil {
			t.Logf("Serve: %v", err)
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(conn, method); err != nil {
		t.Fatalf("method reply: %v", err)
	}
	if method[0] != 0x05 || method[1] != 0x00 {
		t.Fatalf("method reply = %x, want 0500", method)
	}

	req := []byte{0x05, 0x01, 0x00, 0x03, 0x0b}
	req = append(req, "example.com"...)
	req = append(req, 0x00, 0x50)
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("request: %v", err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("SOCKS5 reply = %x, want 05 00 ...", reply)
	}

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("relay write: %v", err)
	}
	echo := make([]byte, 5)
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatalf("relay read: %v", err)
	}
	if string(echo) != "hello" {
		t.Fatalf("echo = %q, want %q", echo, "hello")
	}

	mu.Lock()
	host, port := gotHost, gotPort
	mu.Unlock()
	if host != "example.com" || port != 80 {
		t.Fatalf("opener got %s:%d, want example.com:80", host, port)
	}
}

func TestHTTPProxyCONNECT(t *testing.T) {
	var mu sync.Mutex
	var gotHost string
	var gotPort int
	opener := echoOpener(func(host string, port int) {
		mu.Lock()
		gotHost, gotPort = host, port
		mu.Unlock()
	})

	ln := startListener(t)
	srv := &HTTPProxy{Opener: opener}
	go func() {
		if err := srv.Serve(ln); err != nil {
			t.Logf("Serve: %v", err)
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)

	request := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("CONNECT: %v", err)
	}
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("status line: %v", err)
	}
	if !strings.Contains(status, "200 Connection Established") {
		t.Fatalf("status line = %q", status)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	if _, err := conn.Write([]byte("tunnel")); err != nil {
		t.Fatalf("relay write: %v", err)
	}
	echo := make([]byte, 6)
	if _, err := io.ReadFull(br, echo); err != nil {
		t.Fatalf("relay read: %v", err)
	}
	if string(echo) != "tunnel" {
		t.Fatalf("echo = %q, want %q", echo, "tunnel")
	}

	mu.Lock()
	host, port := gotHost, gotPort
	mu.Unlock()
	if host != "example.com" || port != 443 {
		t.Fatalf("opener got %s:%d, want example.com:443", host, port)
	}
}

func TestHTTPProxyAbsoluteURI(t *testing.T) {
	upstreamLine := make(chan string, 1)
	upstreamProxyHeader := make(chan string, 1)
	opener := openerFunc(func(host string, port int) (net.Conn, error) {
		if host != "example.com" || port != 80 {
			t.Errorf("opener got %s:%d, want example.com:80", host, port)
		}
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			br := bufio.NewReader(server)
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			upstreamLine <- line
			var proxyHeader string
			for {
				h, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if h == "\r\n" {
					break
				}
				if strings.HasPrefix(strings.ToLower(h), "proxy-connection:") {
					proxyHeader = h
				}
			}
			upstreamProxyHeader <- proxyHeader
			_, _ = io.WriteString(server, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
			_, _ = io.Copy(io.Discard, br)
		}()
		return client, nil
	})

	ln := startListener(t)
	srv := &HTTPProxy{Opener: opener}
	go func() {
		if err := srv.Serve(ln); err != nil {
			t.Logf("Serve: %v", err)
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	request := "GET http://example.com/path?q=1 HTTP/1.1\r\nHost: example.com\r\nProxy-Connection: keep-alive\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("response = %d %q, want 200 %q", resp.StatusCode, body, "ok")
	}

	select {
	case line := <-upstreamLine:
		if line != "GET /path?q=1 HTTP/1.1\r\n" {
			t.Fatalf("upstream request line = %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request line not received")
	}
	select {
	case header := <-upstreamProxyHeader:
		if header != "" {
			t.Fatalf("Proxy-Connection leaked upstream: %q", header)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream headers not received")
	}
}
