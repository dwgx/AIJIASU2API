package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// flexIntEnvelope wraps one par.fv fragment in the loginSync response shape,
// with a per-server secret the error path must never echo.
func flexIntEnvelope(fv string) []byte {
	return []byte(`{"Login":{"AuthResult":"OK","LoginToken":"tok-1|user","ConnectPassword":"cp-1"},` +
		`"Servers":{"s1":{"id":"s1","vsid":1,"ctry":"CN","svr":"svr.example","tit":"T","cmt":"",` +
		`"sts":"ok","ord":"1","cat":["cn"],"par":{"fv":{"u":1001,` + fv +
		`,"ps":"per-server-secret"}},"loc":[{"c":"CN","n":"Shanghai","o":1}]}}}`)
}

// decodeServersEnvelope decodes one flexIntEnvelope variant and returns its
// single server entry.
func decodeServersEnvelope(t *testing.T, body []byte) Server {
	t.Helper()
	var env struct {
		Login   *LoginResult      `json:"Login"`
		Servers map[string]Server `json:"Servers"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	server, ok := env.Servers["s1"]
	if !ok {
		t.Fatalf("Servers = %v, want s1", env.Servers)
	}
	return server
}

// TestServerFlexIntShapes pins every accepted numeric shape of the loginSync
// server fields: a live LoginSync (AJS-97) sent par.fv.u and par.fv.p as JSON
// strings, which the stock int decoder rejected with
// "cannot unmarshal string into Go struct field ServerFV.Servers.par.fv.u of
// type int". Port() and Addr() must behave identically for both shapes, and
// FlexInt must still marshal as a bare JSON number.
func TestServerFlexIntShapes(t *testing.T) {
	cases := []struct {
		name   string
		fv     string
		wantU  int
		wantP  int
		wantAd string
	}{
		{"numeric strings", `"u":"123","p":"443"`, 123, 443, "svr.example:443"},
		{"json numbers", `"u":1002,"p":8443`, 1002, 8443, "svr.example:8443"},
		{"negative number", `"u":-1,"p":443`, -1, 443, "svr.example:443"},
		{"negative string", `"u":"-1","p":"443"`, -1, 443, "svr.example:443"},
		{"null u", `"u":null,"p":443`, 0, 443, "svr.example:443"},
		{"empty string u", `"u":"","p":"443"`, 0, 443, "svr.example:443"},
		{"null p", `"u":1,"p":null`, 1, 0, ""},
		{"empty string p", `"u":1,"p":""`, 1, 0, ""},
		{"absent p", `"u":1`, 1, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := decodeServersEnvelope(t, flexIntEnvelope(tc.fv))
			if int(server.Par.FV.U) != tc.wantU {
				t.Fatalf("par.fv.u = %d, want %d", server.Par.FV.U, tc.wantU)
			}
			if got := server.Port(); got != tc.wantP {
				t.Fatalf("Port() = %d, want %d", got, tc.wantP)
			}
			if got := server.Addr(); got != tc.wantAd {
				t.Fatalf("Addr() = %q, want %q", got, tc.wantAd)
			}
			// The neighbouring fields must still decode, and vsid / loc.o
			// share the same FlexInt path.
			if server.Vsid != 1 || server.Par.FV.PS != "per-server-secret" ||
				len(server.Loc) != 1 || server.Loc[0].O != 1 {
				t.Fatalf("surrounding fields = %+v", server)
			}
		})
	}
}

// TestServerFlexIntErrors pins the rejection path: a non-numeric, fractional,
// exponent, or overflowing value fails the decode with an error naming the
// failing field path, the value, and the accepted shapes — never the
// per-server secret or the session credentials.
func TestServerFlexIntErrors(t *testing.T) {
	cases := []struct {
		name    string
		fv      string
		wantSub string
	}{
		{"non-numeric p", `"u":1,"p":"abc"`, "abc"},
		{"non-numeric u", `"u":"soon","p":443`, "soon"},
		{"fractional string p", `"u":1,"p":"44.5"`, "44.5"},
		{"fractional number p", `"u":1,"p":44.5`, "44.5"},
		{"exponent number p", `"u":1,"p":4e2`, "4e2"},
		{"overflow string p", `"u":1,"p":"99999999999999999999"`, "99999999999999999999"},
		{"json array p", `"u":1,"p":[443]`, "[443]"},
		{"json object p", `"u":1,"p":{"p":443}`, "{"},
		{"json bool p", `"u":1,"p":true`, "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var env struct {
				Login   *LoginResult      `json:"Login"`
				Servers map[string]Server `json:"Servers"`
			}
			err := json.Unmarshal(flexIntEnvelope(tc.fv), &env)
			if err == nil {
				t.Fatalf("Unmarshal succeeded with %+v, want error", env.Servers)
			}
			msg := err.Error()
			// The decoder decorates *json.UnmarshalTypeError with the failing
			// field path, the same shape as the live failure message. The u
			// cases fail on par.fv.u; every other case fails on par.fv.p.
			wantPath := "ServerFV.Servers.par.fv.p"
			if tc.name == "non-numeric u" {
				wantPath = "ServerFV.Servers.par.fv.u"
			}
			if !strings.Contains(msg, wantPath) {
				t.Fatalf("error %q does not name the failing field path %s", msg, wantPath)
			}
			if !strings.Contains(msg, "FlexInt") {
				t.Fatalf("error %q does not name the field type", msg)
			}
			if !strings.Contains(msg, tc.wantSub) {
				t.Fatalf("error %q does not include the value %q", msg, tc.wantSub)
			}
			if !strings.Contains(msg, "numeric string") {
				t.Fatalf("error %q does not list the accepted shapes", msg)
			}
			for _, secret := range []string{"per-server-secret", "tok-1", "cp-1"} {
				if strings.Contains(msg, secret) {
					t.Fatalf("error %q leaks %q", msg, secret)
				}
			}
		})
	}
}

// TestServerFlexIntErrorNamesField pins that each FlexInt field reports its
// own path: u, p, vsid, and loc[].o fail independently with distinct paths.
func TestServerFlexIntErrorNamesField(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantPath string
	}{
		{"fv u", `{"Servers":{"s1":{"par":{"fv":{"u":"soon","p":443}}}}}`, "ServerFV.Servers.par.fv.u"},
		{"fv p", `{"Servers":{"s1":{"par":{"fv":{"u":1,"p":"soon"}}}}}`, "ServerFV.Servers.par.fv.p"},
		{"vsid", `{"Servers":{"s1":{"vsid":"soon"}}}`, "Server.Servers.vsid"},
		{"loc o", `{"Servers":{"s1":{"loc":[{"c":"CN","n":"S","o":"soon"}]}}}`, "ServerLoc.Servers.loc.o"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var env struct {
				Servers map[string]Server `json:"Servers"`
			}
			err := json.Unmarshal([]byte(tc.body), &env)
			if err == nil {
				t.Fatalf("Unmarshal succeeded, want error naming %s", tc.wantPath)
			}
			if !strings.Contains(err.Error(), tc.wantPath) {
				t.Fatalf("error %q does not name %s", err, tc.wantPath)
			}
		})
	}
}

// TestLoginSyncDecodesStringServerNumbers replays the live failure end to
// end: the vendor sent par.fv.u and par.fv.p as strings and json.Unmarshal
// rejected the response before the Servers map was built. LoginSync must now
// decode it and report the FV endpoint.
func TestLoginSyncDecodesStringServerNumbers(t *testing.T) {
	device := DefaultDevice()
	if err := device.EnsureIDs(); err != nil {
		t.Fatalf("EnsureIDs: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Login":{"AuthResult":"OK","LoginToken":"tok|user",`+
			`"UserName":"u1","ConnectPassword":"cp-1"},`+
			`"Servers":{"s1":{"id":"s1","vsid":"1","ctry":"CN","svr":"svr.example",`+
			`"tit":"T","cmt":"","sts":"ok","ord":"1","cat":["cn"],`+
			`"par":{"fv":{"u":"123","p":"443","ps":"per-server-secret"}},`+
			`"loc":[{"c":"CN","n":"Shanghai","o":"1"}]}}}`)
	}))
	defer srv.Close()

	client := NewClient([]string{srv.URL}, device)
	client.HTTP = srv.Client()
	client.LoginToken = "tok|user"
	client.UserName = "u1"

	servers, err := client.LoginSync(context.Background())
	if err != nil {
		t.Fatalf("LoginSync: %v", err)
	}
	server, ok := servers["s1"]
	if !ok {
		t.Fatalf("Servers = %v, want s1", servers)
	}
	if got := server.Addr(); got != "svr.example:443" {
		t.Fatalf("Addr() = %q, want svr.example:443", got)
	}
	if got := server.Port(); got != 443 {
		t.Fatalf("Port() = %d, want 443", got)
	}
	if server.Par.FV.U != 123 || server.Vsid != 1 || server.Loc[0].O != 1 {
		t.Fatalf("string numerics = u %d vsid %d o %d, want 123/1/1",
			server.Par.FV.U, server.Vsid, server.Loc[0].O)
	}
	if client.ConnectPassword != "cp-1" {
		t.Fatalf("ConnectPassword = %q, want the refreshed cp-1", client.ConnectPassword)
	}
}

// TestFlexIntMarshalsAsNumber pins the public JSON shape: FlexInt encodes as
// a bare number, so re-encoded server listings stay numeric.
func TestFlexIntMarshalsAsNumber(t *testing.T) {
	out, err := json.Marshal(ServerPar{FV: ServerFV{U: 123, P: 443, PS: "s"}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got, want := string(out), `{"fv":{"u":123,"p":443,"ps":"s"}}`; got != want {
		t.Fatalf("Marshal = %s, want %s", got, want)
	}
}
