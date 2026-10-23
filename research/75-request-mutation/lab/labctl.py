#!/usr/bin/env python3
"""AJS-15 lab CLI: mock control plane, tap, replay, redaction, diff, mutations.

Lab-only. Network subcommands require AJS_LAB=1 and
--i-understand-lab-only, and every peer must resolve into 127.0.0.0/8 or
10.75.0.0/24. Run `python lab/labctl.py selftest` for the offline checks.
"""

from __future__ import annotations

import argparse
import datetime
import gzip
import hashlib
import hmac
import http.server
import ipaddress
import json
import os
import pathlib
import secrets
import selectors
import socket
import socketserver
import ssl
import struct
import sys
import threading
import time
import urllib.parse
import urllib.request
import zlib

import fvproto

LAB_NET = ipaddress.ip_network("10.75.0.0/24")
SECRET_KEYS = {
    "password", "pass", "p", "connectpassword", "logintoken", "_client_login",
    "token", "authorization", "cookie", "set-cookie", "secret", "ps",
}
IDENTITY_KEYS = {
    "_uid", "_os_did", "cuid", "cseid", "lid", "dim", "sid", "n", "k",
    "session_key", "key", "iv", "mac", "imei", "android_id", "username", "u",
}
VOLATILE_KEYS = {"t", "ts", "timestamp", "_", "nonce", "request_id", "trace_id", "date"}
DIFF_VOLATILE = VOLATILE_KEYS | IDENTITY_KEYS


class LabRefused(Exception):
    pass


def fail(msg: str) -> None:
    raise LabRefused(msg)


def require_optin(args) -> None:
    if os.environ.get("AJS_LAB") != "1" or not getattr(args, "i_understand_lab_only", False):
        fail("set AJS_LAB=1 and pass --i-understand-lab-only")


def assert_lab_host(host: str, port: int | None = None) -> None:
    try:
        infos = socket.getaddrinfo(host, port or 0, type=socket.SOCK_STREAM)
    except socket.gaierror as exc:
        fail(f"cannot resolve {host!r}: {exc}")
    for info in infos:
        ip = ipaddress.ip_address(info[4][0])
        if not (ip.is_loopback or ip in LAB_NET):
            fail(f"{host} resolves to {ip}, outside loopback/10.75.0.0/24")


def split_hostport(value: str) -> tuple[str, int]:
    host, _, port = value.rpartition(":")
    if not host or not port.isdigit():
        fail(f"want host:port, got {value!r}")
    return host, int(port)


def redact_value(key: str, value, salt: bytes):
    lk = key.lower()
    if lk in SECRET_KEYS:
        return "<redacted>"
    if lk in IDENTITY_KEYS:
        raw = value if isinstance(value, str) else json.dumps(value, sort_keys=True)
        return "<hmac:%s>" % hmac.new(salt, raw.encode(), hashlib.sha256).hexdigest()[:12]
    if lk in VOLATILE_KEYS:
        return "<volatile>"
    return value


def redact_obj(obj, salt: bytes):
    if isinstance(obj, dict):
        out = {}
        for k, v in obj.items():
            if isinstance(v, dict):
                out[k] = redact_obj(v, salt)
            elif isinstance(v, list):
                out[k] = [redact_obj(x, salt) if isinstance(x, (dict, list))
                          else redact_value(k, x, salt) for x in v]
            else:
                out[k] = redact_value(k, v, salt)
        return out
    if isinstance(obj, list):
        return [redact_obj(v, salt) for v in obj]
    return obj


