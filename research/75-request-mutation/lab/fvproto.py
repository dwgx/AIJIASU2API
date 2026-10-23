#!/usr/bin/env python3
"""FV carrier codec and a lab-only mock FV server (AJS-15).

Wire format, from research/60-dynamic/REPORT.md and
emulator/internal/fv/binary.go:

    request  = [16B K][AES-128-CTR(K, IV=0)(4B BE text_len || text)][body]
    body     = AES-128-CTR(sessionKey, IV=n)(auth fields or SOCKS5 CONNECT)
    response = [AES-128-CTR(K, IV=0)(4B BE len || text)]
    auth map = AES-128-CTR(sessionKey, IV=n)(key=value\\n...), then half-close.

No vendor host is contacted. The mock server binds loopback or 10.75.0.0/24
only and refuses anything else.
"""

from __future__ import annotations

import base64
import ipaddress
import json
import re
import secrets
import socket
import socketserver
import struct
import threading
import time

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding, rsa
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

IV_SIZE = 16
ZERO_IV = b"\x00" * IV_SIZE
MAX_TEXT = 0xFFFF
REDACT_KEYS = {"k", "n", "_", "p", "u", "s", "sid"}
BODY_REDACT_KEYS = REDACT_KEYS | {"cuid", "cseid", "lid", "dim", "peip"}
LAB_NET = ipaddress.ip_network("10.75.0.0/24")


def aes_ctr(key: bytes, iv: bytes, data: bytes) -> bytes:
    if len(key) != 16 or len(iv) != 16:
        raise ValueError("AES-128-CTR needs a 16-byte key and IV")
    return Cipher(algorithms.AES(key), modes.CTR(iv)).encryptor().update(data)


def ctr_stream(key: bytes, iv: bytes):
    return Cipher(algorithms.AES(key), modes.CTR(iv)).encryptor()


def parse_pairs(text: str) -> list[tuple[str, str]]:
    pairs = []
    for line in text.split("\n"):
        if "=" not in line:
            continue
        k, v = line.split("=", 1)
        if k:
            pairs.append((k, v))
    return pairs


def parse_fields(text: str) -> dict[str, str]:
    out: dict[str, str] = {}
    for k, v in parse_pairs(text):
        out[k] = v
    return out


def build_text(fields: dict[str, str]) -> str:
    return "".join(f"{k}={v}\n" for k, v in fields.items())


def build_frame(key: bytes, text: str, body: bytes = b"") -> bytes:
    raw = text.encode("utf-8")
    if len(raw) > MAX_TEXT:
        raise ValueError(f"text is {len(raw)} bytes; the uint16 length allows {MAX_TEXT}")
    plain = struct.pack(">I", len(raw)) + raw
    return key + aes_ctr(key, ZERO_IV, plain) + body


def parse_frame(packet: bytes, private_key=None, session_key: bytes | None = None,
                show_secrets: bool = False) -> dict:
    """Parse one complete binary-carrier packet (request or response)."""
    if len(packet) < IV_SIZE + 4:
        raise ValueError(f"packet is {len(packet)} bytes, need at least 20")
    key = packet[:IV_SIZE]
    head = aes_ctr(key, ZERO_IV, packet[IV_SIZE:IV_SIZE + 4])
    text_len = struct.unpack(">I", head)[0]
    warnings = []
    if head[0] or head[1]:
        warnings.append(f"length prefix top bytes are {head[0]:#x} {head[1]:#x}, not 0 0")
    if text_len > len(packet) - IV_SIZE - 4:
        raise ValueError(f"declares {text_len} text bytes, only {len(packet) - 20} remain")
    outer = aes_ctr(key, ZERO_IV, packet[IV_SIZE:IV_SIZE + 4 + text_len])
    text = outer[4:].decode("utf-8", "replace")
    body = packet[IV_SIZE + 4 + text_len:]
    pairs = parse_pairs(text)
    fields = parse_fields(text)
    declared = None
    if "l" in fields:
        try:
            declared = int(fields["l"])
        except ValueError:
            warnings.append(f"l= is not an integer: {fields['l']!r}")
        else:
            if declared != len(body):
                warnings.append(f"l={declared} but {len(body)} body bytes follow")
            if 0 <= declared < len(body):
                body = body[:declared]
    iv = None
    if re.fullmatch(r"[0-9a-fA-F]{32}", fields.get("n", "")):
        iv = bytes.fromhex(fields["n"])
    else:
        warnings.append("n= is missing or not 32 hex characters")
    sk = session_key
    if sk is None and private_key is not None and fields.get("k"):
        sk = rsa_oaep_decrypt(private_key, fields["k"])
    body_plain = None
    if sk is not None and iv is not None and body:
        body_plain = aes_ctr(sk, iv, body)
    return {
        "key": key.hex() if show_secrets else f"<sha256:{_digest(key)}>",
        "key_hex": key.hex() if show_secrets else None,
        "text_len": text_len,
        "text": text if show_secrets else _redact_text(text),
        "pairs": pairs if show_secrets else [(k, _redact_field(k, v)) for k, v in pairs],
        "fields": fields if show_secrets else _redact_fields(fields),
        "body_len": len(body),
        "body_hex": body.hex() if show_secrets else None,
        "body_plain": body_plain.decode("utf-8", "replace") if (body_plain and show_secrets) else None,
        "session_key": sk.hex() if (sk and show_secrets) else None,
        "warnings": warnings,
    }


