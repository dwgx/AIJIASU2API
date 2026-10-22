#!/usr/bin/env python3
"""Validator for research/30-linux-static (AJS-04).

Checks:
  1. REPORT.md exists, starts with the required title, and has all required sections.
  2. hashes.tsv has >= 2 data rows with 5 tab-separated columns and a 64-hex sha256 each,
     and contains the 4.2.3.0 binary row.
  3. strings/ajiasu.strings.txt has >= 20 non-comment lines carrying protocol-relevant tokens.
  4. If the analysed binary is reachable (env AJS_LINUX_BIN or the default temp path), its
     sha256 is recomputed and compared with the manifest.

On success prints exactly:  OK linux sha256=<64 hex> strings=<N>
Exit code 0 on success, 1 on any failure (with a FAIL line naming the check).
"""
import hashlib
import os
import re
import sys

ROOT = os.path.dirname(os.path.abspath(__file__))
REPORT = os.path.join(ROOT, 'REPORT.md')
HASHES = os.path.join(ROOT, 'hashes.tsv')
STRINGS = os.path.join(ROOT, 'strings', 'ajiasu.strings.txt')
DEFAULT_BIN = r'C:/Users/dwgx1/AppData/Local/Temp/ajs-linux-20261023/ajiasu'

TITLE = '# Linux client 4.2.3.0 \u2014 static analysis'
SECTIONS = [
    '## Answer', '## Artifacts', '## CLI and config', '## Network constants',
    '## Protocol modes', '## Comparison with Windows/OpenAJS', '## 4.3.0.1 delta',
    '## Open questions',
]
TOKENS = re.compile(
    r'(FV_|CommandCode|obscure|OPV2|/s\?s=|Sec-WebSocket|socks|lwip|/dev/net/tun|proxy|'
    r'protocol|client\.php|ClientApi|LoginSync|ajs-app|mmegg|mbclubsh|X-Sig|sys-env|'
    r'cache_dir|cache-dir|nat64|handshake|code=200|127\.0\.0\.1)', re.IGNORECASE)
HEX64 = re.compile(r'^[0-9a-f]{64}$')


def fail(msg):
    print('FAIL ' + msg)
    sys.exit(1)


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, 'rb') as f:
        for chunk in iter(lambda: f.read(1 << 20), b''):
            h.update(chunk)
    return h.hexdigest()


# 1. report
if not os.path.isfile(REPORT):
    fail('missing REPORT.md')
report = open(REPORT, encoding='utf-8').read()
first = report.splitlines()[0].strip()
if first != TITLE:
    fail('REPORT.md first line is %r, expected %r' % (first, TITLE))
missing = [s for s in SECTIONS if s not in report]
if missing:
    fail('REPORT.md missing sections: %s' % ', '.join(missing))

# 2. manifest
if not os.path.isfile(HASHES):
    fail('missing hashes.tsv')
rows = []
for n, line in enumerate(open(HASHES, encoding='utf-8'), 1):
    line = line.rstrip('\n')
    if not line.strip() or n == 1:
        continue
    cols = line.split('\t')
    if len(cols) != 5:
        fail('hashes.tsv line %d has %d columns, expected 5' % (n, len(cols)))
    if not HEX64.match(cols[2]):
        fail('hashes.tsv line %d has a malformed sha256' % n)
    rows.append(cols)
if len(rows) < 2:
    fail('hashes.tsv has %d data rows, need >= 2' % len(rows))
target = [r for r in rows if r[0].replace('\\', '/').endswith('/ajiasu') and r[4] == '4.2.3.0']
if not target:
    fail('hashes.tsv has no row for the analysed 4.2.3.0 ajiasu binary')
linux_sha = target[0][2]

# 3. strings
if not os.path.isfile(STRINGS):
    fail('missing strings/ajiasu.strings.txt')
count = 0
for line in open(STRINGS, encoding='utf-8'):
    s = line.strip()
    if not s or s.startswith('#'):
        continue
    if TOKENS.search(s):
        count += 1
if count < 20:
    fail('strings/ajiasu.strings.txt has %d protocol-relevant lines, need >= 20' % count)

# 4. recompute hash when the binary is available
binary = os.environ.get('AJS_LINUX_BIN', DEFAULT_BIN)
if os.path.isfile(binary):
    actual = sha256_file(binary)
    if actual != linux_sha:
        fail('binary sha256 mismatch: %s != manifest %s' % (actual, linux_sha))

print('OK linux sha256=%s strings=%d' % (linux_sha, count))
