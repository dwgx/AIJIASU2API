#!/usr/bin/env python3
"""Validate the AJS-11 competitive-search artifacts.

Checks, all offline and deterministic:

* ``REPORT.md`` contains the seven required sections.
* Its candidate matrix has >= 12 data rows and the required columns.
* ``## Top 3`` names exactly three distinct ``owner/repo`` entries, all present
  in the candidate matrix.
* ``sources.tsv`` has the required header, >= 20 rows, unique ids, non-empty
  url/date/quote.
* ``claims.tsv`` has the required header, >= 15 rows, and every referenced
  source id exists in ``sources.tsv`` (except ``none`` on inference/unknown
  rows).
* ``searched.tsv`` has >= 30 rows and its ``assessed=yes`` count equals the
  number of candidate-matrix rows.

On success prints ``OK candidates=<N> top3=3 sources=<M>`` and exits 0.
"""

from __future__ import annotations

import csv
import re
import sys
from pathlib import Path

SLICE = Path(__file__).resolve().parent
REPORT = SLICE / "REPORT.md"
SOURCES = SLICE / "sources.tsv"
CLAIMS = SLICE / "claims.tsv"
SEARCHED = SLICE / "searched.tsv"

REQUIRED_SECTIONS = [
    "Answer",
    "Search log",
    "Candidate matrix",
    "Top 3",
    "Feature matrix",
    "Recommendation",
    "Sources",
]
MATRIX_COLUMNS = [
    "repo",
    "stars",
    "license",
    "stack",
    "data plane",
    "auth model",
    "sticky/lease",
    "panel",
    "cross-platform",
    "fit",
    "verdict",
]
MIN_CANDIDATES = 12
MIN_SOURCES = 20
MIN_CLAIMS = 15
MIN_SEARCHED = 30
GRADES = {"A", "B", "[INFERENCE]", "[UNKNOWN]"}


class ValidationError(Exception):
    pass