def _redact_field(key: str, value: str) -> str:
    return f"<redacted:{len(value)}>" if key in {"k", "n", "_", "p", "u"} else value


def _redact_fields(fields: dict[str, str]) -> dict[str, str]:
    return {k: _redact_field(k, v) for k, v in fields.items()}


def _digest(data: bytes) -> str:
    import hashlib
    return hashlib.sha256(data).hexdigest()[:12]


def _redact_text(text: str) -> str:
    out = []
    for k, v in parse_pairs(text):
        if k in {"k", "n", "_"}:
            out.append(f"{k}=<redacted:{len(v)}>")
        else:
            out.append(f"{k}={v}")
    return "\\n".join(out)


def rsa_oaep_encrypt(public_key, session_key: bytes) -> str:
    ct = public_key.encrypt(
        session_key,
        padding.OAEP(mgf=padding.MGF1(hashes.SHA1()), algorithm=hashes.SHA1(), label=None),
    )
    return base64.b64encode(b"OAEP" + ct).decode()


def rsa_oaep_decrypt(private_key, field: str) -> bytes:
    raw = base64.b64decode(field, validate=True)
    if raw[:4] != b"OAEP":
        raise ValueError("k= does not start with the OAEP magic")
    return private_key.decrypt(
        raw[4:],
        padding.OAEP(mgf=padding.MGF1(hashes.SHA1()), algorithm=hashes.SHA1(), label=None),
    )


def load_private_key(path: str):
    with open(path, "rb") as f:
        return serialization.load_pem_private_key(f.read(), password=None)


def generate_rsa(bits: int = 1024):
    return rsa.generate_private_key(public_exponent=65537, key_size=bits)


def socks5_request(host: str, port: int) -> bytes:
    try:
        ip = ipaddress.ip_address(host)
    except ValueError:
        raw = host.encode("idna")
        return b"\x05\x01\x00\x03" + bytes([len(raw)]) + raw + struct.pack(">H", port)
    if ip.version == 4:
        return b"\x05\x01\x00\x01" + ip.packed + struct.pack(">H", port)
    return b"\x05\x01\x00\x04" + ip.packed + struct.pack(">H", port)


def parse_socks5_request(plain: bytes) -> dict:
    if len(plain) < 5 or plain[0] != 5:
        raise ValueError(f"not a SOCKS5 request: {plain[:8].hex()}")
    atyp = plain[3]
    if atyp == 1 and len(plain) >= 10:
        host, off = str(ipaddress.IPv4Address(plain[4:8])), 8
    elif atyp == 4 and len(plain) >= 22:
        host, off = str(ipaddress.IPv6Address(plain[4:20])), 20
    elif atyp == 3 and len(plain) >= 5 + plain[4] + 2:
        ln = plain[4]
        host, off = plain[5:5 + ln].decode("idna"), 5 + ln
    else:
        raise ValueError(f"unsupported SOCKS5 ATYP {atyp} or short request")
    port = struct.unpack(">H", plain[off:off + 2])[0]
    return {"cmd": plain[1], "atyp": atyp, "host": host, "port": port}


def recv_exact(conn: socket.socket, n: int) -> bytes:
    buf = b""
    while len(buf) < n:
        chunk = conn.recv(n - len(buf))
        if not chunk:
            raise EOFError(f"peer closed after {len(buf)} of {n} bytes")
        buf += chunk
    return buf


def send_response_block(conn: socket.socket, key: bytes, text: str) -> None:
    raw = text.encode("utf-8")
    conn.sendall(aes_ctr(key, ZERO_IV, struct.pack(">I", len(raw)) + raw))


