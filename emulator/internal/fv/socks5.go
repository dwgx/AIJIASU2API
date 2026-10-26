package fv

import (
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

// SOCKS5 reply constants (RFC 1928 §6). Only the fields the data channel
// needs are named.
const (
	Socks5ReplyVersion = 0x05
	Socks5AtypIPv4     = 0x01
	Socks5AtypDomain   = 0x03
	Socks5AtypIPv6     = 0x04
)

// Socks5Reply is the server's SOCKS5 CONNECT reply on the data channel.
type Socks5Reply struct {
	// Rep is the reply field: 0 is success.
	Rep byte
	// Atyp is the address type of the bound address.
	Atyp byte
	// BndAddr is the bound address: dotted quad for ATYP 1, a host name for
	// ATYP 3, and a canonical IPv6 literal for ATYP 4.
	BndAddr string
	// BndPort is the bound port.
	BndPort int
}

// ReadSocks5Reply reads one SOCKS5 CONNECT reply from r, decrypting every byte
// with stream so the CTR position stays exact for the payload that follows.
//
// The reply length depends on ATYP: 10 bytes for IPv4, 7+len(domain) for a
// domain name, and 22 for IPv6. An unknown ATYP is an error: the stream must
// never be re-read as a fixed 10-byte reply, because the remaining address
// bytes would then desynchronise the payload keystream.
func ReadSocks5Reply(r io.Reader, stream cipher.Stream) (Socks5Reply, error) {
	var reply Socks5Reply
	head := make([]byte, 4)
	if _, err := io.ReadFull(r, head); err != nil {
		return reply, fmt.Errorf("fv: read SOCKS5 reply header: %w", err)
	}
	stream.XORKeyStream(head, head)
	if head[0] != Socks5ReplyVersion {
		return reply, fmt.Errorf("fv: SOCKS5 reply version %d, want %d", head[0], Socks5ReplyVersion)
	}
	reply.Rep = head[1]
	reply.Atyp = head[3]

	switch reply.Atyp {
	case Socks5AtypIPv4:
		addr := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(r, addr); err != nil {
			return reply, fmt.Errorf("fv: read SOCKS5 reply rep=%d address: %w", reply.Rep, err)
		}
		stream.XORKeyStream(addr, addr)
		reply.BndAddr = net.IP(addr).String()
	case Socks5AtypIPv6:
		addr := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(r, addr); err != nil {
			return reply, fmt.Errorf("fv: read SOCKS5 reply rep=%d address: %w", reply.Rep, err)
		}
		stream.XORKeyStream(addr, addr)
		reply.BndAddr = net.IP(addr).String()
	case Socks5AtypDomain:
		var length [1]byte
		if _, err := io.ReadFull(r, length[:]); err != nil {
			return reply, fmt.Errorf("fv: read SOCKS5 reply rep=%d domain length: %w", reply.Rep, err)
		}
		stream.XORKeyStream(length[:], length[:])
		if length[0] == 0 {
			return reply, fmt.Errorf("fv: SOCKS5 reply rep=%d has an empty bound domain", reply.Rep)
		}
		addr := make([]byte, int(length[0]))
		if _, err := io.ReadFull(r, addr); err != nil {
			return reply, fmt.Errorf("fv: read SOCKS5 reply rep=%d domain: %w", reply.Rep, err)
		}
		stream.XORKeyStream(addr, addr)
		reply.BndAddr = string(addr)
	default:
		return reply, fmt.Errorf("fv: SOCKS5 reply rep=%d has unsupported ATYP %d", reply.Rep, reply.Atyp)
	}

	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return reply, fmt.Errorf("fv: read SOCKS5 reply rep=%d bound port: %w", reply.Rep, err)
	}
	stream.XORKeyStream(port[:], port[:])
	reply.BndPort = int(binary.BigEndian.Uint16(port[:]))
	return reply, nil
}
