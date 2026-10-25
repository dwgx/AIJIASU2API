package account

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"ajsaccountd/internal/model"
	"ajsproxy/apifacade"
	"ajsproxy/fvfacade"
)

const (
	mockSID        = "sid-1"
	mockTargetHost = "echo.example"
	mockTargetPort = 80
	mockUser       = "alice"
	mockPassword   = "cp-1"
	// devDim is the captured Linux 4.2.3.0 _os_dim/dim value
	// (research/60-dynamic/captures/fvmock-1.log).
	devDim = "lid=REDACTED,8e415e603088426aa42fed420212150c"
)

// zeroIV is the CTR position the carrier uses for its outer key stream.
var zeroIV = make([]byte, fvfacade.IVSize)

// TestFactoryOpenAgainstMockFV drives the factory end to end against an
// in-process FV server: binary-carrier auth with a 1024-bit test key, the auth
// map plus half-close, the data channel with its SOCKS5 CONNECT, and an echo
// relay. The server uses only fvfacade exports, so it cannot reach into the
// emulator's internal package.
func TestFactoryOpenAgainstMockFV(t *testing.T) {
	dev, err := NewDevice(LinuxClientVersion)
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}
	m := startMockFV(t, mockFVConfig{
		user:     mockUser,
		password: mockPassword,
		cuid:     dev.CUID,
		cseid:    dev.CSEID,
		host:     mockTargetHost,
		port:     mockTargetPort,
	})
	f := NewFactory(Linux423Binary, &fvfacade.HTTPUpgrade{HandshakeTimeout: 5 * time.Second}, fvfacade.AuthOptions{
		PublicKey:   &m.priv.PublicKey,
		IdleTimeout: 200 * time.Millisecond,
	})
	req := SessionRequest{
		AccountID:       "acct-1",
		Server:          mockServer(m),
		Device:          dev,
		UserName:        mockUser,
		ConnectPassword: Secret(mockPassword),
		Profile:         Linux423Binary,
	}
	if got := fmt.Sprintf("%+v", req); strings.Contains(got, mockPassword) {
		t.Fatalf("SessionRequest formatting leaks the connect password: %s", got)
	}

	sess, err := f.Open(context.Background(), req)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := sess.SID(); got != mockSID {
		t.Fatalf("SID = %q, want %q", got, mockSID)
	}
	if got := sess.Fields()["sip"]; got != "10.0.0.1" {
		t.Fatalf("Fields[sip] = %q, want 10.0.0.1", got)
	}
	if !sess.Healthy() {
		t.Fatal("Healthy() = false, want true")
	}

	flow, err := sess.OpenFlow(context.Background(), mockTargetHost, mockTargetPort)
	if err != nil {
		t.Fatalf("OpenFlow: %v", err)
	}
	if _, err := flow.Write([]byte("ping")); err != nil {
		t.Fatalf("flow write: %v", err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(flow, echo); err != nil {
		t.Fatalf("flow read: %v", err)
	}
	if string(echo) != "ping" {
		t.Fatalf("echo = %q, want ping", echo)
	}
	if err := flow.Close(); err != nil {
		t.Fatalf("flow close: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := sess.OpenFlow(context.Background(), mockTargetHost, mockTargetPort); err == nil {
		t.Fatal("OpenFlow after Close succeeded, want an error")
	}
	m.wait(t)
}

// TestFactoryOpenRejectsDeniedAuth proves an r=denied auth map surfaces as
// *fvfacade.AuthError and that the rejection carries no credential.
func TestFactoryOpenRejectsDeniedAuth(t *testing.T) {
	dev, err := NewDevice(LinuxClientVersion)
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}
	m := startMockFV(t, mockFVConfig{
		reject:   true,
		user:     mockUser,
		password: mockPassword,
		cuid:     dev.CUID,
		cseid:    dev.CSEID,
	})
	f := NewFactory(Linux423Binary, &fvfacade.HTTPUpgrade{HandshakeTimeout: 5 * time.Second}, fvfacade.AuthOptions{
		PublicKey:   &m.priv.PublicKey,
		IdleTimeout: 200 * time.Millisecond,
	})
	_, err = f.Open(context.Background(), SessionRequest{
		AccountID:       "acct-1",
		Server:          mockServer(m),
		Device:          dev,
		UserName:        mockUser,
		ConnectPassword: Secret(mockPassword),
		Profile:         Linux423Binary,
	})
	var authErr *fvfacade.AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("Open error = %v, want *fvfacade.AuthError", err)
	}
	if got := authErr.Fields["r"]; got != "denied" {
		t.Fatalf("AuthError r = %q, want denied", got)
	}
	if strings.Contains(err.Error(), mockPassword) {
		t.Fatalf("rejection leaks the connect password: %v", err)
	}
	m.wait(t)
}

