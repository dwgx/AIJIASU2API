#!/usr/bin/env python3
"""AJS-18 DONE WHEN check for research/75-request-mutation.

Structural checks:
  - REPORT.md has the six required sections and >= 8 scenarios, each with
    Goal / Setup / Commands / Observe / Artifacts / Safety / rollback / Proves.
  - LAB.md covers S1-S3 (mock control, mock FV, tap/replay) and teardown.
  - lab/fvproto.py, lab/labctl.py, lab/rollback.sh and mutation-matrix.tsv exist;
    the matrix has 8 columns, unique ids and >= 40 rows.
  - rollback.sh is idempotent (check/restore/purge) and prints OK rollback=1.

Functional checks (offline, no sockets):
  - frame round-trip with a fresh 1024-bit RSA key, OAEP-SHA1 decrypt;
  - truncated frame rejected; oversized text rejected by the codec;
  - mutation operators, redaction, destination allowlist;
  - send_mutant.py refuses to run without the opt-in.

On success prints exactly:  OK scenarios=<N> rollback=1
Exit code 0.
"""

from __future__ import annotations

import contextlib
import io
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.abspath(__file__))
LAB = os.path.join(ROOT, "lab")
REPORT = os.path.join(ROOT, "REPORT.md")
LABMD = os.path.join(ROOT, "LAB.md")
MATRIX = os.path.join(ROOT, "mutation-matrix.tsv")
ROLLBACK = os.path.join(LAB, "rollback.sh")

REQUIRED_SECTIONS = [
    "## Answer",
    "## Lab topology",
    "## Scenarios",
    "## Guardrails",
    "## Integration with the panel",
    "## Open questions",
]
SCENARIO_FIELDS = [
    "**Goal**",
    "**Setup**",
    "**Commands**",
    "**Observe**",
    "**Artifacts**",
    "**Safety / rollback**",
    "**Proves**",
]
REQUIRED_FILES = [
    os.path.join(LAB, "fvproto.py"),
    os.path.join(LAB, "labctl.py"),
    os.path.join(LAB, "send_mutant.py"),
    ROLLBACK,
    MATRIX,
    os.path.join(LAB, "fixtures", "login_ok.json"),
    os.path.join(LAB, "fixtures", "request_login.json"),
]
MATRIX_COLUMNS = 8
MIN_ROWS = 40
MIN_SCENARIOS = 8


def fail(errors: list[str], msg: str) -> None:
    errors.append(msg)


def read(path: str) -> str:
    with open(path, encoding="utf-8") as f:
        return f.read()


def scenario_bodies(text: str) -> list[tuple[str, str]]:
    m = re.search(r"^## Scenarios\s*$", text, re.M)
    if not m:
        return []
    rest = text[m.end():]
    nxt = re.search(r"^## ", rest, re.M)
    body = rest[:nxt.start()] if nxt else rest
    parts = re.split(r"^### (S\d+)\b.*$", body, flags=re.M)
    out = []
    for i in range(1, len(parts), 2):
        out.append((parts[i].strip(), parts[i + 1]))
    return out


def check_report(errors: list[str]) -> int:
    if not os.path.isfile(REPORT):
        fail(errors, f"missing {REPORT}")
        return 0
    text = read(REPORT)
    for section in REQUIRED_SECTIONS:
        if not re.search(r"^" + re.escape(section) + r"\s*$", text, re.M):
            fail(errors, f"REPORT.md missing section: {section}")
    scenarios = scenario_bodies(text)
    if len(scenarios) < MIN_SCENARIOS:
        fail(errors, f"REPORT.md has {len(scenarios)} scenarios, need >= {MIN_SCENARIOS}")
    seen = set()
    for sid, body in scenarios:
        if sid in seen:
            fail(errors, f"duplicate scenario id {sid}")
        seen.add(sid)
        for field in SCENARIO_FIELDS:
            if field not in body:
                fail(errors, f"{sid} missing field {field}")
        if "rollback" not in body.lower():
            fail(errors, f"{sid} does not mention rollback")
    if not re.search(r"^## Guardrails\s*$", text, re.M):
        fail(errors, "REPORT.md missing Guardrails")
    else:
        guard = text.split("## Guardrails", 1)[1].split("\n## ", 1)[0]
        for needle in ("Never do this", "Retention", "redact"):
            if needle.lower() not in guard.lower():
                fail(errors, f"Guardrails missing {needle!r}")
    return len(scenarios)


