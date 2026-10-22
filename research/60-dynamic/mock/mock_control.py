#!/usr/bin/env python3
"""Mock AJiaSu control plane (/client.php) for offline dynamic analysis.

Listens on 443 and 4443 with a locally generated certificate, logs every
request verbatim (method, path, headers, body) to captures/control.jsonl and
returns a crafted success envelope with one fake FV server at 127.0.0.1:8443.

No real service is contacted. Dummy credentials only.
"""
import json
import os
import ssl
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qsl

LAB = os.environ.get("AJS_LAB", "/tmp/ajs-lab")
CAP = os.path.join(LAB, "captures")
CERT = os.path.join(LAB, "certs", "server.crt")
KEY = os.path.join(LAB, "certs", "server.key")

SERVERS = {
    "vvn-test": {
        "id": "vvn-test",
        "vsid": 1,
        "ctry": "CN",
        "svr": "127.0.0.1",
        "sts": "ok",
        "tit": "Test",
        "cmt": "",
        "ord": "1",
        "cat": ["cn"],
        "par": {"fv": {"u": 1, "p": 8443, "ps": "x"}},
        "loc": [{"c": "310000", "n": "Test", "o": 1}],
    }
}

LOGIN = {
    "AuthResult": "OK",
    "AuthMessage": "",
    "LoginToken": "test|token",
    "UserName": "test",
    "ConnectPassword": "test",
    "Email": "test@example.invalid",
    "PhoneNumber": "",
    "MembershipStatus": "VIP",
    "GroupTitle": "VIP",
    "ExpireTime": 4102444800,
    "Expired": False,
    "UseCount": 0,
    "UrlWeb": "http://127.0.0.1/",
    "LatestVersion": "4.2.3.0",
}

ENVELOPE = {
    "Result": "Success",
    "Success": True,
    "Code": 200,
    "Message": "",
    "Error": "",
    "ErrorMessage": "",
    "Login": LOGIN,
    "ResponseLogin": LOGIN,
    "Servers": SERVERS,
    "SyncVersion": {},
    "AppUpdate": {},
    "Data": [],
    "List": [],
    "Count": 0,
    "Content": "",
    "PresetRoute": [],
    "RouteSection": [],
    "UrlWeb": "http://127.0.0.1/",
    "LatestVersion": "4.2.3.0",
    "Comment": "",
    "Status": "ok",
    "Sites": [],
}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "mock-ajs"
    sys_version = ""

    def log_message(self, fmt, *args):  # silence default stderr logging
        pass

    def _handle(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n) if n else b""
        rec = {
            "ts": time.time(),
            "port": self.server.server_address[1],
            "method": self.command,
            "path": self.path,
            "headers": {k: v for k, v in self.headers.items()},
            "body": body.decode("utf-8", "replace"),
            "body_hex": body.hex(),
        }
        try:
            q = dict(parse_qsl(rec["body"], keep_blank_values=True))
        except Exception:
            q = {}
        rec["cmd"] = q.get("cmd", "")
        rec["body_params"] = q
        with open(os.path.join(CAP, "control.jsonl"), "a", encoding="utf-8") as f:
            f.write(json.dumps(rec, ensure_ascii=False) + "\n")

        resp_file = os.environ.get("AJS_RESPONSE_FILE")
        if resp_file:
            with open(resp_file, "rb") as f:
                payload = f.read()
        else:
            payload = json.dumps(ENVELOPE).encode()
        with open(os.path.join(CAP, "control_responses.log"), "ab") as f:
            f.write(b"--- " + str(rec["cmd"]).encode() + b"\n" + payload + b"\n")
        self.send_response(200)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    do_POST = _handle
    do_GET = _handle


def serve(port):
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(CERT, KEY)
    httpd = ThreadingHTTPServer(("0.0.0.0", port), Handler)
    httpd.socket = ctx.wrap_socket(httpd.socket, server_side=True)
    print(f"mock control listening on {port}", flush=True)
    httpd.serve_forever()


if __name__ == "__main__":
    os.makedirs(CAP, exist_ok=True)
    ports = [int(p) for p in (sys.argv[1:] or ["443", "4443"])]
    threads = [threading.Thread(target=serve, args=(p,), daemon=True) for p in ports]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
