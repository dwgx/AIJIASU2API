#!/usr/bin/env python3
"""Mock FV endpoint for AJiaSu Linux 4.2.3.0 (protocolVersion=2).

Wire format observed from the client (see REPORT.md):
  request  = [16B key K][ AES-128-CTR(K, IV=0) over 4B BE text_len || text || body ]
  text     = s=<sid>\\nn=<hex nonce>\\nk=<b64 OAEP>\\nl=<body len>\\n_=<padding>
  response = [4B CTR(K,0)(00 00 LL LL)][4B CTR(K,0)(00 00 LL LL)][CTR(K,4..)(text)]

The mock decrypts the outer layer, logs the text, optionally decrypts the body
when a private key is supplied, and answers with a code=200 text block.
"""
import argparse
import base64
import os
import socket
import ssl
import struct
import threading
import time
import zlib

from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

LAB = os.environ.get("AJS_LAB", "/tmp/ajs-lab")
CAP = os.path.join(LAB, "captures")
ZERO = bytes(16)
SESSION_KEY = None


def ctr(key, data, iv=ZERO, start=0):
    dec = Cipher(algorithms.AES(key), modes.CTR(iv)).decryptor()
    if start:
        dec.update(bytes(start))
    return dec.update(data)


def read_exact(conn, n, timeout=10.0):
    conn.settimeout(timeout)
    buf = b""
    while len(buf) < n:
        chunk = conn.recv(n - len(buf))
        if not chunk:
            raise EOFError(f"eof after {len(buf)}/{n}")
        buf += chunk
    return buf


def parse_text(text):
    fields = {}
    for line in text.decode("utf-8", "replace").split("\n"):
        if "=" in line:
            k, v = line.split("=", 1)
            fields[k] = v
    return fields


def rsa_decrypt(k_b64, key_file):
    raw = base64.b64decode(k_b64)
    if not raw.startswith(b"OAEP"):
        return None, raw[:8]
    ct = raw[4:]
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import padding

    with open(key_file, "rb") as f:
        priv = serialization.load_pem_private_key(f.read(), None)
    for algo in (hashes.SHA1(), hashes.SHA256()):
        try:
            return priv.decrypt(
                ct,
                padding.OAEP(mgf=padding.MGF1(algo), algorithm=algo, label=None),
            ), algo.name
        except Exception:
            pass
    return None, None


