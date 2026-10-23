#!/usr/bin/env python3
"""Validate the AJS-10 panel design artifacts.

Checks, all offline and deterministic:

* ``research/70-panel/REPORT.md`` contains at least 8 of the required sections.
* Its candidate matrix has at least 6 data rows.
* ``LICENSE`` starts with ``MIT License``.
* ``README.md`` and ``PLAN.md`` mention MIT and the self-use/authorized boundary.
* ``docs/PANEL.md`` exists and is non-empty.

On success prints ``OK candidates=<N> sections=<M> license=MIT`` and exits 0.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
REPORT = ROOT / "research" / "70-panel" / "REPORT.md"
LICENSE = ROOT / "LICENSE"
README = ROOT / "README.md"
PLAN = ROOT / "PLAN.md"
PANEL = ROOT / "docs" / "PANEL.md"

REQUIRED_SECTIONS = [
    "Answer",
    "Candidate matrix",
    "Reuse decision",
    "Architecture",
    "Post-login live flow",
    "Multi-account allocation",
    "Panel feature matrix",
    "Deployment",
    "License and boundary",
    "Open questions",
]
MIN_SECTIONS = 8
MIN_CANDIDATES = 6


class ValidationError(Exception):
    pass


def read_text(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except FileNotFoundError as exc:
        raise ValidationError(f"missing file: {path.relative_to(ROOT)}") from exc


def sections_present(text: str) -> list[str]:
    headings = {
        match.group(1).strip()
        for match in re.finditer(r"^##\s+(.+?)\s*$", text, re.MULTILINE)
    }
    return [name for name in REQUIRED_SECTIONS if name in headings]


def matrix_rows(text: str) -> int:
    lines = text.splitlines()
    start = None
    for index, line in enumerate(lines):
        if line.strip() == "## Candidate matrix":
            start = index + 1
            break
    if start is None:
        raise ValidationError("REPORT.md has no '## Candidate matrix' section")

    rows = 0
    seen_header = False
    for line in lines[start:]:
        stripped = line.strip()
        if stripped.startswith("## "):
            break
        if not stripped.startswith("|"):
            continue
        cells = [cell.strip() for cell in stripped.strip("|").split("|")]
        if all(re.fullmatch(r":?-{3,}:?", cell) for cell in cells if cell):
            continue
        if not seen_header:
            seen_header = True
            continue
        if any(cells):
            rows += 1
    return rows


def require_mentions(text: str, name: str) -> None:
    lowered = text.lower()
    if "mit" not in lowered:
        raise ValidationError(f"{name} does not mention MIT")
    if "self-use" not in lowered and "authorized" not in lowered:
        raise ValidationError(f"{name} does not mention the self-use/authorized boundary")


def main() -> int:
    report = read_text(REPORT)
    license_text = read_text(LICENSE)
    readme = read_text(README)
    plan = read_text(PLAN)

    if not license_text.startswith("MIT License"):
        raise ValidationError("LICENSE does not start with 'MIT License'")

    sections = sections_present(report)
    if len(sections) < MIN_SECTIONS:
        missing = [name for name in REQUIRED_SECTIONS if name not in sections]
        raise ValidationError(
            f"REPORT.md has {len(sections)} required sections, need {MIN_SECTIONS}; missing {missing}"
        )

    candidates = matrix_rows(report)
    if candidates < MIN_CANDIDATES:
        raise ValidationError(
            f"candidate matrix has {candidates} data rows, need {MIN_CANDIDATES}"
        )

    require_mentions(readme, "README.md")
    require_mentions(plan, "PLAN.md")

    if not PANEL.is_file() or PANEL.stat().st_size == 0:
        raise ValidationError("docs/PANEL.md is missing or empty")

    print(f"OK candidates={candidates} sections={len(sections)} license=MIT")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except ValidationError as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        sys.exit(1)