def check_lab_md(errors: list[str]) -> None:
    if not os.path.isfile(LABMD):
        fail(errors, f"missing {LABMD}")
        return
    text = read(LABMD)
    for needle in ("mock-control", "mock-fv", "labctl.py tap", "labctl.py replay",
                   "patch-modulus", "rollback.sh", "AJS_LAB=1"):
        if needle not in text:
            fail(errors, f"LAB.md missing {needle!r}")
    if len(text) < 3000:
        fail(errors, f"LAB.md is {len(text)} bytes, too short to be runnable")


def check_matrix(errors: list[str]) -> int:
    if not os.path.isfile(MATRIX):
        fail(errors, f"missing {MATRIX}")
        return 0
    lines = [ln for ln in read(MATRIX).splitlines() if ln.strip()]
    if not lines:
        fail(errors, "mutation-matrix.tsv is empty")
        return 0
    header = lines[0].split("\t")
    if len(header) != MATRIX_COLUMNS:
        fail(errors, f"matrix header has {len(header)} columns, want {MATRIX_COLUMNS}")
    rows = lines[1:]
    ids = []
    for ln in rows:
        cols = ln.split("\t")
        if len(cols) != len(header):
            fail(errors, f"matrix row {cols[0]!r} has {len(cols)} columns")
            continue
        ids.append(cols[0])
    if len(rows) < MIN_ROWS:
        fail(errors, f"matrix has {len(rows)} rows, need >= {MIN_ROWS}")
    if len(set(ids)) != len(ids):
        fail(errors, "matrix has duplicate ids")
    return len(rows)


def check_rollback(errors: list[str]) -> bool:
    if not os.path.isfile(ROLLBACK):
        fail(errors, f"missing {ROLLBACK}")
        return False
    text = read(ROLLBACK)
    checks = {
        "prints OK rollback=1": "OK rollback=1" in text,
        "check mode": "check)" in text,
        "restore mode": "restore)" in text,
        "purge mode": "purge)" in text,
        "hosts backup": "hosts.bak" in text,
        "CA removal": "update-ca-certificates" in text,
        "netns removal": "ip netns del" in text,
        "capture purge": "purge_captures" in text,
    }
    for name, ok in checks.items():
        if not ok:
            fail(errors, f"rollback.sh missing {name}")
    return all(checks.values())