class Capture:
    def __init__(self, outdir: str, salt: bytes):
        self.dir = pathlib.Path(outdir)
        self.dir.mkdir(parents=True, exist_ok=True)
        self.salt = salt
        self.lock = threading.Lock()
        self.jsonl = self.dir / "capture.jsonl"
        self.sealed = self.dir / "sealed.jsonl"

    def write(self, record: dict, sealed: dict | None = None) -> None:
        with self.lock:
            with open(self.jsonl, "a", encoding="utf-8") as f:
                f.write(json.dumps(redact_obj(record, self.salt), ensure_ascii=False) + "\n")
            if sealed is not None:
                fd = os.open(self.sealed, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
                with os.fdopen(fd, "a", encoding="utf-8") as f:
                    f.write(json.dumps(sealed, ensure_ascii=False) + "\n")


DEFAULT_FIXTURE = {
    "SyncVersion": {},
    "Login": {
        "AuthResult": "OK", "AuthMessage": "", "GroupTitle": "lab",
        "ExpireTime": 0, "Expired": False, "UrlWeb": "https://lab.invalid/",
        "LatestVersion": "4.2.3.0", "LoginToken": "lab-token",
        "UserName": "labuser", "ConnectPassword": "lab-connect",
    },
    "Servers": {
        "lab-node": {
            "id": "lab-node", "vsid": 1, "ctry": "CN", "svr": "10.75.0.1",
            "tit": "Lab Node", "cmt": "", "sts": "ok", "ord": "1",
            "cat": ["lab"],
            "par": {"fv": {"u": 1001, "p": 8444, "ps": "lab"}},
            "loc": [{"c": "CN", "n": "Lab", "o": 1}],
        }
    },
}


class ControlHandler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _handle(self) -> None:
        cfg = self.server.cfg
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n) if n else b""
        body = raw.decode("utf-8", "replace")
        pairs = urllib.parse.parse_qsl(body, keep_blank_values=True)
        fields: dict[str, list[str]] = {}
        for k, v in pairs:
            fields.setdefault(k, []).append(v)
        tls = {}
        try:
            tls = {"version": self.connection.version(),
                   "cipher": (self.connection.cipher() or [None])[0]}
        except (AttributeError, OSError):
            pass
        cfg.capture.write({
            "layer": "control_request", "host": self.headers.get("Host"),
            "method": self.command, "path": self.path, "tls": tls,
            "headers": {k: v for k, v in self.headers.items()
                        if k.lower() not in {"authorization", "cookie"}},
            "body_len": len(raw), "fields": fields,
        }, sealed={"raw": body})
        payload = json.dumps(cfg.fixture, separators=(",", ":")).encode()
        encoding = None
        if cfg.encoding == "gzip":
            payload, encoding = gzip.compress(payload), "gzip"
        elif cfg.encoding == "deflate":
            payload, encoding = zlib.compress(payload), "deflate"
        self.send_response(cfg.status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        if encoding:
            self.send_header("Content-Encoding", encoding)
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(payload)
        self.close_connection = True

    do_POST = _handle
    do_GET = _handle

    def log_message(self, fmt, *args):
        pass


def make_control_server(bind: str, port: int, cert: str, key: str, fixture: dict,
                        capture: Capture, status: int = 200, encoding: str = "none"):
    httpd = http.server.ThreadingHTTPServer((bind, port), ControlHandler)
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(cert, key)
    ctx.minimum_version = ssl.TLSVersion.TLSv1_2
    httpd.socket = ctx.wrap_socket(httpd.socket, server_side=True)
    httpd.cfg = type("Cfg", (), {})()
    httpd.cfg.fixture, httpd.cfg.capture = fixture, capture
    httpd.cfg.status, httpd.cfg.encoding = status, encoding
    return httpd


def generate_ca(dir, hosts, ips, days) -> dict:
    from cryptography import x509
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import rsa
    from cryptography.x509.oid import ExtendedKeyUsageOID, NameOID

    out = pathlib.Path(dir)
    out.mkdir(parents=True, exist_ok=True)
    now = datetime.datetime.now(datetime.timezone.utc)
    ca_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "AJS Lab CA")])
    ca = (x509.CertificateBuilder().subject_name(name).issuer_name(name)
          .public_key(ca_key.public_key()).serial_number(x509.random_serial_number())
          .not_valid_before(now - datetime.timedelta(minutes=5))
          .not_valid_after(now + datetime.timedelta(days=days))
          .add_extension(x509.BasicConstraints(ca=True, path_length=0), critical=True)
          .add_extension(x509.SubjectKeyIdentifier.from_public_key(ca_key.public_key()), critical=False)
          .add_extension(x509.KeyUsage(digital_signature=True, content_commitment=False,
                                       key_encipherment=False, data_encipherment=False,
                                       key_agreement=False, key_cert_sign=True,
                                       crl_sign=True, encipher_only=False,
                                       decipher_only=False), critical=True)
          .sign(ca_key, hashes.SHA256()))
    leaf_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    san = [x509.DNSName(h) for h in hosts] + [x509.IPAddress(ipaddress.ip_address(i)) for i in ips]
    leaf = (x509.CertificateBuilder().subject_name(x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, hosts[0] if hosts else "ajs-lab")]))
            .issuer_name(ca.subject).public_key(leaf_key.public_key())
            .serial_number(x509.random_serial_number())
            .not_valid_before(now - datetime.timedelta(minutes=5))
            .not_valid_after(now + datetime.timedelta(days=days))
            .add_extension(x509.SubjectAlternativeName(san), critical=False)
            .add_extension(x509.SubjectKeyIdentifier.from_public_key(leaf_key.public_key()), critical=False)
            .add_extension(x509.AuthorityKeyIdentifier.from_issuer_public_key(ca_key.public_key()), critical=False)
            .add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.SERVER_AUTH]), critical=False)
            .sign(ca_key, hashes.SHA256()))
    def write(path, data, mode=0o644):
        p = out / path
        with open(p, "wb") as f:
            f.write(data)
        os.chmod(p, mode)
    write("ca.pem", ca.public_bytes(serialization.Encoding.PEM))
    write("ca.key", ca_key.private_bytes(serialization.Encoding.PEM,
          serialization.PrivateFormat.PKCS8, serialization.NoEncryption()), 0o600)
    write("server.pem", leaf.public_bytes(serialization.Encoding.PEM))
    write("server.key", leaf_key.private_bytes(serialization.Encoding.PEM,
          serialization.PrivateFormat.PKCS8, serialization.NoEncryption()), 0o600)
    return {"ok": True, "dir": str(out), "sans": [str(s.value) for s in san]}