// TestLinux423BinaryProfile pins the default profile the factory ships with.
func TestLinux423BinaryProfile(t *testing.T) {
	p := Linux423Binary
	if p.ClientVersion != "4.2.3.0" || p.CoreVersion != "20241105-1" {
		t.Fatalf("version claims = %q/%q", p.ClientVersion, p.CoreVersion)
	}
	if p.ProtocolVersion != 2 || p.OAEPHash != "SHA-1" || p.SigningMode != "none" ||
		p.Transport != "httpupgrade" || p.RequestShape != apifacade.ShapeLinux423 {
		t.Fatalf("profile = %+v", p)
	}
	f := NewFactory(p, &fvfacade.HTTPUpgrade{}, fvfacade.AuthOptions{})
	if got := f.Profile(); got != p {
		t.Fatalf("Profile() = %+v, want %+v", got, p)
	}
}

// TestLinux430BinaryProfile pins the 4.3.0.1 profile: the 4.3.0.1 version
// claims on the same protocol-2 binary carrier, OAEP-SHA1 and unsigned
// requests, because the 4.3.0.1 signer's canonical field order is [UNKNOWN]
// and no live request has exercised it.
func TestLinux430BinaryProfile(t *testing.T) {
	p := Linux430Binary
	if p.Name != "linux-4.3.0.1-binary" || p.ClientVersion != "4.3.0.1" || p.CoreVersion != "20260525-1" {
		t.Fatalf("name/version claims = %q/%q/%q", p.Name, p.ClientVersion, p.CoreVersion)
	}
	if p.ProtocolVersion != 2 || p.OAEPHash != "SHA-1" || p.SigningMode != "none" ||
		p.Transport != "httpupgrade" || p.RequestShape != apifacade.ShapeLinux423 {
		t.Fatalf("profile = %+v", p)
	}
	if p == Linux423Binary {
		t.Fatal("Linux430Binary equals Linux423Binary")
	}

	protocol, err := NewProtocolForProfile(p)
	if err != nil {
		t.Fatalf("NewProtocolForProfile: %v", err)
	}
	if !protocol.Available() || protocol.Transport.Name() != "httpupgrade" {
		t.Fatalf("protocol = %+v", protocol)
	}
	if upgrade, ok := protocol.Transport.(*fvfacade.HTTPUpgrade); !ok || upgrade.ProtocolVersion != 2 {
		t.Fatalf("transport = %#v, want *fvfacade.HTTPUpgrade with ProtocolVersion 2", protocol.Transport)
	}

	f := NewFactory(p, protocol.Transport, fvfacade.AuthOptions{})
	if got := f.Profile(); got != p {
		t.Fatalf("Profile() = %+v, want %+v", got, p)
	}
}

// TestLinux430DeviceDescriptor pins the device a 4.3.0.1 login presents: the
// Linux descriptor is unchanged from 4.2.3.0 (LINUX.md §7), only the _v claim
// moves.
func TestLinux430DeviceDescriptor(t *testing.T) {
	device, err := NewDeviceForProfile(Linux430Binary)
	if err != nil {
		t.Fatalf("NewDeviceForProfile: %v", err)
	}
	if device.Version != Linux430ClientVersion || device.Platform != "linux" ||
		device.OSDevice != "x86_64" || device.CSEID != "linux" || device.Lang != "en" {
		t.Fatalf("device = %+v", device)
	}
	if len(device.UID) != 20 || device.CUID != device.UID {
		t.Fatalf("UID/CUID = %q/%q, want a 20-hex uid", device.UID, device.CUID)
	}
	if len(device.DeviceID) != 16 {
		t.Fatalf("DeviceID = %q, want 16 hex characters", device.DeviceID)
	}
}

// mockFVConfig is what the mock server expects on the wire.
type mockFVConfig struct {
	reject   bool
	user     string
	password string
	cuid     string
	cseid    string
	host     string
	port     int
	// dataRefusal, when set, makes the data channel answer
	// "code=403 / fm=<dataRefusal>" instead of relaying a flow. It models the
	// vendor refusing an authenticated SID (AJS-353).
	dataRefusal string
}

// mockFV is an in-process FV server speaking the protocol-2 binary carrier.
type mockFV struct {
	ln   net.Listener
	echo net.Listener
	priv *rsa.PrivateKey
	cfg  mockFVConfig
	errs chan error
}