class _FVHandler(socketserver.BaseRequestHandler):
    def handle(self) -> None:
        cfg = self.server.cfg
        conn = self.request
        conn.settimeout(cfg.timeout)
        try:
            key = recv_exact(conn, IV_SIZE)
            head_ct = recv_exact(conn, 4)
            head = aes_ctr(key, ZERO_IV, head_ct)
            text_len = struct.unpack(">I", head)[0]
            if text_len > MAX_TEXT:
                raise ValueError(f"text_len {text_len} exceeds {MAX_TEXT}")
            text_ct = recv_exact(conn, text_len)
            text = aes_ctr(key, ZERO_IV, head_ct + text_ct)[4:].decode("utf-8", "replace")
            fields = parse_fields(text)
            if fields.get("k"):
                self._auth(conn, key, fields, text)
            else:
                self._data(conn, key, fields, text)
        except Exception as exc:  # noqa: BLE001 - lab log, never fatal
            cfg.log({"layer": "fv_error", "error": repr(exc)})

    def _auth(self, conn, key: bytes, fields: dict[str, str], text: str) -> None:
        cfg = self.server.cfg
        declared = int(fields.get("l", "0"))
        body_ct = recv_exact(conn, declared) if declared else b""
        session_key = rsa_oaep_decrypt(cfg.private_key, fields["k"])
        iv = bytes.fromhex(fields["n"])
        body = aes_ctr(session_key, iv, body_ct).decode("utf-8", "replace") if body_ct else ""
        sid = "lab-" + secrets.token_hex(4)
        cfg.sessions[sid] = session_key
        cfg.last_session_key = session_key
        cfg.log({
            "layer": "fv_auth", "sid": _redact_sid(sid), "fields": _redact_auth(fields),
            "body_fields": _redact_body(parse_fields(body)), "body_len": len(body_ct),
            "text_len": len(text),
        })
        send_response_block(conn, key, f"code={cfg.code}\nr={cfg.r}\ns={sid}\nsid={sid}\n")
        auth_map = (
            f"c=aur\nr={cfg.r}\nsid={sid}\ns={sid}\nok=1\n"
            "sip=10.75.0.2\ncip=10.75.0.3\ndns=10.75.0.1\n"
            f"ka={cfg.ka}\nkat=90\nkapf=1\nmtu=1400\nau=labuser\nst=ajs\n"
            "cv=20241105-1\nosp=linux\nosd=x86_64\nosv=lab\n"
        )
        conn.sendall(aes_ctr(session_key, iv, auth_map.encode()))
        if cfg.halfclose:
            try:
                conn.shutdown(socket.SHUT_WR)
            except OSError:
                pass

    def _data(self, conn, key: bytes, fields: dict[str, str], text: str) -> None:
        cfg = self.server.cfg
        sid = fields.get("s", "")
        session_key = cfg.sessions.get(sid)
        if session_key is None and cfg.permissive:
            session_key = cfg.last_session_key
            cfg.log({"layer": "fv_data", "sid": _redact_sid(sid), "event": "permissive-sid"})
        if session_key is None:
            cfg.log({"layer": "fv_data", "sid": _redact_sid(sid), "error": "unknown sid"})
            send_response_block(conn, key, "code=403\nr=auth.session\n")
            return
        iv = bytes.fromhex(fields["n"])
        dec = Cipher(algorithms.AES(session_key), modes.CTR(iv)).decryptor()
        first = dec.update(recv_exact(conn, 4))
        if len(first) < 4 or first[0] != 5:
            raise ValueError(f"data body is not SOCKS5: {first.hex()}")
        atyp = first[3]
        if atyp == 1:
            rest = dec.update(recv_exact(conn, 6))
        elif atyp == 4:
            rest = dec.update(recv_exact(conn, 18))
        elif atyp == 3:
            ln = dec.update(recv_exact(conn, 1))
            rest = ln + dec.update(recv_exact(conn, ln[0] + 2))
        else:
            raise ValueError(f"unsupported ATYP {atyp}")
        req = parse_socks5_request(first + rest)
        cfg.log({"layer": "fv_data", "sid": _redact_sid(sid), "socks": req,
                 "fields": _redact_auth(fields), "text_len": len(text)})
        send_response_block(conn, key, f"code={cfg.code}\nr={cfg.r}\n")
        conn.sendall(aes_ctr(session_key, iv, b"\x05\x00\x00\x01" + b"\x00" * 6))


def _redact_auth(fields: dict[str, str]) -> dict[str, str]:
    return {k: (f"<redacted:{len(v)}>" if k in REDACT_KEYS else v) for k, v in fields.items()}


def _redact_body(fields: dict[str, str]) -> dict[str, str]:
    return {k: (f"<redacted:{len(v)}>" if k in BODY_REDACT_KEYS else v) for k, v in fields.items()}


def _redact_sid(sid: str) -> str:
    return f"<sid:{len(sid)}>" if sid else sid


class FVServer(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True

    def __init__(self, addr, private_key, log_path: str | None = None, timeout: float = 5.0,
                 permissive: bool = False, code: str = "200", r: str = "ok",
                 halfclose: bool = True, ka: str = "30"):
        self.cfg = type("Cfg", (), {})()
        self.cfg.private_key = private_key
        self.cfg.sessions = {}
        self.cfg.last_session_key = None
        self.cfg.timeout = timeout
        self.cfg.permissive = permissive
        self.cfg.code = code
        self.cfg.r = r
        self.cfg.halfclose = halfclose
        self.cfg.ka = ka
        self._lock = threading.Lock()
        self._log_path = log_path
        self.cfg.log = self._log
        super().__init__(addr, _FVHandler)

    def _log(self, record: dict) -> None:
        record = {"ts": time.time(), **record}
        with self._lock:
            if self._log_path:
                with open(self._log_path, "a", encoding="utf-8") as f:
                    f.write(json.dumps(record, ensure_ascii=False) + "\n")
            else:
                print(json.dumps(record, ensure_ascii=False), flush=True)

    def start(self) -> "FVServer":
        threading.Thread(target=self.serve_forever, daemon=True).start()
        return self
