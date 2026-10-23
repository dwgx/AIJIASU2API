#!/usr/bin/env python3
"""Validate the AJS-16 SDK / protocol integration design artifacts.

Checks the two deliverables in this slice, not the rest of the repository:

* ``REPORT.md`` has the eight mandated sections, a protocol surface table with valid
  status/evidence/role cells, a field-source table with a tag on every row, and a
  failure taxonomy with an action verb on every row.
* ``INTERFACE.md`` declares at least six Go interfaces (each with at least one method),
  at least twelve HTTP endpoints, and the same protocol ids as the report.
* Both files agree on the three state machines (account, node, flow).
* Neither file carries a credential: no PEM private key, no non-placeholder password.

Success prints exactly ``OK protocols=<N> interfaces=<M>`` and exits 0.
Failure prints ``FAIL: <reason>`` to stderr and exits 1.

Standard library only. Run: python research/76-sdk-integration/validate.py
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SLICE = ROOT / "research" / "76-sdk-integration"
REPORT = SLICE / "REPORT.md"
INTERFACE = SLICE / "INTERFACE.md"

REQUIRED_SECTIONS = [
    "Answer",
    "Protocol surface",
    "Adapter contract",
    "Panel integration",
    "Cross-platform",
    "Observability",
    "Failure taxonomy",
    "Open questions",
]

MIN_PROTOCOLS = 4
MIN_INTERFACES = 6
MIN_FIELDS = 40
MIN_FAILURES = 10
MIN_ENDPOINTS = 12
MIN_METRICS = 15
MIN_BYTES = 8000

STATUS = {"IMPLEMENTED", "PARTIAL", "NONE"}
EVIDENCE = {"CONFIRMED", "INFERENCE", "UNKNOWN"}
ROLES = {"PRIMARY", "FALLBACK", "PLUGGABLE", "PASSIVE-ONLY", "EXCLUDED", "MAPPED"}
ACTIONS = {"RETRY", "BACKOFF", "CIRCUIT", "DRAIN", "DEGRADE", "TERMINAL", "MAP", "REJECT"}
METHODS = {"GET", "POST", "PATCH", "PUT", "DELETE"}

ACCOUNT_STATES = [
    "NEW",
    "LOGGING_IN",
    "AUTHENTICATED",
    "SERVERS_OK",
    "READY",
    "DRAINING",
    "STOPPED",
    "AUTH_FAILED",
    "MEMBERSHIP_EXPIRED",
    "SUSPENDED",
]
NODE_STATES = ["STARTING", "READY", "LIVE", "DRAINING", "DOWN"]
FLOW_STATES = ["ACCEPTED", "ADMITTED", "CONNECT_SENT", "REMOTE_OK", "RELAY", "CLOSED"]

REDACTION_TERMS = ["ConnectPassword", "LoginToken", "sessionKey", "k="]

PASSWORD_PLACEHOLDERS = {
    "REPLACE_ME",
    "REDACTED",
    "[redacted]",
    "<redacted>",
    "test",
    "",
    "…",
}

PROTOCOL_ROW = re.compile(r"^P\d+$")
INTERFACE_DECL = re.compile(r"^type\s+([A-Z][A-Za-z0-9_]*)\s+interface\s*\{")
METHOD_DECL = re.compile(r"^\s{1,8}[A-Z][A-Za-z0-9_]*\(")
PASSWORD_LITERAL = re.compile(r'"password"\s*:\s*"([^"]*)"')
PEM_MARKER = re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----")


class ValidationError(Exception):
    pass


def read_text(path: Path) -> str:
    try:
        text = path.read_text(encoding="utf-8")
    except FileNotFoundError as exc:
        raise ValidationError(f"missing file: {path.relative_to(ROOT)}") from exc
    except OSError as exc:  # pragma: no cover - unreadable file
        raise ValidationError(f"cannot read {path.relative_to(ROOT)}: {exc}") from exc
    if len(text.encode("utf-8")) < MIN_BYTES:
        raise ValidationError(
            f"{path.relative_to(ROOT)} is too small to be a design document "
            f"({len(text.encode('utf-8'))} bytes < {MIN_BYTES})"
        )
    return text


def sections(text: str) -> dict[str, str]:
    """Map each level-2 heading to its body (up to the next level-2 heading)."""
    found: dict[str, list[str]] = {}
    current: str | None = None
    for line in text.splitlines():
        if line.startswith("## "):
            current = line[3:].strip()
            found.setdefault(current, [])
        elif current is not None:
            found[current].append(line)
    return {name: "\n".join(body) for name, body in found.items()}


def table_rows(body: str) -> list[list[str]]:
    """Return the cells of every table row, skipping header separators.

    A backslash-escaped pipe (``\\|``) is content, not a cell boundary.
    """
    rows: list[list[str]] = []
    for line in body.splitlines():
        stripped = line.strip()
        if not stripped.startswith("|") or not stripped.endswith("|"):
            continue
        protected = stripped.replace("\\|", "\x00")
        cells = [cell.strip().replace("\x00", "|") for cell in protected.strip("|").split("|")]
        if not cells:
            continue
        if all(re.fullmatch(r":?-{2,}:?", cell) for cell in cells if cell):
            # The separator is preceded by the header row: drop the header so callers
            # only ever see data rows.
            if rows:
                rows.pop()
            continue
        rows.append(cells)
    return rows


def strip_ticks(value: str) -> str:
    return value.strip().strip("`").strip()


def tag(value: str) -> str:
    """Normalize a table cell used as an evidence tag: `[CONFIRMED]` -> CONFIRMED."""
    return strip_ticks(value).strip("[]").strip()


def require_sections(report: str) -> dict[str, str]:
    parts = sections(report)
    missing = [name for name in REQUIRED_SECTIONS if name not in parts]
    if missing:
        raise ValidationError("REPORT.md is missing sections: " + ", ".join(missing))
    return parts


def protocol_rows(parts: dict[str, str]) -> list[list[str]]:
    rows = [r for r in table_rows(parts["Protocol surface"]) if PROTOCOL_ROW.fullmatch(strip_ticks(r[0]))]
    if len(rows) < MIN_PROTOCOLS:
        raise ValidationError(
            f"protocol surface lists {len(rows)} protocols, need >= {MIN_PROTOCOLS}"
        )
    seen: set[str] = set()
    for row in rows:
        if len(row) < 6:
            raise ValidationError(f"protocol row has {len(row)} cells, need 6: {row}")
        pid = strip_ticks(row[0])
        if pid in seen:
            raise ValidationError(f"duplicate protocol id {pid}")
        seen.add(pid)
        status, evidence, role = (strip_ticks(row[3]), strip_ticks(row[4]), strip_ticks(row[5]))
        if status not in STATUS:
            raise ValidationError(f"{pid}: status {status!r} not in {sorted(STATUS)}")
        if evidence not in EVIDENCE:
            raise ValidationError(f"{pid}: evidence {evidence!r} not in {sorted(EVIDENCE)}")
        if role not in ROLES:
            raise ValidationError(f"{pid}: role {role!r} not in {sorted(ROLES)}")
    if not any(strip_ticks(r[3]) == "IMPLEMENTED" for r in rows):
        raise ValidationError("no protocol is marked IMPLEMENTED; the surface table is a wish list")
    if not any(strip_ticks(r[5]) in {"EXCLUDED", "PASSIVE-ONLY"} for r in rows):
        raise ValidationError("no protocol is excluded or passive; scope discipline is missing")
    return rows


def field_rows(parts: dict[str, str]) -> int:
    if "Field source table" not in parts:
        raise ValidationError("REPORT.md is missing the 'Field source table' section")
    rows = [r for r in table_rows(parts["Field source table"]) if len(r) >= 5]
    tagged = [r for r in rows if tag(r[4]) in EVIDENCE]
    if len(tagged) != len(rows):
        bad = [r[0] for r in rows if tag(r[4]) not in EVIDENCE][:5]
        raise ValidationError(f"field rows without a valid source tag: {bad}")
    if len(tagged) < MIN_FIELDS:
        raise ValidationError(f"field table has {len(tagged)} rows, need >= {MIN_FIELDS}")
    present = {tag(r[4]) for r in tagged}
    if present != EVIDENCE:
        raise ValidationError(f"field table tags {sorted(present)} do not cover {sorted(EVIDENCE)}")
    return len(tagged)


def failure_rows(parts: dict[str, str]) -> int:
    rows = [r for r in table_rows(parts["Failure taxonomy"]) if len(r) >= 6]
    classes = [r for r in rows if re.fullmatch(r"[A-Z][A-Z_]{3,}", strip_ticks(r[0]))]
    if len(classes) < MIN_FAILURES:
        raise ValidationError(
            f"failure taxonomy has {len(classes)} classes, need >= {MIN_FAILURES}"
        )
    for row in classes:
        action = strip_ticks(row[3]).upper()
        if not any(verb in action for verb in ACTIONS):
            raise ValidationError(
                f"{strip_ticks(row[0])}: action {row[3]!r} has no verb from {sorted(ACTIONS)}"
            )
    return len(classes)


def interfaces(text: str) -> list[tuple[str, int]]:
    if "Go interfaces" not in sections(text):
        raise ValidationError("INTERFACE.md is missing the 'Go interfaces' section")
    body = sections(text)["Go interfaces"]
    found: list[tuple[str, int]] = []
    current: str | None = None
    methods = 0
    for line in body.splitlines():
        match = INTERFACE_DECL.match(line.strip())
        if match:
            if current is not None:
                found.append((current, methods))
            current = match.group(1)
            methods = 0
            continue
        if current is not None:
            if line.strip() == "}":
                found.append((current, methods))
                current, methods = None, 0
            elif METHOD_DECL.match(line):
                methods += 1
    if current is not None:
        found.append((current, methods))
    names = [name for name, _ in found]
    if len(names) != len(set(names)):
        raise ValidationError("duplicate interface names in 'Go interfaces'")
    empty = [name for name, count in found if count == 0]
    if empty:
        raise ValidationError(f"interfaces with no methods: {empty}")
    if len(found) < MIN_INTERFACES:
        raise ValidationError(f"{len(found)} interfaces, need >= {MIN_INTERFACES}")
    return found


def endpoints(text: str) -> int:
    parts = sections(text)
    total = 0
    for name in ("HTTP: ajs-accountd", "HTTP: gate"):
        if name not in parts:
            raise ValidationError(f"INTERFACE.md is missing the '{name}' section")
        for row in table_rows(parts[name]):
            if len(row) < 2:
                continue
            method = strip_ticks(row[0]).upper()
            path = strip_ticks(row[1])
            if method in METHODS and path.startswith("/"):
                total += 1
    if total < MIN_ENDPOINTS:
        raise ValidationError(f"{total} HTTP endpoints, need >= {MIN_ENDPOINTS}")
    return total


def cross_reference(protocols: list[list[str]], interface_text: str) -> None:
    if "Protocol codecs" not in sections(interface_text):
        raise ValidationError("INTERFACE.md is missing the 'Protocol codecs' section")
    codec_ids = {
        strip_ticks(r[0])
        for r in table_rows(sections(interface_text)["Protocol codecs"])
        if PROTOCOL_ROW.fullmatch(strip_ticks(r[0]))
    }
    report_ids = {strip_ticks(r[0]) for r in protocols}
    missing = sorted(report_ids - codec_ids)
    extra = sorted(codec_ids - report_ids)
    if missing:
        raise ValidationError(f"protocols missing from INTERFACE.md codecs table: {missing}")
    if extra:
        raise ValidationError(f"codecs table names protocols absent from the report: {extra}")


def state_machines(report: str, interface_text: str) -> None:
    for name, states in (
        ("account", ACCOUNT_STATES),
        ("node", NODE_STATES),
        ("flow", FLOW_STATES),
    ):
        for state in states:
            token = f"`{state}`"
            if token not in report:
                raise ValidationError(f"REPORT.md never names the {name} state {state}")
            if token not in interface_text:
                raise ValidationError(f"INTERFACE.md never names the {name} state {state}")


def observability(parts: dict[str, str]) -> None:
    body = parts["Observability"]
    missing = [term for term in REDACTION_TERMS if term not in body]
    if missing:
        raise ValidationError(f"observability section does not redact: {missing}")
    metrics = {m for m in re.findall(r"`(ajs_[a-z0-9_]+)", body)}
    if len(metrics) < MIN_METRICS:
        raise ValidationError(f"{len(metrics)} distinct metrics, need >= {MIN_METRICS}")


def self_consistency(report: str, protocols: list[list[str]], failures: int, ifaces: int) -> None:
    words = {"Eight": 8, "Fifteen": 15, "Thirteen": 13}
    counts = {"Eight": len(protocols), "Fifteen": failures, "Thirteen": ifaces}
    for word, expected in words.items():
        if word in report and counts[word] != expected:
            raise ValidationError(
                f"REPORT.md says {word!r} but the artifact has {counts[word]}"
            )


def no_credentials(report: str, interface_text: str) -> None:
    for name, text in (("REPORT.md", report), ("INTERFACE.md", interface_text)):
        if PEM_MARKER.search(text):
            raise ValidationError(f"{name} contains a private key block")
        for value in PASSWORD_LITERAL.findall(text):
            if value not in PASSWORD_PLACEHOLDERS:
                raise ValidationError(f"{name} contains a literal password value {value!r}")


def main() -> int:
    report = read_text(REPORT)
    interface_text = read_text(INTERFACE)

    parts = require_sections(report)
    protocols = protocol_rows(parts)
    fields = field_rows(parts)
    failures = failure_rows(parts)
    ifaces = interfaces(interface_text)
    total_endpoints = endpoints(interface_text)
    cross_reference(protocols, interface_text)
    state_machines(report, interface_text)
    observability(parts)
    self_consistency(report, protocols, failures, len(ifaces))
    no_credentials(report, interface_text)

    print(
        f"checked: fields={fields} failures={failures} endpoints={total_endpoints} "
        f"interfaces={len(ifaces)}",
        file=sys.stderr,
    )
    print(f"OK protocols={len(protocols)} interfaces={len(ifaces)}")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except ValidationError as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        sys.exit(1)
