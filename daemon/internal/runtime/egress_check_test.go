package runtime_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/account"
	"ajsaccountd/internal/gate"
	"ajsaccountd/internal/runtime"
	"ajsproxy/apifacade"
)

// directSession is a FlowSession whose flows are real TCP dials, so the
// production gate can relay the egress probe to an httptest echo server.
type directSession struct {
	claimed string
}

func (s *directSession) SID() string               { return "direct-sid" }
func (s *directSession) Fields() map[string]string { return map[string]string{"sip": s.claimed} }
func (s *directSession) Healthy() bool             { return true }
func (s *directSession) Close() error              { return nil }

func (s *directSession) OpenFlow(ctx context.Context, host string, port int) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}

type directSessionOpener struct {
	claimed string
}

func (o directSessionOpener) Open(context.Context, account.SessionRequest) (account.FlowSession, error) {
	return &directSession{claimed: o.claimed}, nil
}

// newEgressFixture starts one real gate (the production gate factory) over a
// session that dials the probe's target directly.
func newEgressFixture(t *testing.T, claimed string) (*runtime.NodeController, string) {
	t.Helper()
	st := newNodeTestStore(t)
	acct := newNodeTestAccount(t, st, "egress-check")
	logins := newFakeLoginResults()
	logins.set(acct.ID, account.LoginResult{
		AuthResult:      "OK",
		UserName:        "vendor-user",
		ConnectPassword: account.Secret(testConnectPassword),
		Servers:         map[string]apifacade.Server{"s1": testServer("s1")},
	})
	controller, err := runtime.NewNodeController(runtime.NodeControllerConfig{
		Store:    st,
		Logins:   logins,
		Sessions: directSessionOpener{claimed: claimed},
		PortBase: 13500,
		Logger:   discardLogger(),
		// The egress probe speaks the no-auth SOCKS5 greeting, so this fixture
		// opts into the compatibility wire explicitly.
		GateAuth: gate.AuthLoopback,
	})
	if err != nil {
		t.Fatalf("NewNodeController: %v", err)
	}
	t.Cleanup(func() { _ = controller.Stop(context.Background()) })
	if err := controller.SyncAccount(context.Background(), acct.ID); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}
	return controller, slot1NodeID(acct.ID, "s1")
}

// echoServer serves a plain-text address, like ifconfig.me/ip.
func echoServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ip" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func echoTLSConfig(t *testing.T, ts *httptest.Server) *tls.Config {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

func TestLabViewCheckEgressVerifiedMatchAndCooldown(t *testing.T) {
	controller, nodeID := newEgressFixture(t, "203.0.113.9")
	echo := echoServer(t, "203.0.113.9\n")
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	view := runtime.NewLabView(runtime.LabConfig{
		Store:               newNodeTestStore(t),
		Controller:          controller,
		Now:                 func() time.Time { return now },
		EgressCheckURL:      echo.URL + "/ip",
		EgressTLSConfig:     echoTLSConfig(t, echo),
		EgressCheckTimeout:  5 * time.Second,
		EgressCheckInterval: 5 * time.Second,
	})

	got, err := view.CheckEgress(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("CheckEgress: %v", err)
	}
	if got.NodeID != nodeID || got.Claimed != "203.0.113.9" || got.Verified != "203.0.113.9" {
		t.Fatalf("check = %+v", got)
	}
	if got.Match == nil || !*got.Match {
		t.Fatalf("match = %v, want true", got.Match)
	}
	if got.Source != "echo" || got.Error != "" || got.LatencyMs < 0 {
		t.Fatalf("check metadata = %+v", got)
	}
	if got.CheckedAt != now.Format(time.RFC3339) {
		t.Fatalf("checkedAt = %q, want %q", got.CheckedAt, now.Format(time.RFC3339))
	}

	// GET /v1/lab/egress stays the claim-only view: the check result is not
	// stored or merged into it.
	egress, reason := view.Egress()
	if reason != "" || len(egress) != 1 || egress[0].Verified != "" || egress[0].Match != nil ||
		egress[0].Source != "fv-claim" {
		t.Fatalf("egress list changed after check: %+v reason=%q", egress, reason)
	}

	if _, err := view.CheckEgress(context.Background(), nodeID); !errors.Is(err, runtime.ErrEgressRateLimited) {
		t.Fatalf("second check err = %v, want ErrEgressRateLimited", err)
	}
	now = now.Add(4 * time.Second)
	if _, err := view.CheckEgress(context.Background(), nodeID); !errors.Is(err, runtime.ErrEgressRateLimited) {
		t.Fatalf("check after 4s err = %v, want ErrEgressRateLimited", err)
	}
	now = now.Add(time.Second)
	if _, err := view.CheckEgress(context.Background(), nodeID); err != nil {
		t.Fatalf("check after 5s: %v", err)
	}
}