def check_functional(errors: list[str]) -> None:
    sys.path.insert(0, LAB)
    try:
        import fvproto
        import labctl
        import send_mutant
    except Exception as exc:  # noqa: BLE001
        fail(errors, f"cannot import lab modules: {exc!r}")
        return

    try:
        key = bytes(range(16))
        session_key = bytes(range(16, 32))
        iv = bytes(range(32, 48))
        priv = fvproto.generate_rsa(1024)
        k_field = fvproto.rsa_oaep_encrypt(priv.public_key(), session_key)
        body = b"c=au\nu=labuser\np=lab\n"
        text = fvproto.build_text({"s": "", "n": iv.hex(), "k": k_field,
                                   "l": str(len(body)), "_": "labpad"})
        packet = fvproto.build_frame(key, text, fvproto.aes_ctr(session_key, iv, body))
        doc = fvproto.parse_frame(packet, private_key=priv, show_secrets=True)
        if doc["fields"]["k"] != k_field:
            fail(errors, "frame round-trip lost the k field")
        if doc["body_plain"] != body.decode():
            fail(errors, "frame round-trip did not decrypt the body")
        if doc["session_key"] != session_key.hex():
            fail(errors, "frame round-trip derived the wrong session key")
        if fvproto.rsa_oaep_decrypt(priv, k_field) != session_key:
            fail(errors, "OAEP-SHA1 decrypt mismatch")
    except Exception as exc:  # noqa: BLE001
        fail(errors, f"frame round-trip raised {exc!r}")

    cut = 16 + 4 + len(text.encode("utf-8")) - 10
    try:
        fvproto.parse_frame(packet[:cut])
        fail(errors, "truncated text frame was accepted")
    except ValueError:
        pass
    except Exception as exc:  # noqa: BLE001
        fail(errors, f"truncated text frame raised {type(exc).__name__}, want ValueError")

    # A short body is tolerated by the codec but must be reported, not silently accepted.
    short = fvproto.parse_frame(packet[:-10])
    if not any("body bytes follow" in w for w in short["warnings"]):
        fail(errors, "short body produced no length warning")

    try:
        fvproto.build_frame(key, "x" * 65536)
        fail(errors, "codec accepted text > 65535 bytes")
    except ValueError:
        pass

    try:
        got = labctl.apply_ops({"cmd": "A", "_v": "1", "cv": "x"},
                               "delete:cmd;set:_v=9;empty:cv")
        if got != {"_v": "9", "cv": ""}:
            fail(errors, f"mutation operators produced {got!r}")
    except Exception as exc:  # noqa: BLE001
        fail(errors, f"mutation operators raised {exc!r}")

    import tempfile
    expected_cases = max(0, len(read(MATRIX).splitlines()) - 1)
    with tempfile.TemporaryDirectory(prefix="ajs-mut-") as tmp:
        try:
            ns = labctl.build_parser().parse_args([
                "mutate",
                "--request", os.path.join(LAB, "fixtures", "request_login.json"),
                "--matrix", MATRIX, "--out", tmp])
            with contextlib.redirect_stdout(io.StringIO()):
                ns.func(ns)
            ids = {n[:-5] for n in os.listdir(tmp) if n.endswith(".json")}
            if len(ids) != expected_cases:
                fail(errors, f"mutate wrote {len(ids)} cases, want {expected_cases}")
            with open(os.path.join(tmp, "C16.json"), encoding="utf-8") as f:
                c16 = json.load(f)
            if sum(1 for k, _ in c16.get("pairs", []) if k == "username") != 2:
                fail(errors, "C16 did not keep the duplicate username key")
            with open(os.path.join(tmp, "C20.json"), encoding="utf-8") as f:
                if not json.load(f).get("trailing_amp"):
                    fail(errors, "C20 did not set trailing_amp")
        except labctl.LabRefused as exc:
            fail(errors, f"mutate refused a valid request: {exc}")
        except Exception as exc:  # noqa: BLE001
            fail(errors, f"mutate raised {exc!r}")

        control = os.path.join(tmp, "control")
        try:
            ns = labctl.build_parser().parse_args([
                "mutate",
                "--request", os.path.join(LAB, "fixtures", "request_login.json"),
                "--matrix", MATRIX, "--layer", "control_request", "--out", control])
            with contextlib.redirect_stdout(io.StringIO()):
                ns.func(ns)
            n_control = len([n for n in os.listdir(control) if n.endswith(".json")])
            if n_control != 20:
                fail(errors, f"--layer control_request wrote {n_control} cases, want 20")
        except labctl.LabRefused as exc:
            fail(errors, f"mutate --layer control_request failed: {exc}")
        except Exception as exc:  # noqa: BLE001
            fail(errors, f"mutate --layer control_request raised {exc!r}")

        try:
            ns = labctl.build_parser().parse_args([
                "mutate",
                "--request", os.path.join(LAB, "fixtures", "request_login.json"),
                "--matrix", MATRIX, "--layer", "fv_auth",
                "--out", os.path.join(tmp, "mismatch")])
            with contextlib.redirect_stdout(io.StringIO()):
                ns.func(ns)
        except labctl.LabRefused:
            pass
        else:
            fail(errors, "mutate accepted a layer/fixture mismatch")

    red = labctl.redact_obj({"password": "x", "_uid": "abc", "cmd": "y"}, b"salt")
    if red["password"] != "<redacted>" or not red["_uid"].startswith("<hmac:"):
        fail(errors, f"redaction produced {red!r}")

    try:
        labctl.assert_lab_host("203.0.113.10")
        fail(errors, "allowlist accepted 203.0.113.10")
    except labctl.LabRefused:
        pass

    buf = io.StringIO()
    with contextlib.redirect_stderr(buf), contextlib.redirect_stdout(buf):
        rc = send_mutant.main(["--url", "https://login.mmegg.com/client.php",
                               "--case", os.path.join(LAB, "fixtures", "request_login.json"),
                               "--cacert", os.path.join(LAB, "pki", "ca.pem"),
                               "--out", os.path.join(ROOT, "should-not-exist.json")])
    if rc != 2:
        fail(errors, f"send_mutant without opt-in returned {rc}, want 2")
    if os.path.exists(os.path.join(ROOT, "should-not-exist.json")):
        fail(errors, "send_mutant wrote output without opt-in")


def main() -> int:
    errors: list[str] = []
    for path in REQUIRED_FILES:
        if not os.path.isfile(path):
            fail(errors, f"missing {os.path.relpath(path, ROOT)}")
    n_scenarios = check_report(errors)
    check_lab_md(errors)
    n_rows = check_matrix(errors)
    rollback_ok = check_rollback(errors)
    check_functional(errors)

    if errors:
        for err in errors:
            print(f"FAIL {err}", file=sys.stderr)
        print(f"FAIL scenarios={n_scenarios} rollback={int(rollback_ok)} "
              f"matrix_rows={n_rows} errors={len(errors)}", file=sys.stderr)
        return 1
    print(f"OK scenarios={n_scenarios} rollback={int(rollback_ok)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
