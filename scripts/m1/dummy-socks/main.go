package main

import (
	"flag"
	"log"
	"net"
	"os"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	var (
		listenAddr = flag.String("listen", "127.0.0.1:12001", "SOCKS5 listen address")
		mode       = flag.String("mode", "direct", "egress mode: direct or upstream")
		upstream   = flag.String("upstream", "", "upstream SOCKS5 address (required for mode=upstream)")
		name       = flag.String("name", "dummy-socks", "node label used in logs")
		selfTest   = flag.Bool("self-test", false, "run the in-process self-test and exit")
	)
	flag.Parse()

	if *selfTest {
		if err := runSelfTest(os.Stdout); err != nil {
			log.Printf("self-test FAILED: %v", err)
			os.Exit(1)
		}
		return
	}

	var dial dialFunc
	switch *mode {
	case "direct":
		dial = directDialer
	case "upstream":
		if *upstream == "" {
			log.Fatal("--upstream is required when --mode=upstream")
		}
		dial = upstreamDialer(*upstream)
	default:
		log.Fatalf("unknown --mode %q (want direct or upstream)", *mode)
	}

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", *listenAddr, err)
	}
	log.Printf("event=listen node=%s addr=%s mode=%s upstream=%s",
		*name, ln.Addr(), *mode, *upstream)

	srv := newServer(*name, dial, os.Stderr)
	if err := srv.serve(ln); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
