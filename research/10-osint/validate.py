#!/usr/bin/env python3
"""Validate research/10-osint/sources.tsv and claims.tsv.

Checks column headers, row counts, field shapes, quote length, grade domain and
that every claim references known source ids (or an explicit [INFERENCE]/[UNKNOWN]).

Prints "OK sources=N claims=M" and exits 0 when every check passes.
"""

import csv
import re
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
MIN_SOURCES = 20
MIN_CLAIMS = 15
MAX_QUOTE = 200

SOURCE_HEADER = ["source_id", "url", "date", "quote"]
CLAIM_HEADER = ["claim_id", "claim", "source_ids", "grade"]

ID_RE = re.compile(r"^[SC]\d+$")
DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
GRADES = {"A", "B", "C"}
OPEN_MARKERS = ("[INFERENCE]", "[UNKNOWN]")

errors = []


def fail(msg):
    errors.append(msg)


def read_tsv(path, expected_header):
    if not path.exists():
        fail(f"{path.name}: missing")
        return []
    with path.open("r", encoding="utf-8-sig", newline="") as fh:
        rows = list(csv.reader(fh, delimiter="\t", quoting=csv.QUOTE_NONE))
    if not rows:
        fail(f"{path.name}: empty")
        return []
    header = rows[0]
    if header != expected_header:
        fail(f"{path.name}: header {header!r} != {expected_header!r}")
    data = []
    for lineno, row in enumerate(rows[1:], start=2):
        if not any(cell.strip() for cell in row):
            continue
        if len(row) != len(expected_header):
            fail(f"{path.name}:{lineno}: {len(row)} fields, expected {len(expected_header)}")
            continue
        data.append((lineno, row))
    return data


def main():
    sources = read_tsv(HERE / "sources.tsv", SOURCE_HEADER)
    claims = read_tsv(HERE / "claims.tsv", CLAIM_HEADER)

    if len(sources) < MIN_SOURCES:
        fail(f"sources.tsv: {len(sources)} data rows, need >= {MIN_SOURCES}")
    if len(claims) < MIN_CLAIMS:
        fail(f"claims.tsv: {len(claims)} data rows, need >= {MIN_CLAIMS}")

    source_ids = set()
    for lineno, (sid, url, date, quote) in sources:
        if not ID_RE.match(sid):
            fail(f"sources.tsv:{lineno}: bad source_id {sid!r}")
        if sid in source_ids:
            fail(f"sources.tsv:{lineno}: duplicate source_id {sid}")
        source_ids.add(sid)
        if not url.strip():
            fail(f"sources.tsv:{lineno}: empty url")
        if not DATE_RE.match(date.strip()):
            fail(f"sources.tsv:{lineno}: date {date!r} is not YYYY-MM-DD")
        if not quote.strip():
            fail(f"sources.tsv:{lineno}: empty quote")
        if len(quote) > MAX_QUOTE:
            fail(f"sources.tsv:{lineno}: quote {len(quote)} chars > {MAX_QUOTE}")

    claim_ids = set()
    for lineno, (cid, claim, refs, grade) in claims:
        if not ID_RE.match(cid):
            fail(f"claims.tsv:{lineno}: bad claim_id {cid!r}")
        if cid in claim_ids:
            fail(f"claims.tsv:{lineno}: duplicate claim_id {cid}")
        claim_ids.add(cid)
        if not claim.strip():
            fail(f"claims.tsv:{lineno}: empty claim")
        if grade.strip() not in GRADES:
            fail(f"claims.tsv:{lineno}: grade {grade!r} not in A/B/C")
        if not refs.strip():
            fail(f"claims.tsv:{lineno}: empty source_ids")
            continue
        known = False
        for ref in [r.strip() for r in refs.split(";") if r.strip()]:
            if ref in source_ids:
                known = True
            elif ref not in OPEN_MARKERS:
                fail(f"claims.tsv:{lineno}: unknown source id {ref!r}")
        if not known and not any(m in refs for m in OPEN_MARKERS):
            fail(f"claims.tsv:{lineno}: no resolvable source id and no {OPEN_MARKERS} marker")

    if errors:
        for err in errors:
            print(f"FAIL: {err}", file=sys.stderr)
        return 1

    print(f"OK sources={len(sources)} claims={len(claims)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
