#!/usr/bin/env python3
"""Acceptance check for the Windows 4.9.6.0 FV transport slice.

Checks:
  * REPORT.md contains the "## Call flow" section
  * FUNCTIONS.md contains >= 5 data rows of the form "| <n> | 0xADDR | ..."
  * pseudocode/ contains >= 1 file

Prints: OK functions=<N> callflow=1 pseudocode=<M>
Exit code 0 on success, 1 on any failure.
"""
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
REPORT = os.path.join(HERE, 'REPORT.md')
FUNCS = os.path.join(HERE, 'FUNCTIONS.md')
PSEUDO = os.path.join(HERE, 'pseudocode')

ROW_RE = re.compile(r'^\|\s*\d+\s*\|\s*0x[0-9a-fA-F]{6,8}\s*\|')


def fail(msg):
    print('FAIL ' + msg)
    return 1


def main():
    if not os.path.isfile(REPORT):
        return fail('missing REPORT.md')
    report = open(REPORT, encoding='utf-8').read()
    if '## Call flow' not in report:
        return fail('REPORT.md has no "## Call flow" section')
    for section in ('## Answer', '## OPV2 framing', '## product.dat', '## Evidence'):
        if section not in report:
            return fail('REPORT.md has no "%s" section' % section)

    if not os.path.isfile(FUNCS):
        return fail('missing FUNCTIONS.md')
    rows = [ln for ln in open(FUNCS, encoding='utf-8') if ROW_RE.match(ln)]
    if len(rows) < 5:
        return fail('FUNCTIONS.md has %d data rows, need >= 5' % len(rows))

    if not os.path.isdir(PSEUDO):
        return fail('missing pseudocode/')
    files = [f for f in os.listdir(PSEUDO) if os.path.isfile(os.path.join(PSEUDO, f))]
    if len(files) < 1:
        return fail('pseudocode/ is empty')

    print('OK functions=%d callflow=1 pseudocode=%d' % (len(rows), len(files)))
    return 0


if __name__ == '__main__':
    sys.exit(main())