def handle(idx, conn, addr, args):
    t0 = time.time()
    raw_all = b""
    log = []
    try:
        key = read_exact(conn, 16)
        raw_all += key
        enc_len = read_exact(conn, 4)
        raw_all += enc_len
        head = ctr(key, enc_len)
        if head[0] != 0 or head[1] != 0:
            log.append(f"bad length prefix {head.hex()}")
            return
        text_len = struct.unpack(">H", head[2:4])[0]
        text_ct = read_exact(conn, text_len)
        raw_all += text_ct
        text = ctr(key, text_ct, ZERO, 4)
        fields = parse_text(text)
        if fields.get("s"):
            # Data channel: s=<sid>, n=<div hex>, k= empty, no l=. The body
            # (encrypted SOCKS5 request) follows the header text immediately.
            conn.settimeout(0.5)
            body = b""
            while True:
                try:
                    chunk = conn.recv(65536)
                except socket.timeout:
                    break
                if not chunk:
                    break
                body += chunk
            raw_all += body
            iv = bytes.fromhex(fields["n"])
            pt = ctr(SESSION_KEY, body, iv) if SESSION_KEY else b""
            log.append(f"DATA s={fields['s']} n={fields['n']} body={len(body)}")
            log.append("SOCKS=" + pt.hex() + " " + repr(pt))
            resp_text = b"code=200\n"
            resp = ctr(key, struct.pack(">HH", 0, len(resp_text)) + resp_text)
            conn.sendall(resp)
            reply = b"\x05\x00\x00\x01\x00\x00\x00\x00\x00\x00"
            if SESSION_KEY:
                conn.sendall(ctr(SESSION_KEY, reply, iv))
            log.append("sent data code=200 + socks reply")
            conn.settimeout(args.hold)
            relay = b""
            while True:
                try:
                    chunk = conn.recv(65536)
                except socket.timeout:
                    break
                if not chunk:
                    break
                relay += chunk
            raw_all += relay
            if relay:
                log.append(f"RELAY_CT={len(relay)}")
                if SESSION_KEY:
                    rpt = ctr(SESSION_KEY, relay, iv, start=len(pt))
                    log.append("RELAY=" + repr(rpt[:300]))
                    dummy = b"HTTP/1.0 200 OK\r\nContent-Length: 2\r\n\r\nOK"
                    conn.sendall(ctr(SESSION_KEY, dummy, iv, start=10))
                    log.append("sent dummy http response")
            return
        body_len = int(fields.get("l", "0") or 0)
        body = read_exact(conn, body_len) if body_len else b""
        raw_all += body
        log.append(f"text_len={text_len} body_len={body_len}")
        log.append("TEXT=" + text.decode("latin1"))
        # reply: code=200 + session fields. The response AES key is the
        # context's second half (ctx+16), which the client keeps secret; test
        # whether it equals the RSA-wrapped session key.
        resp_key = key
        session = None
        if args.key and fields.get("k"):
            session, algo = rsa_decrypt(fields["k"], args.key)
            if session:
                global SESSION_KEY
                SESSION_KEY = session
                log.append(f"session_key={session.hex()} oaep={algo}")
                if body and fields.get("n"):
                    iv = bytes.fromhex(fields["n"])
                    pt = ctr(session, body, iv)
                    log.append("BODY=" + pt.decode("latin1", "replace"))
                if args.resp_key == "s":
                    resp_key = session
            else:
                log.append("rsa_decrypt failed")
        resp_text = b"code=200\nr=ok\ns=test\nsid=test\n"
        # One CTR stream from position 0: [4B length][text]. The client peeks
        # the first 4 bytes for the length, then re-reads len+4 bytes from the
        # start of the same buffer and decrypts them in one pass.
        resp = ctr(resp_key, struct.pack(">HH", 0, len(resp_text)) + resp_text)
        conn.sendall(resp)
        log.append(f"sent response {len(resp)} bytes key={resp_key.hex()}")
        log.append("RESP_HEX=" + resp.hex())
        if session and args.auth_frame:
            # Auth result: AES-CTR(sessionKey, IV=n) over k=v&... fields,
            # appended after the code=200 block on the same stream.
            fields_txt = (
                "c=aur\nr=ok\nsid=test\ns=test\nok=1\nsip=10.0.0.1\n"
                "cip=10.0.0.2\ndns=1.1.1.1\nka=30\nkat=90\nkapf=1\n"
                "mtu=1400\nau=test\nst=ajs\ncv=20241105-1\nosp=linux\n"
                "osd=x86_64\nosv=Linux\ndim=test\npeip=127.0.0.1\nce=\n"
            ).encode()
            iv = bytes.fromhex(fields["n"])
            # The FV control packet is a TLV stream: tag 0x01 + map text.
            auth = ctr(session, fields_txt, iv)
            conn.sendall(auth)
            log.append(f"sent auth fields {len(auth)} bytes iv={iv.hex()} body={fields_txt.decode()}")
            log.append("AUTH_HEX=" + auth.hex())
            if args.close_after_auth:
                conn.shutdown(socket.SHUT_WR)
                log.append("shutdown write after auth")
        # keep reading whatever the client sends next
        conn.settimeout(args.hold)
        extra = b""
        while True:
            try:
                chunk = conn.recv(65536)
            except socket.timeout:
                break
            if not chunk:
                break
            extra += chunk
        raw_all += extra
        log.append(f"post bytes={len(extra)}")
        if extra:
            log.append("POST_HEX=" + extra.hex())
            # try OPV2 parse if magic appears after keyA^keyB/keyB/crc
            if len(extra) >= 40:
                ka = bytes(x ^ y for x, y in zip(extra[0:16], extra[16:32]))
                dec = ctr(ka, extra[36:])
                log.append("OPV2_TRY=" + repr(dec[:120]))
    except Exception as e:
        log.append(f"error: {type(e).__name__}: {e}")
    finally:
        path = os.path.join(CAP, f"fvmock-{idx}")
        with open(path + ".bin", "wb") as f:
            f.write(raw_all)
        with open(path + ".log", "w", encoding="utf-8") as f:
            f.write(f"peer={addr} total={len(raw_all)} elapsed={time.time()-t0:.2f}\n")
            f.write("\n".join(log) + "\n")
        print(f"fv conn {idx}: {len(raw_all)} bytes -> {path}.log", flush=True)
        conn.close()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=8443)
    ap.add_argument("--key", default=None, help="PEM private key for k= decryption")
    ap.add_argument("--hold", type=float, default=8.0)
    ap.add_argument("--auth-frame", action="store_true", help="send the auth field map after code=200")
    ap.add_argument("--close-after-auth", action="store_true", help="half-close after the auth map")
    ap.add_argument("--resp-key", choices=["p", "s"], default="p",
                    help="response AES key: p=wire prefix, s=RSA session key")
    args = ap.parse_args()
    os.makedirs(CAP, exist_ok=True)
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("0.0.0.0", args.port))
    srv.listen(16)
    print(f"mock fv v2 listening on {args.port}", flush=True)
    idx = 0
    while True:
        conn, addr = srv.accept()
        idx += 1
        threading.Thread(target=handle, args=(idx, conn, addr, args), daemon=True).start()


if __name__ == "__main__":
    main()