// startMockFV starts the echo target and the FV listener, and runs one
// auth/data exchange in the background.
func startMockFV(t *testing.T, cfg mockFVConfig) *mockFV {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate 1024-bit test key: %v", err)
	}
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo target: %v", err)
	}
	go echoLoop(echo)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		echo.Close()
		t.Fatalf("listen mock FV server: %v", err)
	}
	m := &mockFV{ln: ln, echo: echo, priv: priv, cfg: cfg, errs: make(chan error, 1)}
	go func() { m.errs <- m.serve() }()
	t.Cleanup(func() {
		ln.Close()
		echo.Close()
	})
	return m
}

func (m *mockFV) wait(t *testing.T) {
	t.Helper()
	select {
	case err := <-m.errs:
		if err != nil {
			t.Fatalf("mock FV server: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mock FV server did not finish")
	}
}

func mockServer(m *mockFV) apifacade.Server {
	host, portText, _ := net.SplitHostPort(m.ln.Addr().String())
	port, _ := strconv.Atoi(portText)
	return apifacade.Server{
		ID:  "srv-1",
		Svr: host,
		Sts: "ok",
		Par: apifacade.ServerPar{FV: apifacade.ServerFV{P: apifacade.FlexInt(port)}},
	}
}

func (m *mockFV) serve() error {
	authConn, err := m.ln.Accept()
	if err != nil {
		return fmt.Errorf("accept auth connection: %w", err)
	}
	defer authConn.Close()
	key, sessionKey, iv, err := m.authenticate(authConn)
	if err != nil {
		return err
	}

	respText := "code=200\nr=ok\ns=test\nsid=" + mockSID + "\n"
	mapText := "c=aur\nr=ok\ns=test\nsid=" + mockSID + "\nok=1\nsip=10.0.0.1\n"
	if m.cfg.reject {
		respText = "code=200\n"
		mapText = "r=denied\nfm=bad connect password\n"
	}
	if err := writeCarrierText(authConn, key, respText); err != nil {
		return err
	}
	mapCT, err := fvfacade.AESCTR(sessionKey, iv, []byte(mapText))
	if err != nil {
		return err
	}
	if _, err := authConn.Write(mapCT); err != nil {
		return fmt.Errorf("write auth map: %w", err)
	}
	// The client reads the auth map until EOF: the server must half-close.
	if err := closeWrite(authConn); err != nil {
		return err
	}
	if m.cfg.reject {
		return nil
	}
	return m.serveData(sessionKey)
}

// authenticate decodes the binary-carrier auth request and returns the outer
// key, the RSA-OAEP session key and the body IV. It verifies the fields the
// factory is supposed to map.
func (m *mockFV) authenticate(conn net.Conn) ([]byte, []byte, []byte, error) {
	key, fields, err := readCarrierText(conn)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read auth request: %w", err)
	}
	block, err := base64.StdEncoding.DecodeString(fields["k"])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("decode k field: %w", err)
	}
	if len(block) <= len(fvfacade.OAEPBlockMagic) || string(block[:len(fvfacade.OAEPBlockMagic)]) != fvfacade.OAEPBlockMagic {
		return nil, nil, nil, fmt.Errorf("k field is not an %s block", fvfacade.OAEPBlockMagic)
	}
	hash, err := fvfacade.HashByName(Linux423Binary.OAEPHash)
	if err != nil {
		return nil, nil, nil, err
	}
	sessionKey, err := rsa.DecryptOAEP(hash.New(), rand.Reader, m.priv, block[len(fvfacade.OAEPBlockMagic):], nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("decrypt OAEP session key: %w", err)
	}
	if len(sessionKey) != fvfacade.SessionKeySize {
		return nil, nil, nil, fmt.Errorf("session key is %d bytes, want %d", len(sessionKey), fvfacade.SessionKeySize)
	}
	bodyLen, err := strconv.Atoi(fields["l"])
	if err != nil || bodyLen <= 0 {
		return nil, nil, nil, fmt.Errorf("l field = %q", fields["l"])
	}
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, nil, nil, fmt.Errorf("read auth body: %w", err)
	}
	iv, err := hex.DecodeString(fields["n"])
	if err != nil || len(iv) != fvfacade.IVSize {
		return nil, nil, nil, fmt.Errorf("n field = %q", fields["n"])
	}
	plain, err := fvfacade.AESCTR(sessionKey, iv, body)
	if err != nil {
		return nil, nil, nil, err
	}
	auth := fvfacade.ParseFields(string(plain))
	if auth["u"] != m.cfg.user || auth["p"] != m.cfg.password {
		return nil, nil, nil, fmt.Errorf("auth u/p = %q/%q", auth["u"], auth["p"])
	}
	if auth["cuid"] != m.cfg.cuid || auth["cseid"] != m.cfg.cseid {
		return nil, nil, nil, fmt.Errorf("auth cuid/cseid = %q/%q, want %q/%q",
			auth["cuid"], auth["cseid"], m.cfg.cuid, m.cfg.cseid)
	}
	if auth["osp"] != "linux" || auth["osd"] != "x86_64" {
		return nil, nil, nil, fmt.Errorf("auth osp/osd = %q/%q, want linux/x86_64", auth["osp"], auth["osd"])
	}
	if auth["dim"] != devDim {
		return nil, nil, nil, fmt.Errorf("auth dim = %q, want the captured value", auth["dim"])
	}
	if auth["v"] != Linux423Binary.ClientVersion || auth["cv"] != Linux423Binary.CoreVersion {
		return nil, nil, nil, fmt.Errorf("auth v/cv = %q/%q", auth["v"], auth["cv"])
	}
	if auth["st"] != "ajs" {
		return nil, nil, nil, fmt.Errorf("auth st = %q", auth["st"])
	}
	return key, sessionKey, iv, nil
}

