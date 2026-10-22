#!/usr/bin/env python3
"""Minimal SOCKS5 client to trigger one AJiaSu data channel.

Sends greeting + CONNECT example.com:80, prints the reply, optionally sends an
HTTP request and prints whatever comes back.
"""
import argparse
import socket
import struct
import sys


def recvn(s, n):
    buf = b""
    while len(buf) < n:
        c = s.recv(n - len(buf))
        if not c:
            raise EOFError(f"eof after {len(buf)}/{n}")
        buf += c
    return buf


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=1080)
    ap.add_argument("--target", default="example.com")
    ap.add_argument("--tport", type=int, default=80)
    ap.add_argument("--payload", default="GET / HTTP/1.0\r\nHost: example.com\r\n\r\n")
    ap.add_argument("--timeout", type=float, default=8.0)
    args = ap.parse_args()

    s = socket.create_connection((args.host, args.port), timeout=args.timeout)
    s.settimeout(args.timeout)
    s.sendall(b"\x05\x01\x00")
    print("greeting reply:", recvn(s, 2).hex())
    host = args.target.encode()
    req = b"\x05\x01\x00\x03" + bytes([len(host)]) + host + struct.pack(">H", args.tport)
    s.sendall(req)
    rep = recvn(s, 10)
    print("connect reply:", rep.hex())
    if rep[1] != 0:
        print("connect failed rep=", rep[1])
        return 1
    s.sendall(args.payload.encode())
    try:
        data = s.recv(4096)
        print("payload reply:", data[:200])
    except socket.timeout:
        print("payload timeout")
    s.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