def cmd_ca(args) -> None:
    print(json.dumps(generate_ca(args.dir, args.host, args.ip, args.days)))


def cmd_mock_control(args) -> None:
    require_optin(args)
    assert_lab_host(args.bind, args.port[0])
    fixture = json.loads(pathlib.Path(args.fixture).read_text(encoding="utf-8")) if args.fixture else DEFAULT_FIXTURE
    salt = pathlib.Path(args.salt_file).read_bytes() if args.salt_file else secrets.token_bytes(32)
    capture = Capture(args.capture, salt)
    servers = [make_control_server(args.bind, p, args.cert, args.key, fixture, capture,
                                   args.status, args.encoding) for p in args.port]
    for s in servers:
        threading.Thread(target=s.serve_forever, daemon=True).start()
    print(json.dumps({"ok": True, "bind": args.bind, "ports": args.port,
                      "capture": args.capture, "fixture": args.fixture or "builtin"}))
    try:
        while True:
            time.sleep(3600)
    except KeyboardInterrupt:
        for s in servers:
            s.shutdown()


def cmd_mock_fv(args) -> None:
    require_optin(args)
    assert_lab_host(args.bind, args.port)
    key = fvproto.load_private_key(args.rsa_key)
    server = fvproto.FVServer((args.bind, args.port), key, args.capture_log,
                              permissive=args.permissive, code=args.code, r=args.r,
                              halfclose=not args.no_halfclose, ka=args.ka).start()
    print(json.dumps({"ok": True, "bind": args.bind, "port": server.server_address[1]}))
    try:
        while True:
            time.sleep(3600)
    except KeyboardInterrupt:
        server.shutdown()


class TapHandler(socketserver.BaseRequestHandler):
    def handle(self) -> None:
        cfg = self.server.cfg
        up = socket.create_connection((cfg.up_host, cfg.up_port), timeout=5)
        cid = next(cfg.counter)
        c2s = cfg.out / f"conn-{cid:04d}.c2s.bin"
        s2c = cfg.out / f"conn-{cid:04d}.s2c.bin"
        counters = {"c2s": 0, "s2c": 0}
        lock = threading.Lock()
        with open(c2s, "wb") as f1, open(s2c, "wb") as f2:
            sel = selectors.DefaultSelector()
            sel.register(self.request, selectors.EVENT_READ, ("c2s", f1))
            sel.register(up, selectors.EVENT_READ, ("s2c", f2))
            while True:
                events = sel.select(timeout=cfg.idle)
                if not events:
                    break
                for key, _ in events:
                    direction, fh = key.data
                    try:
                        data = key.fileobj.recv(65536)
                    except OSError:
                        data = b""
                    if not data:
                        sel.close()
                        cfg.record({"conn": cid, "event": "close", "bytes": counters})
                        return
                    with lock:
                        fh.write(data)
                        fh.flush()
                        counters[direction] += len(data)
                    (up if direction == "c2s" else self.request).sendall(data)
        sel.close()
        cfg.record({"conn": cid, "event": "idle_close", "bytes": counters})


