#!/usr/bin/env python3
"""Offline FV/API conformance metric for the ajsproxy emulator.

Runs `go test ./... -json` in emulator/ and counts only the fixed allowlist of
named tests below. The allowlist is the metric: adding new tests must not raise
the number, so the autoresearch loop cannot game the metric by writing more
tests. This is a regression gate (target 8/8), not a growth metric.

Exit status:
  0  the test command succeeded and every allowlisted test passed
  1  the test command failed, a test was missing/skipped, or the run timed out
"""

from __future__ import annotations

import json
import os
import pathlib
import shutil
import subprocess
import sys

ALLOWLIST = (
    "TestSignVectors",
    "TestDualHash",
    "TestFrameRoundTrip",
    "TestAESCTR",
    "TestOAEPShape",
    "TestSOCKS5Handshake",
    "TestHTTPProxyCONNECT",
    "TestTransportRegistry",
)

ROOT = pathlib.Path(__file__).resolve().parents[1]
EMULATOR = ROOT / "emulator"
TIMEOUT_SEC = 600

GO_FALLBACKS = (
    r"D:/Software/Developer/go/bin/go.exe",
    "/usr/local/go/bin/go",
    "/usr/lib/go/bin/go",
)


def find_go() -> str:
    explicit = os.environ.get("GO")
    if explicit:
        return explicit
    found = shutil.which("go")
    if found:
        return found
    for candidate in GO_FALLBACKS:
        if pathlib.Path(candidate).exists():
            return candidate
    return "go"


def parse_events(stdout: str) -> tuple[set[str], set[str]]:
    passed: set[str] = set()
    not_passed: set[str] = set()
    for line in stdout.splitlines():
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        test = event.get("Test")
        if not test or "/" in test:
            continue
        action = event.get("Action")
        if action == "pass":
            passed.add(test)
        elif action in ("fail", "skip"):
            not_passed.add(test)
    return passed, not_passed


def main() -> int:
    if not (EMULATOR / "go.mod").is_file():
        print("metric=0")
        print(f"missing {EMULATOR / 'go.mod'}", file=sys.stderr)
        return 1
    command = [find_go(), "test", "./...", "-json"]
    try:
        proc = subprocess.run(
            command,
            cwd=EMULATOR,
            capture_output=True,
            text=True,
            timeout=TIMEOUT_SEC,
        )
    except FileNotFoundError:
        print("metric=0")
        print("go executable not found; set GO or add go to PATH", file=sys.stderr)
        return 1
    except subprocess.TimeoutExpired:
        print("metric=0")
        print(f"go test exceeded {TIMEOUT_SEC}s", file=sys.stderr)
        return 1

    passed, not_passed = parse_events(proc.stdout)
    count = len(passed.intersection(ALLOWLIST))
    print(f"metric={count}")

    missing = [name for name in ALLOWLIST if name not in passed]
    if proc.returncode != 0:
        tail = (proc.stdout + "\n" + proc.stderr).strip().splitlines()[-40:]
        print("\n".join(tail), file=sys.stderr)
        return 1
    if missing:
        detail = ", ".join(missing)
        if not_passed.intersection(ALLOWLIST):
            detail += " (failed/skipped: " + ", ".join(sorted(not_passed.intersection(ALLOWLIST))) + ")"
        print(f"allowlisted tests did not pass: {detail}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