def read_text(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except FileNotFoundError as exc:
        raise ValidationError(f"missing file: {path.name}") from exc


def read_tsv(path: Path) -> tuple[list[str], list[list[str]]]:
    text = read_text(path)
    rows = list(csv.reader(text.splitlines(), delimiter="\t"))
    if not rows:
        raise ValidationError(f"{path.name} is empty")
    header, body = rows[0], [row for row in rows[1:] if any(cell.strip() for cell in row)]
    for index, row in enumerate(body, start=2):
        if len(row) != len(header):
            raise ValidationError(
                f"{path.name}:{index} has {len(row)} fields, expected {len(header)}"
            )
    return header, body


def section_lines(text: str, name: str) -> list[str]:
    lines = text.splitlines()
    start = None
    for index, line in enumerate(lines):
        if line.strip() == f"## {name}":
            start = index + 1
            break
    if start is None:
        raise ValidationError(f"REPORT.md has no '## {name}' section")
    out: list[str] = []
    for line in lines[start:]:
        if line.startswith("## "):
            break
        out.append(line)
    return out


def sections_present(text: str) -> list[str]:
    headings = {
        match.group(1).strip()
        for match in re.finditer(r"^##\s+(.+?)\s*$", text, re.MULTILINE)
    }
    return [name for name in REQUIRED_SECTIONS if name in headings]


def parse_matrix(text: str) -> list[list[str]]:
    rows: list[list[str]] = []
    seen_header = False
    for line in section_lines(text, "Candidate matrix"):
        stripped = line.strip()
        if not stripped.startswith("|"):
            continue
        cells = [cell.strip() for cell in stripped.strip("|").split("|")]
        if all(re.fullmatch(r":?-{3,}:?", cell) for cell in cells if cell):
            continue
        if not seen_header:
            seen_header = True
            continue
        if any(cells):
            rows.append(cells)
    if not rows:
        raise ValidationError("candidate matrix has no data rows")
    return rows


def parse_top3(text: str) -> list[str]:
    repos: list[str] = []
    for line in section_lines(text, "Top 3"):
        if not re.match(r"^\s*[123]\.\s", line):
            continue
        match = re.search(r"`([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)`", line)
        if match:
            repos.append(match.group(1))
    return repos


def main() -> int:
    report = read_text(REPORT)

    missing = [name for name in REQUIRED_SECTIONS if name not in sections_present(report)]
    if missing:
        raise ValidationError(f"REPORT.md missing sections: {missing}")

    matrix = parse_matrix(report)
    if len(matrix) < MIN_CANDIDATES:
        raise ValidationError(
            f"candidate matrix has {len(matrix)} rows, need {MIN_CANDIDATES}"
        )
    header_cells = [cell.lower() for cell in section_lines(report, "Candidate matrix") if cell.strip().startswith("|")][0]
    header_cells = [cell.strip().lower() for cell in header_cells.strip().strip("|").split("|")]
    for column in MATRIX_COLUMNS:
        if column not in header_cells:
            raise ValidationError(f"candidate matrix missing column: {column}")
    matrix_repos = {row[0].strip("`* ") for row in matrix}

    top3 = parse_top3(report)
    if len(top3) != 3:
        raise ValidationError(f"'## Top 3' lists {len(top3)} repos, need 3")
    if len(set(top3)) != 3:
        raise ValidationError(f"'## Top 3' repos are not distinct: {top3}")
    for repo in top3:
        if repo not in matrix_repos:
            raise ValidationError(f"Top 3 repo {repo} is absent from the candidate matrix")

    source_header, sources = read_tsv(SOURCES)
    if source_header != ["source_id", "url", "date", "quote"]:
        raise ValidationError(f"sources.tsv header is {source_header}")
    if len(sources) < MIN_SOURCES:
        raise ValidationError(f"sources.tsv has {len(sources)} rows, need {MIN_SOURCES}")
    source_ids = [row[0] for row in sources]
    if len(set(source_ids)) != len(source_ids):
        raise ValidationError("sources.tsv has duplicate source ids")
    for row in sources:
        if not row[1].startswith(("http://", "https://", "D:/")):
            raise ValidationError(f"source {row[0]} has a non-URL location: {row[1]}")
        if not row[2] or not row[3]:
            raise ValidationError(f"source {row[0]} has an empty date or quote")

    claim_header, claims = read_tsv(CLAIMS)
    if claim_header != ["claim_id", "claim", "source_ids", "grade"]:
        raise ValidationError(f"claims.tsv header is {claim_header}")
    if len(claims) < MIN_CLAIMS:
        raise ValidationError(f"claims.tsv has {len(claims)} rows, need {MIN_CLAIMS}")
    known = set(source_ids)
    for row in claims:
        claim_id, _claim, refs, grade = row
        if grade not in GRADES:
            raise ValidationError(f"{claim_id} has an unknown grade: {grade}")
        tokens = [token.strip() for token in refs.split(";") if token.strip()]
        if not tokens:
            raise ValidationError(f"{claim_id} has no source reference")
        for token in tokens:
            if token == "none":
                if grade not in {"[INFERENCE]", "[UNKNOWN]"}:
                    raise ValidationError(f"{claim_id} cites no source but grade is {grade}")
                continue
            if token not in known:
                raise ValidationError(f"{claim_id} references unknown source {token}")

    searched_header, searched = read_tsv(SEARCHED)
    if searched_header != ["repo", "source", "assessed", "note"]:
        raise ValidationError(f"searched.tsv header is {searched_header}")
    if len(searched) < MIN_SEARCHED:
        raise ValidationError(f"searched.tsv has {len(searched)} rows, need {MIN_SEARCHED}")
    assessed = sum(1 for row in searched if row[2] == "yes")
    if assessed != len(matrix):
        raise ValidationError(
            f"searched.tsv marks {assessed} assessed, candidate matrix has {len(matrix)}"
        )

    print(f"OK candidates={len(matrix)} top3=3 sources={len(sources)}")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except ValidationError as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        sys.exit(1)