// serveData accepts the data connection, checks the SOCKS5 CONNECT, replies
// 05 00 and relays the stream to the echo target.
func (m *mockFV) serveData(sessionKey []byte) error {
	conn, err := m.ln.Accept()
	if err != nil {
		return fmt.Errorf("accept data connection: %w", err)
	}
	defer conn.Close()
	key, fields, err := readCarrierText(conn)
	if err != nil {
		return fmt.Errorf("read data request: %w", err)
	}
	if fields["s"] != mockSID {
		return fmt.Errorf("data s = %q, want %q", fields["s"], mockSID)
	}
	if fields["k"] != "" || fields["l"] != "" {
		return fmt.Errorf("data text carries k/l: %v", fields)
	}
	div, err := hex.DecodeString(fields["n"])
	if err != nil || len(div) != fvfacade.IVSize {
		return fmt.Errorf("data n = %q", fields["n"])
	}
	if m.cfg.dataRefusal != "" {
		// The authenticated SID is refused before any destination exchange,
		// exactly the vendor's session-loss shape.
		return writeCarrierText(conn, key, "code=403\nfm="+m.cfg.dataRefusal+"\n")
	}
	up, err := fvfacade.NewCTR(sessionKey, div)
	if err != nil {
		return err
	}
	host, port, err := readSocks5Connect(conn, up)
	if err != nil {
		return err
	}
	if host != m.cfg.host || port != m.cfg.port {
		return fmt.Errorf("SOCKS target = %s:%d, want %s:%d", host, port, m.cfg.host, m.cfg.port)
	}
	if err := writeCarrierText(conn, key, "code=200\n"); err != nil {
		return err
	}
	down, err := fvfacade.NewCTR(sessionKey, div)
	if err != nil {
		return err
	}
	reply := make([]byte, 10)
	down.XORKeyStream(reply, []byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	if _, err := conn.Write(reply); err != nil {
		return fmt.Errorf("write SOCKS5 reply: %w", err)
	}
	echoConn, err := net.Dial("tcp", m.echo.Addr().String())
	if err != nil {
		return fmt.Errorf("dial echo target: %w", err)
	}
	defer echoConn.Close()
	relay(conn, echoConn, up, down)
	return nil
}

// readSocks5Connect decodes 05 01 00 03 <len> <host> <port> and advances the
// keystream past it.
func readSocks5Connect(conn net.Conn, stream cipher.Stream) (string, int, error) {
	head := make([]byte, 5)
	if _, err := io.ReadFull(conn, head); err != nil {
		return "", 0, fmt.Errorf("read SOCKS5 head: %w", err)
	}
	stream.XORKeyStream(head, head)
	if head[0] != 0x05 || head[1] != 0x01 || head[2] != 0x00 || head[3] != 0x03 {
		return "", 0, fmt.Errorf("SOCKS5 head = %x", head)
	}
	rest := make([]byte, int(head[4])+2)
	if _, err := io.ReadFull(conn, rest); err != nil {
		return "", 0, fmt.Errorf("read SOCKS5 host/port: %w", err)
	}
	stream.XORKeyStream(rest, rest)
	return string(rest[:len(rest)-2]), int(binary.BigEndian.Uint16(rest[len(rest)-2:])), nil
}

// relay copies between the carrier connection and the echo target. up is the
// client-to-server keystream positioned after the SOCKS5 request; down is the
// server-to-client keystream positioned after the 10-byte SOCKS5 reply.
func relay(conn, echo net.Conn, up, down cipher.Stream) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(echo, cipher.StreamReader{S: up, R: conn})
		if tcp, ok := echo.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	_, _ = io.Copy(cipher.StreamWriter{S: down, W: conn}, echo)
	_ = conn.Close() // unblock the client-to-server copy
	<-done
}

