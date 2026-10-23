#!/usr/bin/env python3
"""Send one materialized control-plane case to the lab mock (AJS-18).

The case file is either `lab/fixtures/request_login.json` (baseline) or one
`captures/mutants/<ID>.json` produced by `labctl.py mutate`. The URL host must
resolve into loopback or 10.75.0.0/24; anything else is refused before a socket
is opened.

    AJS_LAB=1 python3 lab/send_mutant.py \
        --url https://login.mmegg.com/client.php \
        --case captures/mutants/C01.json \
        --cacert lab/pki/ca.pem \
        --out captures/responses/C01.json \
        --i-understand-lab-only

Only field *names* are written to the response record; values never are.
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import ssl
import sys
import urllib.error
import urllib.parse
import urllib.request

import labctl


def parse_args(argv=None) -> argparse.Namespace:
    p = argparse.ArgumentParser(description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--url", required=True, help="mock control URL, e.g. https://login.mmegg.com/client.php")
    p.add_argument("--case", required=True, help="request fixture or mutant JSON with a fields map")
    p.add_argument("--cacert", required=True, help="lab CA PEM used to verify the mock")
    p.add_argument("--out", required=True, help="where to write the response record")
    p.add_argument("--response-out", help="write only the parsed response body (for diff)")
    p.add_argument("--timeout", type=float, default=10.0)
    p.add_argument("--i-understand-lab-only", action="store_true")
    return p.parse_args(argv)


def main(argv=None) -> int:
    args = parse_args(argv)
    if os.environ.get("AJS_LAB") != "1" or not args.i_understand_lab_only:
        print("REFUSED: set AJS_LAB=1 and pass --i-understand-lab-only", file=sys.stderr)
        return 2
    parts = urllib.parse.urlsplit(args.url)
    try:
        if not parts.hostname:
            raise labctl.LabRefused(f"no host in {args.url!r}")
        labctl.assert_lab_host(parts.hostname, parts.port)
    except labctl.LabRefused as exc:
        print(f"REFUSED: {exc}", file=sys.stderr)
        return 2

    case = json.loads(pathlib.Path(args.case).read_text(encoding="utf-8"))
    fields = case.get("fields")
    if not isinstance(fields, dict):
        print(f"REFUSED: {args.case} has no fields map", file=sys.stderr)
        return 2
    pairs = case.get("pairs")
    if pairs is None:
        pairs = list(fields.items())
    pairs = [(str(k), str(v)) for k, v in pairs]
    body = urllib.parse.urlencode(pairs).encode("utf-8")
    if case.get("trailing_amp"):
        body += b"&"
    try:
        ctx = ssl.create_default_context(cafile=args.cacert)
    except Exception as exc:  # noqa: BLE001 - bad path/PEM is an operator error
        print(f"REFUSED: cannot load CA {args.cacert!r}: {exc}", file=sys.stderr)
        return 2
    req = urllib.request.Request(
        args.url, data=body,
        headers={"Content-Type": "application/x-www-form-urlencoded",
                 "Accept-Encoding": "gzip, deflate"})
    rec = {"case": case.get("id"), "url": args.url, "status": None,
           "sent_fields": sorted(k for k, _ in pairs), "body_len": len(body)}
    raw = b""
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=args.timeout) as resp:
            rec["status"] = resp.status
            rec["headers"] = {k.lower(): v for k, v in resp.headers.items()
                              if k.lower() in {"content-type", "content-encoding", "content-length"}}
            raw = resp.read()
    except urllib.error.HTTPError as exc:
        rec["status"] = exc.code
        raw = exc.read()
    except Exception as exc:  # noqa: BLE001 - recorded, not raised
        rec["error"] = f"{type(exc).__name__}: {exc}"
    if raw:
        try:
            rec["json"] = json.loads(raw)
        except ValueError:
            rec["body_prefix"] = raw[:200].decode("utf-8", "replace")
            rec["body_len_actual"] = len(raw)
    out = pathlib.Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(rec, indent=2), encoding="utf-8")
    if args.response_out:
        payload = rec.get("json")
        if payload is None:
            payload = {"status": rec.get("status"), "error": rec.get("error"),
                       "body_prefix": rec.get("body_prefix")}
        body_out = pathlib.Path(args.response_out)
        body_out.parent.mkdir(parents=True, exist_ok=True)
        body_out.write_text(json.dumps(payload, indent=2), encoding="utf-8")
    summary = {k: rec[k] for k in ("case", "status", "error") if k in rec}
    summary["out"] = str(out)
    print(json.dumps(summary))
    return 0


if __name__ == "__main__":
    sys.exit(main())
