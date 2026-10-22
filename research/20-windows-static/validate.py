#!/usr/bin/env python3
"""Validate the AJS-03 Windows static inventory deliverables.

Checks
  1. hashes.tsv exists, has the exact 7-column header, and exactly 37 data rows.
  2. Every row has 7 fields, a 64-hex sha256, and a non-empty path/type/arch/signature.
  3. INVENTORY.md exists, starts with the required H1, and contains all required sections.
  4. The four other deliverables exist and are non-empty.
  5. If the extraction directory is still present, the 37 recorded paths match it exactly
     (set equality, ignoring the _work scratch directory).

Exit 0 and print "OK files=37 hashes=37" on success; exit 1 with a reason otherwise.
"""

import os
import re
import sys

ROOT = os.path.dirname(os.path.abspath(__file__))
EXTRACT = r"C:/Users/dwgx1/AppData/Local/Temp/ajs-win-20261023"
EXPECTED = 37
HEADER = ["path", "size", "sha256", "type", "arch", "signature", "version"]
SECTIONS = [
    "## Component map",
    "## File inventory",
    "## AJiaSu.exe",
    "## TAP driver",
    "## Installer",
    "## Strings of interest",
    "## Open questions",
]
H1 = "# Windows client 4.9.6.0 — static inventory"

errors = []


def fail(msg):
    errors.append(msg)


# --- 1/2. hashes.tsv -------------------------------------------------------
tsv = os.path.join(ROOT, "hashes.tsv")
rows = []
if not os.path.isfile(tsv):
    fail("hashes.tsv missing")
else:
    with open(tsv, encoding="utf-8") as f:
        lines = [ln.rstrip("\n") for ln in f if ln.strip()]
    if not lines:
        fail("hashes.tsv empty")
    else:
        if lines[0].split("\t") != HEADER:
            fail("hashes.tsv header is %r" % lines[0])
        rows = lines[1:]
        if len(rows) != EXPECTED:
            fail("hashes.tsv has %d data rows, expected %d" % (len(rows), EXPECTED))
        seen = set()
        for i, ln in enumerate(rows, 1):
            parts = ln.split("\t")
            if len(parts) != 7:
                fail("hashes.tsv row %d has %d fields" % (i, len(parts)))
                continue
            path, size, sha, typ, arch, sig, ver = parts
            if path in seen:
                fail("hashes.tsv duplicate path %r" % path)
            seen.add(path)
            if not re.fullmatch(r"[0-9a-f]{64}", sha):
                fail("hashes.tsv row %d bad sha256" % i)
            if not size.isdigit():
                fail("hashes.tsv row %d bad size" % i)
            if not (path and typ and arch and sig):
                fail("hashes.tsv row %d has an empty field" % i)

# --- 3. INVENTORY.md -------------------------------------------------------
inv = os.path.join(ROOT, "INVENTORY.md")
if not os.path.isfile(inv):
    fail("INVENTORY.md missing")
else:
    with open(inv, encoding="utf-8") as f:
        text = f.read()
    first = text.split("\n", 1)[0].strip()
    if first != H1:
        fail("INVENTORY.md first line is %r" % first)
    for s in SECTIONS:
        if s not in text:
            fail("INVENTORY.md missing section %r" % s)
    if text.count("`") % 2:
        fail("INVENTORY.md has unbalanced backticks")

# --- 4. other deliverables -------------------------------------------------
for rel in ("strings/AJiaSu.strings.txt", "strings/sysproxy.strings.txt",
            "strings/tap.strings.txt", "driver/TAP.md", "installer/NSIS.md"):
    p = os.path.join(ROOT, rel)
    if not os.path.isfile(p) or os.path.getsize(p) == 0:
        fail("missing or empty: %s" % rel)

# --- 5. cross-check against the extraction (optional) ----------------------
files = EXPECTED
if os.path.isdir(EXTRACT):
    disk = set()
    for dirpath, dirnames, filenames in os.walk(EXTRACT):
        dirnames[:] = [d for d in dirnames if d != "_work"]
        for fn in filenames:
            full = os.path.join(dirpath, fn)
            disk.add(os.path.relpath(full, EXTRACT).replace("\\", "/"))
    files = len(disk)
    recorded = {ln.split("\t")[0] for ln in rows if ln.count("\t") == 6}
    if disk != recorded:
        fail("path set differs from extraction: only-on-disk=%r only-in-tsv=%r"
             % (sorted(disk - recorded)[:5], sorted(recorded - disk)[:5]))

if errors:
    for e in errors:
        print("FAIL: " + e, file=sys.stderr)
    sys.exit(1)

print("OK files=%d hashes=%d" % (files, len(rows)))
sys.exit(0)