// echoLoop accepts connections and echoes their bytes back.
func echoLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			_, _ = io.Copy(c, c)
		}(conn)
	}
}

// readCarrierText reads [16B K][AES-CTR(K,0)(4B len || text)] and returns the
// key and the parsed text. The body is left for the caller.
func readCarrierText(conn net.Conn) ([]byte, map[string]string, error) {
	key := make([]byte, fvfacade.IVSize)
	if _, err := io.ReadFull(conn, key); err != nil {
		return nil, nil, fmt.Errorf("read carrier key: %w", err)
	}
	head := make([]byte, fvfacade.HeaderSize)
	if _, err := io.ReadFull(conn, head); err != nil {
		return nil, nil, fmt.Errorf("read carrier header: %w", err)
	}
	plainHead, err := fvfacade.AESCTR(key, zeroIV, head)
	if err != nil {
		return nil, nil, err
	}
	if plainHead[0] != 0 || plainHead[1] != 0 {
		return nil, nil, fmt.Errorf("carrier length prefix is %#x %#x", plainHead[0], plainHead[1])
	}
	textLen := int(binary.BigEndian.Uint16(plainHead[2:4]))
	textCT := make([]byte, textLen)
	if _, err := io.ReadFull(conn, textCT); err != nil {
		return nil, nil, fmt.Errorf("read carrier text: %w", err)
	}
	plain, err := fvfacade.AESCTR(key, zeroIV, append(head, textCT...))
	if err != nil {
		return nil, nil, err
	}
	return key, fvfacade.ParseFields(string(plain[fvfacade.HeaderSize:])), nil
}

// writeCarrierText writes [AES-CTR(K,0)(4B len || text)].
func writeCarrierText(conn net.Conn, key []byte, text string) error {
	plain := make([]byte, fvfacade.HeaderSize+len(text))
	binary.BigEndian.PutUint16(plain[2:4], uint16(len(text)))
	copy(plain[fvfacade.HeaderSize:], text)
	ct, err := fvfacade.AESCTR(key, zeroIV, plain)
	if err != nil {
		return err
	}
	if _, err := conn.Write(ct); err != nil {
		return fmt.Errorf("write carrier text: %w", err)
	}
	return nil
}

func closeWrite(conn net.Conn) error {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return conn.Close()
	}
	return tcp.CloseWrite()
}

// TestSlotUserName pins the vendor tunnel name of one slot: the base name for
// slot 1 (and any index at or below one), base#N for higher slots, and an
// empty result when the login carried no user name at all.
func TestSlotUserName(t *testing.T) {
	cases := []struct {
		base  string
		index int
		want  string
	}{
		{base: "", index: 1, want: ""},
		{base: "", index: 2, want: ""},
		{base: "   ", index: 3, want: ""},
		{base: " vendor-user ", index: 1, want: "vendor-user"},
		{base: "vendor-user", index: 0, want: "vendor-user"},
		{base: "vendor-user", index: 2, want: "vendor-user#2"},
		{base: "vendor-user", index: 8, want: "vendor-user#8"},
	}
	for _, tc := range cases {
		if got := SlotUserName(tc.base, tc.index); got != tc.want {
			t.Fatalf("SlotUserName(%q, %d) = %q, want %q", tc.base, tc.index, got, tc.want)
		}
	}
}

// TestSessionRequestSlot pins the factory guard: a non-zero slot that is not a
// valid key fails before any network work, while the zero key means slot 1 and
// falls through to the factory's own checks.
func TestSessionRequestSlot(t *testing.T) {
	factory := NewFactory(Linux423Binary, nil, fvfacade.AuthOptions{})
	invalid := []model.SlotKey{
		{AccountID: "", Index: 2},
		{AccountID: "   ", Index: 1},
		{AccountID: "acc-01", Index: 9},
		{AccountID: "acc-01", Index: -1},
	}
	for _, key := range invalid {
		_, err := factory.Open(context.Background(), SessionRequest{Slot: key})
		if err == nil || !strings.Contains(err.Error(), "slot") {
			t.Fatalf("Open(slot %q) error = %v, want an invalid-slot refusal", key.String(), err)
		}
	}
	_, err := factory.Open(context.Background(), SessionRequest{})
	if err == nil || !strings.Contains(err.Error(), "no transport") {
		t.Fatalf("Open(zero slot) error = %v, want the transport refusal", err)
	}
}
