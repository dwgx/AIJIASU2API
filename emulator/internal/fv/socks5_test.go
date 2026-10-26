package fv

import (
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
)

// TestReadSocks5ReplyATYP checks that the reply length follows ATYP and that
// the CTR position stays exact: a sentinel after the reply must decrypt intact
// with the same stream.
func TestReadSocks5ReplyATYP(t *testing.T) {
	key := bytes.Repeat([]byte{0x7e}, SessionKeySize)
	iv := bytes.Repeat([]byte{0x21}, IVSize)
	sentinel := []byte("TAIL!")

	ipv6 := net.ParseIP("2001:db8::1").To16()
	if ipv6 == nil {
		t.Fatal("ParseIP returned nil")
	}
	domain := append([]byte{5, 0, 0, 3, 11}, []byte("example.com")...)
	domain = append(domain, 0x00, 0x50)

	cases := []struct {
		name  string
		plain []byte
		want  Socks5Reply
	}{
		{
			name:  "ipv4",
			plain: []byte{5, 0, 0, 1, 10, 0, 0, 1, 0x01, 0xbb},
			want:  Socks5Reply{Rep: 0, Atyp: 1, BndAddr: "10.0.0.1", BndPort: 443},
		},
		{
			name:  "domain",
			plain: domain,
			want:  Socks5Reply{Rep: 0, Atyp: 3, BndAddr: "example.com", BndPort: 80},
		},
		{
			name:  "ipv6",
			plain: append([]byte{5, 0, 0, 4}, append(ipv6, 0x00, 0x50)...),
			want:  Socks5Reply{Rep: 0, Atyp: 4, BndAddr: "2001:db8::1", BndPort: 80},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plain := append(append([]byte(nil), tc.plain...), sentinel...)
			encrypt, err := NewCTR(key, iv)
			if err != nil {
				t.Fatalf("NewCTR encrypt: %v", err)
			}
			ciphertext := make([]byte, len(plain))
			encrypt.XORKeyStream(ciphertext, plain)

			decrypt, err := NewCTR(key, iv)
			if err != nil {
				t.Fatalf("NewCTR decrypt: %v", err)
			}
			r := bytes.NewReader(ciphertext)
			got, err := ReadSocks5Reply(r, decrypt)
			if err != nil {
				t.Fatalf("ReadSocks5Reply: %v", err)
			}
			if got != tc.want {
				t.Fatalf("reply = %+v, want %+v", got, tc.want)
			}
			rest := make([]byte, len(sentinel))
			if _, err := io.ReadFull(r, rest); err != nil {
				t.Fatalf("read sentinel: %v", err)
			}
			decrypt.XORKeyStream(rest, rest)
			if !bytes.Equal(rest, sentinel) {
				t.Fatalf("sentinel = %q, want %q", rest, sentinel)
			}
		})
	}

	t.Run("unknown atyp fails closed", func(t *testing.T) {
		plain := []byte{5, 0, 0, 5, 1, 2, 3, 4}
		encrypt, err := NewCTR(key, iv)
		if err != nil {
			t.Fatalf("NewCTR encrypt: %v", err)
		}
		ciphertext := make([]byte, len(plain))
		encrypt.XORKeyStream(ciphertext, plain)
		decrypt, err := NewCTR(key, iv)
		if err != nil {
			t.Fatalf("NewCTR decrypt: %v", err)
		}
		_, err = ReadSocks5Reply(bytes.NewReader(ciphertext), decrypt)
		if err == nil {
			t.Fatal("unknown ATYP was accepted")
		}
		if !strings.Contains(err.Error(), "ATYP 5") {
			t.Fatalf("error = %v, want the unsupported ATYP", err)
		}
	})
}