func TestLabViewCheckEgressMismatchAndMissingClaim(t *testing.T) {
	t.Run("mismatch", func(t *testing.T) {
		controller, nodeID := newEgressFixture(t, "198.51.100.1")
		echo := echoServer(t, "203.0.113.9")
		view := runtime.NewLabView(runtime.LabConfig{
			Store:           newNodeTestStore(t),
			Controller:      controller,
			EgressCheckURL:  echo.URL + "/ip",
			EgressTLSConfig: echoTLSConfig(t, echo),
		})
		got, err := view.CheckEgress(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("CheckEgress: %v", err)
		}
		if got.Match == nil || *got.Match {
			t.Fatalf("match = %v, want false", got.Match)
		}
		if got.Verified != "203.0.113.9" || got.Claimed != "198.51.100.1" {
			t.Fatalf("check = %+v", got)
		}
	})

	t.Run("no claim", func(t *testing.T) {
		controller, nodeID := newEgressFixture(t, "")
		echo := echoServer(t, "203.0.113.9")
		view := runtime.NewLabView(runtime.LabConfig{
			Store:           newNodeTestStore(t),
			Controller:      controller,
			EgressCheckURL:  echo.URL + "/ip",
			EgressTLSConfig: echoTLSConfig(t, echo),
		})
		got, err := view.CheckEgress(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("CheckEgress: %v", err)
		}
		if got.Match != nil || got.Verified != "203.0.113.9" || got.Claimed != "" {
			t.Fatalf("check = %+v, want verified with null match", got)
		}
	})
}

func TestLabViewCheckEgressGuards(t *testing.T) {
	controller, nodeID := newEgressFixture(t, "203.0.113.9")
	echo := echoServer(t, "203.0.113.9")
	view := runtime.NewLabView(runtime.LabConfig{
		Store:           newNodeTestStore(t),
		Controller:      controller,
		EgressCheckURL:  echo.URL + "/ip",
		EgressTLSConfig: echoTLSConfig(t, echo),
	})
	if _, err := view.CheckEgress(context.Background(), "missing"); !errors.Is(err, runtime.ErrEgressNoGate) {
		t.Fatalf("unknown node err = %v, want ErrEgressNoGate", err)
	}
	if _, err := view.CheckEgress(context.Background(), nodeID); err != nil {
		t.Fatalf("known node: %v", err)
	}
	if _, err := view.CheckEgress(context.Background(), nodeID); !errors.Is(err, runtime.ErrEgressRateLimited) {
		t.Fatalf("cooldown err = %v, want ErrEgressRateLimited", err)
	}

	disabled := runtime.NewLabView(runtime.LabConfig{Store: newNodeTestStore(t)})
	if _, err := disabled.CheckEgress(context.Background(), nodeID); !errors.Is(err, runtime.ErrEgressNoGate) {
		t.Fatalf("disabled controller err = %v, want ErrEgressNoGate", err)
	}
}

func TestLabViewCheckEgressProbeFailure(t *testing.T) {
	t.Run("non-IP body", func(t *testing.T) {
		controller, nodeID := newEgressFixture(t, "203.0.113.9")
		echo := echoServer(t, "<html>not an address</html>")
		view := runtime.NewLabView(runtime.LabConfig{
			Store:           newNodeTestStore(t),
			Controller:      controller,
			EgressCheckURL:  echo.URL + "/ip",
			EgressTLSConfig: echoTLSConfig(t, echo),
		})
		got, err := view.CheckEgress(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("CheckEgress: %v", err)
		}
		if got.Error == "" || got.Verified != "" || got.Match != nil || got.Source != "echo" {
			t.Fatalf("failed check = %+v", got)
		}
		if got.CheckedAt == "" {
			t.Fatalf("failed check has no checkedAt: %+v", got)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		controller, nodeID := newEgressFixture(t, "203.0.113.9")
		slow := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		t.Cleanup(slow.Close)
		view := runtime.NewLabView(runtime.LabConfig{
			Store:              newNodeTestStore(t),
			Controller:         controller,
			EgressCheckURL:     slow.URL + "/ip",
			EgressTLSConfig:    echoTLSConfig(t, slow),
			EgressCheckTimeout: 150 * time.Millisecond,
		})
		started := time.Now()
		got, err := view.CheckEgress(context.Background(), nodeID)
		if err != nil {
			t.Fatalf("CheckEgress: %v", err)
		}
		if got.Error == "" || !strings.Contains(got.Error, "deadline exceeded") {
			t.Fatalf("timeout error = %q, want deadline exceeded", got.Error)
		}
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Fatalf("timeout took %s, want the configured 150ms bound", elapsed)
		}
	})
}
