#!/usr/bin/env python3
"""Extract rodata string immediates referenced inside an address range of dis.txt."""
import re
import sys

BIN = sys.argv[1]
DIS = sys.argv[2]
LO = int(sys.argv[3], 16)
HI = int(sys.argv[4], 16)
data = open(BIN, "rb").read()
seen = set()
for line in open(DIS, encoding="utf-8", errors="replace"):
    m = re.match(r"\s+([0-9a-f]+):", line)
    if not m:
        continue
    a = int(m.group(1), 16)
    if not (LO <= a <= HI):
        continue
    for imm in re.findall(r"\$0x(6[0-9a-f]{5})", line):
        v = int(imm, 16)
        if v in seen:
            continue
        seen.add(v)
        s = data[v - 0x400000 : v - 0x400000 + 80].split(b"\0")[0]
        if len(s) > 1 and all(32 <= c < 127 for c in s):
            print(f"{a:#08x} {v:#08x} {s.decode()!r}")
