#!/usr/bin/env python3
"""AJS-07 DONE WHEN check.

Verifies that REPORT.md contains the required sections and that captures/ holds
at least one artifact, then prints:
    OK captures=<N> sig=<found|unknown>
Exit code 0 on success.
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.abspath(__file__))
REPORT = os.path.join(ROOT, "REPORT.md")
CAPTURES = os.path.join(ROOT, "captures")
REQUIRED = ["## OPV2 and frame", "## Captures"]


def main():
    errors = []
    if not os.path.isfile(REPORT):
        errors.append(f"missing {REPORT}")
        text = ""
    else:
        with open(REPORT, encoding="utf-8") as f:
            text = f.read()
    for section in REQUIRED:
        if section not in text:
            errors.append(f"REPORT.md missing section: {section}")

    files = []
    if os.path.isdir(CAPTURES):
        files = [n for n in os.listdir(CAPTURES) if os.path.isfile(os.path.join(CAPTURES, n))]
    if len(files) < 1:
        errors.append(f"captures/ has {len(files)} files, need >=1")

    m = re.search(r"SIG_STATUS:\s*(found|unknown)", text)
    sig = m.group(1) if m else "unknown"

    if errors:
        for e in errors:
            print(f"FAIL {e}", file=sys.stderr)
        return 1
    print(f"OK captures={len(files)} sig={sig}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
