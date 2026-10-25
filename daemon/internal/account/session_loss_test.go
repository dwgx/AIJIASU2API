package account

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ajsproxy/fvfacade"
)

// TestFactoryClassifiesEquivalentSessionLoss proves AJS-353 finding 1's
// classification half on the production adapter, not a fake: an authenticated
// SID refused with the vendor's session-expiry text is a session loss even
// though it does not carry the documented takeover marker, so Alive() turns
// false and the runtime can contain the slot. The last case is the negative
// control: an unknown data-channel refusal leaves the session Alive instead of
// guessing a loss.
func TestFactoryClassifiesEquivalentSessionLoss(t *testing.T) {
	for _, tc := range []struct {
		name      string
		refusal   string
		wantAlive bool
	}{
		{name: "documented_takeover", refusal: vendorKickMarker, wantAlive: false},
		{name: "session_expired", refusal: "session expired; authenticate again", wantAlive: false},
		{name: "invalid_session_relogin", refusal: "invalid session, please re-login", wantAlive: false},
		{name: "unknown_refusal_keeps_session", refusal: "upstream connect failed", wantAlive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alive, openErr := openRefusedFlow(t, tc.refusal)
			if openErr == nil {
				t.Fatal("the vendor refusal unexpectedly opened a flow")
			}
			if alive != tc.wantAlive {
				t.Errorf("Alive after data-channel refusal = %t, want %t; refusal=%q error=%v",
					alive, tc.wantAlive, tc.refusal, openErr)
			}
			if !strings.Contains(openErr.Error(), "data channel rejected") {
				t.Errorf("OpenFlow error = %v, want a data-channel rejection", openErr)
			}
		})
	}
}

// openRefusedFlow authenticates one real factory session against an in-process
// FV server whose data channel answers code=403 with refusal, then attempts one
// flow and returns the session's liveness plus the flow error.
func openRefusedFlow(t *testing.T, refusal string) (bool, error) {
	t.Helper()
	dev, err := NewDevice(LinuxClientVersion)
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}
	m := startMockFV(t, mockFVConfig{
		user:        mockUser,
		password:    mockPassword,
		cuid:        dev.CUID,
		cseid:       dev.CSEID,
		dataRefusal: refusal,
	})
	factory := NewFactory(Linux423Binary, &fvfacade.HTTPUpgrade{HandshakeTimeout: 5 * time.Second},
		fvfacade.AuthOptions{PublicKey: &m.priv.PublicKey, IdleTimeout: 200 * time.Millisecond})
	sess, err := factory.Open(context.Background(), SessionRequest{
		AccountID:       "acct-loss",
		Server:          mockServer(m),
		Device:          dev,
		UserName:        mockUser,
		ConnectPassword: Secret(mockPassword),
		Profile:         Linux423Binary,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close()
	live, ok := sess.(Liveness)
	if !ok {
		t.Fatal("the production session does not implement Liveness")
	}
	if !live.Alive() {
		t.Fatal("Alive() = false before any refusal")
	}
	_, openErr := sess.OpenFlow(context.Background(), mockTargetHost, mockTargetPort)
	m.wait(t)
	return live.Alive(), openErr
}

// TestSessionLossClassifierVocabulary pins the classifier itself: the
// documented marker, equivalent expiry/takeover vocabulary in any case, and the
// negative controls that must not take a healthy session out of service.
func TestSessionLossClassifierVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "documented", err: errors.New(`fv: data channel rejected: "code=403\nfm=disconnected by another connection\n"`), want: true},
		{name: "expired_upper", err: errors.New(`fv: data channel rejected: "code=403\nfm=Session Expired\n"`), want: true},
		{name: "authenticate_again", err: errors.New(`fv: data channel rejected: "code=403\nfm=please authenticate again\n"`), want: true},
		{name: "another_connection", err: errors.New(`fv: data channel rejected: "code=403\nfm=account moved to another connection\n"`), want: true},
		{name: "destination_refused", err: errors.New("fv: socks connect failed: rep=5"), want: false},
		{name: "network_error", err: errors.New("dial tcp 203.0.113.10:443: connect: connection refused"), want: false},
		{name: "unknown_refusal", err: errors.New(`fv: data channel rejected: "code=403\nfm=upstream connect failed\n"`), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSessionLoss(tc.err); got != tc.want {
				t.Errorf("isSessionLoss(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}