class TapServer(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


def cmd_tap(args) -> None:
    require_optin(args)
    lhost, lport = split_hostport(args.listen)
    uhost, uport = split_hostport(args.upstream)
    assert_lab_host(lhost, lport)
    assert_lab_host(uhost, uport)
    out = pathlib.Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    counter = iter(range(1, 100000))
    server = TapServer((lhost, lport), TapHandler)
    server.cfg = type("Cfg", (), {})()
    server.cfg.up_host, server.cfg.up_port = uhost, uport
    server.cfg.out, server.cfg.counter = out, counter
    server.cfg.idle = args.idle
    log = out / "tap.jsonl"
    lock = threading.Lock()
    def record(rec):
        with lock, open(log, "a", encoding="utf-8") as f:
            f.write(json.dumps({"ts": time.time(), **rec}) + "\n")
    server.cfg.record = record
    threading.Thread(target=server.serve_forever, daemon=True).start()
    print(json.dumps({"ok": True, "listen": args.listen, "upstream": args.upstream, "out": str(out)}))
    try:
        while True:
            time.sleep(3600)
    except KeyboardInterrupt:
        server.shutdown()


def cmd_replay(args) -> None:
    require_optin(args)
    host, port = split_hostport(args.target)
    assert_lab_host(host, port)
    cap = pathlib.Path(args.capture)
    files = sorted(cap.glob("*.c2s.bin")) if cap.is_dir() else [cap]
    if not files:
        fail(f"no *.c2s.bin under {cap}")
    out = pathlib.Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    results = []
    for f in files:
        data = f.read_bytes()
        s = socket.create_connection((host, port), timeout=args.timeout)
        s.settimeout(args.timeout)
        s.sendall(data)
        s.shutdown(socket.SHUT_WR)
        chunks = []
        while True:
            try:
                chunk = s.recv(65536)
            except socket.timeout:
                break
            if not chunk:
                break
            chunks.append(chunk)
        s.close()
        reply = b"".join(chunks)
        dest = out / f.name.replace(".c2s.bin", ".s2c.bin")
        dest.write_bytes(reply)
        results.append({"sent": f.name, "sent_bytes": len(data),
                        "reply": dest.name, "reply_bytes": len(reply)})
    print(json.dumps({"ok": True, "target": args.target, "results": results}))


def cmd_redact(args) -> None:
    salt = pathlib.Path(args.salt_file).read_bytes() if args.salt_file else secrets.token_bytes(32)
    src, dst = pathlib.Path(args.input), pathlib.Path(args.output)
    dst.parent.mkdir(parents=True, exist_ok=True)
    n = 0
    with open(src, encoding="utf-8") as f, open(dst, "w", encoding="utf-8") as g:
        for line in f:
            if line.strip():
                g.write(json.dumps(redact_obj(json.loads(line), salt), ensure_ascii=False) + "\n")
                n += 1
    print(json.dumps({"ok": True, "lines": n, "out": str(dst)}))


def normalize(obj, volatile: set[str]):
    if isinstance(obj, dict):
        return {k: normalize(v, volatile) for k, v in sorted(obj.items()) if k not in volatile}
    if isinstance(obj, list):
        return [normalize(v, volatile) for v in obj]
    return obj


def diff(a, b, path: str = "") -> list[dict]:
    out = []
    if type(a) is not type(b):
        return [{"path": path, "op": "type", "a": type(a).__name__, "b": type(b).__name__}]
    if isinstance(a, dict):
        for k in sorted(set(a) | set(b)):
            p = f"{path}.{k}" if path else k
            if k not in a:
                out.append({"path": p, "op": "add", "b": b[k]})
            elif k not in b:
                out.append({"path": p, "op": "remove", "a": a[k]})
            else:
                out.extend(diff(a[k], b[k], p))
    elif isinstance(a, list):
        if a != b:
            out.append({"path": path, "op": "list", "a": a, "b": b})
    elif a != b:
        out.append({"path": path, "op": "change", "a": a, "b": b})
    return out


def cmd_diff(args) -> None:
    volatile = set(args.volatile.split(",")) if args.volatile else set(DIFF_VOLATILE)
    a = normalize(json.loads(pathlib.Path(args.a).read_text(encoding="utf-8")), volatile)
    b = normalize(json.loads(pathlib.Path(args.b).read_text(encoding="utf-8")), volatile)
    changes = diff(a, b)
    print(json.dumps({"changes": changes, "count": len(changes)}, ensure_ascii=False))
    raise SystemExit(0 if not changes else 1)


def split_ops(spec: str) -> tuple[str, dict]:
    """Split a matrix mutation cell into field ops and body-level flags.

    Two rows mutate the request body rather than a field:
    C20 appends the trailing '&' the Linux client sends; F15 truncates the
    FV body. Keeping them explicit means a typo still fails instead of being
    silently dropped.
    """
    field_ops: list[str] = []
    body: dict = {}
    for op in spec.split(";"):
        op = op.strip()
        if not op:
            continue
        if op == "append trailing &":
            body["trailing_amp"] = True
        elif op.startswith("truncate:body"):
            body["truncate_body"] = True
        else:
            field_ops.append(op)
    return ";".join(field_ops), body


def apply_ops_pairs(pairs, spec: str) -> list[list[str]]:
    """Apply a mutation spec to an ordered [key, value] list.

    Unlike a dict this keeps duplicate keys, which the `dup` operator needs:
    C16 tests urlencoded duplicate-key precedence and must reach the wire twice.
    """
    out = [[k, v] for k, v in pairs]

    def put(key: str, value: str) -> None:
        for pair in out:
            if pair[0] == key:
                pair[1] = value
                return
        out.append([key, value])

    for op in spec.split(";"):
        op = op.strip()
        if not op:
            continue
        name, _, arg = op.partition(":")
        if name == "delete":
            out = [pair for pair in out if pair[0] != arg]
        elif name == "empty":
            put(arg, "")
        elif name == "set":
            k, _, v = arg.partition("=")
            put(k, v)
        elif name == "len":
            k, _, n = arg.partition("=")
            put(k, "A" * int(n))
        elif name == "upper":
            put(arg, next((p[1] for p in out if p[0] == arg), "").upper())
        elif name == "nul":
            put(arg, next((p[1] for p in out if p[0] == arg), "") + "\x00")
        elif name == "dup":
            for i, pair in enumerate(out):
                if pair[0] == arg:
                    out.insert(i + 1, [arg, pair[1]])
                    break
            else:
                out.append([arg, ""])
        elif name == "swap":
            x, _, y = arg.partition("=")
            vx = next((p[1] for p in out if p[0] == x), "")
            vy = next((p[1] for p in out if p[0] == y), "")
            put(x, vy)
            put(y, vx)
        else:
            raise ValueError(f"unknown mutation operator {name!r}")
    return out


def apply_ops(fields: dict, spec: str) -> dict:
    return dict(apply_ops_pairs(list(fields.items()), spec))


def cmd_mutate(args) -> None:
    rows = [line.rstrip("\n").split("\t") for line in pathlib.Path(args.matrix).read_text(encoding="utf-8").splitlines()]
    header, rows = rows[0], rows[1:]
    if args.id:
        rows = [r for r in rows if r[0] == args.id]
        if not rows:
            fail(f"no matrix row {args.id!r}")
    if args.layer:
        rows = [r for r in rows if len(r) > 1 and r[1] == args.layer]
        if not rows:
            fail(f"no matrix rows for layer {args.layer!r}")
    req = json.loads(pathlib.Path(args.request).read_text(encoding="utf-8"))
    if args.layer and req.get("layer") not in (None, args.layer):
        fail(f"request fixture layer {req.get('layer')!r} does not match --layer {args.layer!r}")
    out = pathlib.Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    written = []
    for row in rows:
        if len(row) != len(header):
            fail(f"matrix row has {len(row)} columns, want {len(header)}: {row[0]}")
        rec = dict(zip(header, row))
        field_spec, body_meta = split_ops(rec["mutations"])
        pairs = apply_ops_pairs(list(req.get("fields", {}).items()), field_spec)
        doc = {"id": rec["id"], "layer": rec["layer"],
               "fields": dict(pairs), "pairs": pairs,
               "expected_client": rec["expected_client"],
               "expected_server": rec["expected_server"],
               **body_meta}
        (out / f"{rec['id']}.json").write_text(json.dumps(doc, indent=2), encoding="utf-8")
        written.append(rec["id"])
    print(json.dumps({"ok": True, "cases": written, "out": str(out)}))


def cmd_keygen(args) -> None:
    out = pathlib.Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    from cryptography.hazmat.primitives import serialization
    for _ in range(128):
        key = fvproto.generate_rsa(args.bits)
        modulus = str(key.public_key().public_numbers().n)
        if len(modulus) == args.digits:
            break
    else:
        fail(f"could not generate a {args.bits}-bit modulus with {args.digits} digits")
    key_path = out / "fv.key"
    key_path.write_bytes(key.private_bytes(
        serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8,
        serialization.NoEncryption()))
    os.chmod(key_path, 0o600)
    (out / "fv.modulus").write_text(modulus + "\n", encoding="ascii")
    print(json.dumps({"ok": True, "bits": args.bits, "digits": len(modulus),
                      "key": str(key_path), "modulus": str(out / "fv.modulus")}))


VENDOR_MODULUS = (
    "141601187905926909444644730286409666755495300050721564398199769080746616339148556683219048017120018748016428099359153205651362664001287890722535799466700786136519566714160932079339659299698611914846217596226121056961277268141789905486380539961102053398435632146710731361922836526658451793921073088741138357721"
)


def cmd_patch_modulus(args) -> None:
    data = bytearray(pathlib.Path(args.binary).read_bytes())
    lab = pathlib.Path(args.modulus_file).read_text(encoding="ascii").strip()
    if len(lab) != len(VENDOR_MODULUS):
        fail(f"lab modulus has {len(lab)} digits, vendor modulus has {len(VENDOR_MODULUS)}")
    needle = VENDOR_MODULUS.encode("ascii")
    hits = data.count(needle)
    if args.offset is not None:
        off = args.offset
        if data[off:off + len(needle)] != needle:
            fail(f"vendor modulus not at offset {off:#x}")
    else:
        off = data.find(needle)
        if off < 0:
            fail("vendor modulus not found; pass --offset")
    data[off:off + len(needle)] = lab.encode("ascii")
    out = pathlib.Path(args.out)
    out.write_bytes(bytes(data))
    os.chmod(out, 0o755)
    import hashlib
    print(json.dumps({
        "ok": True, "offset": hex(off), "hits": hits, "out": str(out),
        "sha256_before": hashlib.sha256(pathlib.Path(args.binary).read_bytes()).hexdigest(),
        "sha256_after": hashlib.sha256(bytes(data)).hexdigest(),
    }))


def cmd_frame(args) -> None:
    if args.frame_cmd == "parse":
        priv = fvproto.load_private_key(args.rsa_key) if args.rsa_key else None
        sk = bytes.fromhex(args.session_key_hex) if args.session_key_hex else None
        doc = fvproto.parse_frame(pathlib.Path(args.file).read_bytes(), priv, sk, args.show_secrets)
        print(json.dumps(doc, indent=2))
        return
    packet = pathlib.Path(args.file).read_bytes()
    priv = fvproto.load_private_key(args.rsa_key) if args.rsa_key else None
    sk = bytes.fromhex(args.session_key_hex) if args.session_key_hex else None
    doc = fvproto.parse_frame(packet, priv, sk, True)
    fields = apply_ops(doc["fields"], args.ops)
    key = bytes.fromhex(doc["key_hex"])
    body = bytes.fromhex(doc["body_hex"]) if doc["body_hex"] else b""
    if args.body_text is not None:
        iv = bytes.fromhex(fields["n"])
        if sk is None:
            fail("--body-text needs --session-key-hex")
        body = fvproto.aes_ctr(sk, iv, args.body_text.encode())
    new = fvproto.build_frame(key, fvproto.build_text(fields), body)
    pathlib.Path(args.out).write_bytes(new)
    print(json.dumps({"ok": True, "out": args.out, "fields": fields,
                      "text_len": len(fvproto.build_text(fields)), "body_len": len(body)}))


def cmd_preflight(args) -> None:
    report = {"ok": True, "python": sys.version.split()[0], "lab_env": os.environ.get("AJS_LAB")}
    try:
        import cryptography
        report["cryptography"] = cryptography.__version__
    except Exception as exc:  # noqa: BLE001
        report["cryptography"] = repr(exc)
    hosts = pathlib.Path("/etc/hosts")
    report["etc_hosts"] = hosts.read_text(encoding="utf-8", errors="replace") if hosts.exists() else None
    report["vendor_hosts_redirected"] = bool(report["etc_hosts"] and "login.mmegg.com" in report["etc_hosts"])
    try:
        route = pathlib.Path("/proc/net/route").read_text(encoding="utf-8")
        report["default_route"] = any(line.split()[1] == "00000000" for line in route.splitlines()[1:])
    except OSError:
        report["default_route"] = None
    listening = []
    for port in (443, 4443, 8443, 8444):
        s = socket.socket()
        try:
            s.bind(("127.0.0.1", port))
        except OSError:
            listening.append(port)
        finally:
            s.close()
    report["busy_loopback_ports"] = listening
    print(json.dumps(report, indent=2))


def cmd_selftest(args) -> None:
    checks = {}
    import tempfile
    from cryptography.hazmat.primitives import serialization

    with tempfile.TemporaryDirectory(prefix="ajs-lab-selftest-") as tmp:
        tmp = pathlib.Path(tmp)
        # 1. frame round-trip + RSA OAEP-SHA1
        key, sk, iv = secrets.token_bytes(16), secrets.token_bytes(16), secrets.token_bytes(16)
        priv = fvproto.generate_rsa(1024)
        pub = priv.public_key()
        k_field = fvproto.rsa_oaep_encrypt(pub, sk)
        body = b"c=au\nu=labuser\np=lab\n"
        text = fvproto.build_text({"s": "", "n": iv.hex(), "k": k_field,
                                   "l": str(len(body)), "_": "labpad"})
        packet = fvproto.build_frame(key, text, fvproto.aes_ctr(sk, iv, body))
        parsed = fvproto.parse_frame(packet, private_key=priv, show_secrets=True)
        checks["frame_roundtrip"] = parsed["fields"]["k"] == k_field and parsed["body_plain"] == body.decode()
        checks["rsa_oaep_sha1"] = fvproto.rsa_oaep_decrypt(priv, k_field) == sk
        # 2. truncated frame is rejected
        cut = 16 + 4 + len(text.encode("utf-8")) - 10
        try:
            fvproto.parse_frame(packet[:cut])
            checks["frame_length_mismatch_detected"] = False
        except ValueError:
            checks["frame_length_mismatch_detected"] = True
        # 3. mock FV auth + data channel
        log = tmp / "fv.jsonl"
        srv = fvproto.FVServer(("127.0.0.1", 0), priv, str(log)).start()
        try:
            host, port = srv.server_address
            c = socket.create_connection((host, port), timeout=3)
            c.sendall(packet)
            c.settimeout(3)
            head = fvproto.recv_exact(c, 4)
            n = struct.unpack(">I", fvproto.aes_ctr(key, fvproto.ZERO_IV, head))[0]
            text_ct = fvproto.recv_exact(c, n)
            code_text = fvproto.aes_ctr(key, fvproto.ZERO_IV, head + text_ct)[4:].decode()
            auth_ct = b""
            while True:
                chunk = c.recv(4096)
                if not chunk:
                    break
                auth_ct += chunk
            auth_map = fvproto.parse_fields(fvproto.aes_ctr(sk, iv, auth_ct).decode())
            c.close()
            checks["mock_fv_auth"] = "code=200" in code_text and auth_map.get("r") == "ok"
            sid = auth_map.get("sid", "")
            iv2 = secrets.token_bytes(16)
            req_key = secrets.token_bytes(16)
            socks = fvproto.socks5_request("example.com", 80)
            text2 = fvproto.build_text({"s": sid, "n": iv2.hex(), "k": "", "_": "labpad"})
            c = socket.create_connection((host, port), timeout=3)
            c.sendall(fvproto.build_frame(req_key, text2, fvproto.aes_ctr(sk, iv2, socks)))
            c.settimeout(3)
            head = fvproto.recv_exact(c, 4)
            n = struct.unpack(">I", fvproto.aes_ctr(req_key, fvproto.ZERO_IV, head))[0]
            text_ct = fvproto.recv_exact(c, n)
            code2 = fvproto.aes_ctr(req_key, fvproto.ZERO_IV, head + text_ct)[4:].decode()
            reply = fvproto.recv_exact(c, 10)
            reply_plain = fvproto.aes_ctr(sk, iv2, reply)
            c.close()
            checks["mock_fv_data"] = "code=200" in code2 and reply_plain[:2] == b"\x05\x00"
        finally:
            srv.shutdown()
            srv.server_close()
        # 4. mock control over HTTPS with lab CA
        ca_dir = tmp / "pki"
        generate_ca(str(ca_dir), ["lab.invalid"], ["127.0.0.1"], 2)
        cap = Capture(str(tmp / "cap"), b"salt")
        httpd = make_control_server("127.0.0.1", 0, str(ca_dir / "server.pem"),
                                    str(ca_dir / "server.key"), DEFAULT_FIXTURE, cap)
        threading.Thread(target=httpd.serve_forever, daemon=True).start()
        try:
            ctx = ssl.create_default_context(cafile=str(ca_dir / "ca.pem"))
            req = urllib.request.Request(
                f"https://127.0.0.1:{httpd.server_address[1]}/client.php",
                data=urllib.parse.urlencode({"cmd": "ClientApiLogin/Login",
                                             "username": "labuser",
                                             "password": "lab-secret"}).encode(),
                headers={"Content-Type": "application/x-www-form-urlencoded"})
            with urllib.request.urlopen(req, context=ctx, timeout=5) as r:
                doc = json.loads(r.read())
            checks["mock_control_https"] = doc["Login"]["AuthResult"] == "OK"
            raw = (tmp / "cap" / "capture.jsonl").read_text(encoding="utf-8")
            checks["capture_redacts_password"] = "lab-secret" not in raw and "<redacted>" in raw
        finally:
            httpd.shutdown()
            httpd.server_close()
        # 5. redaction / diff / mutation / allowlist
        red = redact_obj({"password": "x", "_uid": "abc", "cmd": "y"}, b"salt")
        checks["redaction"] = red["password"] == "<redacted>" and red["_uid"].startswith("<hmac:")
        a = normalize({"cmd": "A", "t": "1", "_uid": "u1", "n": "aa"}, set(DIFF_VOLATILE))
        b = normalize({"cmd": "A", "t": "2", "_uid": "u2", "n": "bb"}, set(DIFF_VOLATILE))
        checks["diff_normalization"] = diff(a, b) == []
        checks["mutation_apply"] = apply_ops({"cmd": "A", "_v": "1", "cv": "x"},
                                             "delete:cmd;set:_v=9;empty:cv") == {"_v": "9", "cv": ""}
        try:
            assert_lab_host("203.0.113.10")
            checks["allowlist_rejects_external"] = False
        except LabRefused:
            checks["allowlist_rejects_external"] = True
    ok = all(checks.values())
    print(json.dumps({"ok": ok, "checks": checks}, indent=2))
    raise SystemExit(0 if ok else 1)


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest="cmd", required=True)

    ca = sub.add_parser("ca", help="generate a lab CA and server certificate")
    ca.add_argument("--dir", required=True)
    ca.add_argument("--host", action="append", default=[])
    ca.add_argument("--ip", action="append", default=[])
    ca.add_argument("--days", type=int, default=2)
    ca.set_defaults(func=cmd_ca)

    mc = sub.add_parser("mock-control", help="HTTPS mock /client.php with capture")
    mc.add_argument("--bind", default="10.75.0.1")
    mc.add_argument("--port", action="append", type=int, default=[])
    mc.add_argument("--cert", required=True)
    mc.add_argument("--key", required=True)
    mc.add_argument("--fixture")
    mc.add_argument("--capture", required=True)
    mc.add_argument("--salt-file")
    mc.add_argument("--status", type=int, default=200)
    mc.add_argument("--encoding", choices=["none", "gzip", "deflate"], default="none")
    mc.add_argument("--i-understand-lab-only", action="store_true")
    mc.set_defaults(func=cmd_mock_control)

    mf = sub.add_parser("mock-fv", help="mock FV binary carrier")
    mf.add_argument("--bind", default="10.75.0.1")
    mf.add_argument("--port", type=int, default=8443)
    mf.add_argument("--rsa-key", required=True)
    mf.add_argument("--capture-log")
    mf.add_argument("--permissive", action="store_true",
                    help="accept an unknown sid and reuse the last auth session key")
    mf.add_argument("--code", default="200")
    mf.add_argument("--r", default="ok")
    mf.add_argument("--no-halfclose", action="store_true")
    mf.add_argument("--ka", default="30")
    mf.add_argument("--i-understand-lab-only", action="store_true")
    mf.set_defaults(func=cmd_mock_fv)

    tap = sub.add_parser("tap", help="record a TCP relay between client and mock")
    tap.add_argument("--listen", required=True)
    tap.add_argument("--upstream", required=True)
    tap.add_argument("--out", required=True)
    tap.add_argument("--idle", type=float, default=10.0)
    tap.add_argument("--i-understand-lab-only", action="store_true")
    tap.set_defaults(func=cmd_tap)

    rp = sub.add_parser("replay", help="replay captured client byte streams")
    rp.add_argument("--capture", required=True)
    rp.add_argument("--target", required=True)
    rp.add_argument("--out", required=True)
    rp.add_argument("--timeout", type=float, default=5.0)
    rp.add_argument("--i-understand-lab-only", action="store_true")
    rp.set_defaults(func=cmd_replay)

    rd = sub.add_parser("redact", help="redact a JSONL capture")
    rd.add_argument("--input", required=True)
    rd.add_argument("--output", required=True)
    rd.add_argument("--salt-file")
    rd.set_defaults(func=cmd_redact)

    df = sub.add_parser("diff", help="normalized diff of two JSON documents")
    df.add_argument("--a", required=True)
    df.add_argument("--b", required=True)
    df.add_argument("--volatile")
    df.set_defaults(func=cmd_diff)

    mu = sub.add_parser("mutate", help="materialize mutation cases from the matrix")
    mu.add_argument("--request", required=True)
    mu.add_argument("--matrix", required=True)
    mu.add_argument("--out", required=True)
    mu.add_argument("--id")
    mu.add_argument("--layer", help="only rows whose layer column matches, e.g. control_request")
    mu.set_defaults(func=cmd_mutate)

    kg = sub.add_parser("keygen", help="generate a lab FV RSA key and decimal modulus")
    kg.add_argument("--out", required=True)
    kg.add_argument("--bits", type=int, default=1024)
    kg.add_argument("--digits", type=int, default=309,
                    help="modulus length required by the vendor patch site (default 309)")
    kg.set_defaults(func=cmd_keygen)

    pm = sub.add_parser("patch-modulus", help="replace the vendor modulus in a lab copy")
    pm.add_argument("--binary", required=True)
    pm.add_argument("--modulus-file", required=True)
    pm.add_argument("--offset", type=lambda s: int(s, 0))
    pm.add_argument("--out", required=True)
    pm.set_defaults(func=cmd_patch_modulus)

    fr = sub.add_parser("frame", help="parse or mutate an FV carrier frame")
    fr_sub = fr.add_subparsers(dest="frame_cmd", required=True)
    fp = fr_sub.add_parser("parse")
    fp.add_argument("--file", required=True)
    fp.add_argument("--rsa-key")
    fp.add_argument("--session-key-hex")
    fp.add_argument("--show-secrets", action="store_true")
    fp.set_defaults(func=cmd_frame)
    fm = fr_sub.add_parser("mutate")
    fm.add_argument("--file", required=True)
    fm.add_argument("--rsa-key")
    fm.add_argument("--session-key-hex")
    fm.add_argument("--ops", required=True)
    fm.add_argument("--body-text")
    fm.add_argument("--out", required=True)
    fm.set_defaults(func=cmd_frame)

    pf = sub.add_parser("preflight", help="print lab environment facts")
    pf.set_defaults(func=cmd_preflight)

    st = sub.add_parser("selftest", help="run offline checks")
    st.set_defaults(func=cmd_selftest)
    return p


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    if getattr(args, "port", None) == []:
        args.port = [443, 4443]
    try:
        args.func(args)
    except LabRefused as exc:
        print(f"REFUSED: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
