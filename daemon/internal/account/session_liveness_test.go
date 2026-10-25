package account

import (
	"context"
	"testing"
	"time"

	"ajsproxy/fvfacade"
)

// TestActiveLivenessProbeClassifiesIdleLoss proves the detection half of
// REVIEW-MULTISLOT-RESIDUALS finding 1 on the production adapter, with no
// client flow at all: an idle vendor kick is only visible through the active
// probe. The healthy control is a live session whose destination is refused
// (the probe's sentinel target): the probe must report "not lost" and leave
// Alive true, so a transient or target-level failure never takes a slot out of
// service.
func TestActiveLivenessProbeClassifiesIdleLoss(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      mockFVConfig
		wantLost bool
	}{
		{
			// A flow-level refusal is the probe's healthy outcome: the
			// authenticated session accepted the data channel and only the
			// destination failed. It must never be classified as a loss.
			name: "live session with a flow-level refusal",
			cfg:  mockFVConfig{dataRefusal: "socks connect failed: rep=5"},
		},
		{
			name:     "idle vendor kick",
			cfg:      mockFVConfig{dataRefusal: vendorKickMarker},
			wantLost: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, mock := openLivenessProbeSession(t, tc.cfg)
			live, ok := session.(Liveness)
			if !ok {
				t.Fatal("the production session does not implement Liveness")
			}
			if !live.Alive() {
				t.Fatal("Alive() = false before the probe")
			}
			prober, ok := session.(interface {
				SessionLost(context.Context) bool
			})
			if !ok {
				t.Fatal("the production session does not implement the active liveness probe")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if got := prober.SessionLost(ctx); got != tc.wantLost {
				t.Fatalf("SessionLost() = %t, want %t", got, tc.wantLost)
			}
			if got := live.Alive(); got == tc.wantLost {
				t.Fatalf("Alive() = %t after SessionLost() = %t", got, tc.wantLost)
			}
			mock.wait(t)
			_ = session.Close()
		})
	}
}

// openLivenessProbeSession authenticates one real factory session against an
// in-process FV server. The caller owns Close and must call mock.wait once the
// probe's data exchange is done.
func openLivenessProbeSession(t *testing.T, cfg mockFVConfig) (FlowSession, *mockFV) {
	t.Helper()
	dev, err := NewDevice(LinuxClientVersion)
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}
	cfg.user = mockUser
	cfg.password = mockPassword
	cfg.cuid = dev.CUID
	cfg.cseid = dev.CSEID
	m := startMockFV(t, cfg)
	factory := NewFactory(Linux423Binary, &fvfacade.HTTPUpgrade{HandshakeTimeout: 5 * time.Second},
		fvfacade.AuthOptions{PublicKey: &m.priv.PublicKey, IdleTimeout: 200 * time.Millisecond})
	sess, err := factory.Open(context.Background(), SessionRequest{
		AccountID:       "acct-probe",
		Server:          mockServer(m),
		Device:          dev,
		UserName:        mockUser,
		ConnectPassword: Secret(mockPassword),
		Profile:         Linux423Binary,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return sess, m
}
