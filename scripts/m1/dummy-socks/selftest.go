package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"time"
)

// runSelfTest exercises a direct node and an upstream node in one process:
//
//	echo target <- fake upstream (direct SOCKS5) <- node under test (upstream)
//
// It prints one PASS line per mode and returns an error on any mismatch.
func runSelfTest(out io.Writer) error {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen echo: %w", err)
	}
	defer echoLn.Close()
	go echoLoop(echoLn)

	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen fake upstream: %w", err)
	}
	defer upLn.Close()
	go newServer("selftest-upstream", directDialer, io.Discard).serve(upLn)

	directLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen direct node: %w", err)
	}
	defer directLn.Close()
	go newServer("selftest-direct", directDialer, io.Discard).serve(directLn)

	upstreamLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen upstream node: %w", err)
	}
	defer upstreamLn.Close()
	go newServer("selftest-node", upstreamDialer(upLn.Addr().String()), io.Discard).serve(upstreamLn)

	port := uint16(echoLn.Addr().(*net.TCPAddr).Port)
	cases := []struct {
		mode  string
		proxy string
		t     target
	}{
		{"direct", directLn.Addr().String(), target{host: "127.0.0.1", port: port}},
		{"upstream", upstreamLn.Addr().String(), target{host: "127.0.0.1", port: port}},
		{"upstream-domain", upstreamLn.Addr().String(), target{host: "localhost", port: port}},
	}
	for _, tc := range cases {
		start := time.Now()
		if err := roundTrip(tc.proxy, tc.t, []byte("m1-spike-selftest-"+tc.mode)); err != nil {
			return fmt.Errorf("mode %s: %w", tc.mode, err)
		}
		fmt.Fprintf(out, "PASS mode=%s proxy=%s target=%s rtt_ms=%d\n",
			tc.mode, tc.proxy, tc.t, time.Since(start).Milliseconds())
	}
	fmt.Fprintln(out, "SELFTEST PASS")
	return nil
}

// roundTrip sends payload through proxy to t and verifies the echo.
func roundTrip(proxy string, t target, payload []byte) error {
	conn, err := socks5Connect(proxy, t)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if _, err := conn.Write(payload); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("echo mismatch: got %q want %q", got, payload)
	}
	return nil
}

// echoLoop copies every accepted connection back to its peer.
func echoLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}()
	}
}
