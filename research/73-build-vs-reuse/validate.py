#!/usr/bin/env python3
"""Validate the AJS-13 build-vs-reuse decision.

Checks, in order:
  1. REPORT.md declares exactly four options A-D.
  2. The score matrix has a weights row summing to 100 and one row per option,
     eight 1-5 scores and a stated weighted total.
  3. The weighted total is recomputed from the weights and must match the
     stated value (the report cannot hand-write a total that the rubric does
     not produce).
  4. The option with the highest recomputed total is the named recommendation.
  5. REPORT.md, DECISION.md and the report's Answer all name the same letter.

Exit 0 and print `OK options=4 recommendation=<A|B|C|D>` on success.
Exit 1 and print `FAIL: <reason>` otherwise.

Standard library only. Run: python validate.py [--verbose]
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPORT = HERE / "REPORT.md"
DECISION = HERE / "DECISION.md"

LETTERS = ("A", "B", "C", "D")
N_DIMENSIONS = 8
TOLERANCE = 0.05


def fail(msg: str) -> "NoReturn":  # type: ignore[name-defined]
    print(f"FAIL: {msg}")
    raise SystemExit(1)


def sections(text: str) -> dict[str, list[str]]:
    """Split a markdown document into `## Heading` -> body lines."""
    out: dict[str, list[str]] = {}
    current = ""
    for line in text.splitlines():
        if line.startswith("## "):
            current = line[3:].strip()
            out.setdefault(current, [])
            continue
        if current:
            out[current].append(line)
    return out


def table_rows(lines: list[str]) -> list[list[str]]:
    """Return the cells of every pipe-table row, separator rows dropped."""
    rows = []
    for line in lines:
        stripped = line.strip()
        if not stripped.startswith("|"):
            continue
        cells = [c.strip() for c in stripped.strip("|").split("|")]
        if all(re.fullmatch(r":?-{2,}:?", c) for c in cells if c):
            continue
        rows.append(cells)
    return rows


def parse_options(sec: dict[str, list[str]]) -> list[str]:
    if "Options" not in sec:
        fail("REPORT.md has no '## Options' section")
    found = []
    for line in sec["Options"]:
        m = re.match(r"^### ([A-D])\. ", line)
        if m:
            found.append(m.group(1))
    if sorted(found) != list(LETTERS):
        fail(f"expected options A-D in '## Options', found {found or 'none'}")
    return found


def parse_matrix(sec: dict[str, list[str]]) -> tuple[list[int], dict[str, tuple[list[int], float]]]:
    if "Score matrix" not in sec:
        fail("REPORT.md has no '## Score matrix' section")

    weights: list[int] | None = None
    options: dict[str, tuple[list[int], float]] = {}

    for cells in table_rows(sec["Score matrix"]):
        head = cells[0].strip("* ").lower()
        if head == "weights":
            try:
                vals = [int(c) for c in cells[1:]]
            except ValueError:
                fail(f"weights row is not all integers: {cells}")
            if len(vals) == N_DIMENSIONS + 1:
                # Trailing cell is the documented sum; it must be honest.
                if vals[-1] != sum(vals[:-1]):
                    fail(f"weights row total {vals[-1]} != sum {sum(vals[:-1])}")
                vals = vals[:-1]
            if len(vals) != N_DIMENSIONS:
                fail(f"expected {N_DIMENSIONS} weights, got {len(vals)}: {vals}")
            weights = vals
            continue
        if cells[0] in LETTERS and len(cells) >= N_DIMENSIONS + 2:
            try:
                scores = [int(c) for c in cells[1 : 1 + N_DIMENSIONS]]
                stated = float(cells[1 + N_DIMENSIONS])
            except ValueError:
                fail(f"option row {cells[0]} has non-numeric cells: {cells}")
            if cells[0] in options:
                fail(f"option {cells[0]} appears twice in the score matrix")
            options[cells[0]] = (scores, stated)

    if weights is None:
        fail("score matrix has no 'Weights' row")
    if len(weights) != N_DIMENSIONS:
        fail(f"expected {N_DIMENSIONS} weights, got {len(weights)}: {weights}")
    if sum(weights) != 100:
        fail(f"weights must sum to 100, got {sum(weights)}: {weights}")
    if any(w < 0 for w in weights):
        fail(f"negative weight in {weights}")

    if sorted(options) != list(LETTERS):
        fail(f"score matrix must score all four options, got {sorted(options)}")

    for letter, (scores, _stated) in options.items():
        if len(scores) != N_DIMENSIONS:
            fail(f"option {letter} has {len(scores)} scores, expected {N_DIMENSIONS}")
        bad = [s for s in scores if not 1 <= s <= 5]
        if bad:
            fail(f"option {letter} has scores outside 1-5: {bad}")

    return weights, options


def weighted_totals(weights: list[int], options: dict[str, tuple[list[int], float]]) -> dict[str, float]:
    return {letter: sum(w * s for w, s in zip(weights, scores)) / 5.0 for letter, (scores, _) in options.items()}


def named_recommendation(sec: dict[str, list[str]]) -> str:
    if "Recommendation" not in sec:
        fail("REPORT.md has no '## Recommendation' section")
    body = "\n".join(sec["Recommendation"])
    m = re.search(r"\*\*([A-D])\.", body)
    if not m:
        fail("'## Recommendation' does not name an option as '**X.'")
    return m.group(1)


def answer_recommendation(sec: dict[str, list[str]]) -> str:
    if "Answer" not in sec:
        fail("REPORT.md has no '## Answer' section")
    body = "\n".join(sec["Answer"])
    m = re.search(r"Recommendation:\s*\**\s*([A-D])", body)
    if not m:
        fail("'## Answer' does not state 'Recommendation: <letter>'")
    return m.group(1)


def decision_memo_recommendation() -> str:
    if not DECISION.exists():
        fail("DECISION.md is missing")
    text = DECISION.read_text(encoding="utf-8")
    m = re.search(r"Recommendation:\s*\**\s*([A-D])", text)
    if not m:
        fail("DECISION.md does not state 'Recommendation: <letter>'")
    return m.group(1)


def main() -> int:
    verbose = "--verbose" in sys.argv[1:]

    if not REPORT.exists():
        fail("REPORT.md is missing")
    sec = sections(REPORT.read_text(encoding="utf-8"))

    options = parse_options(sec)
    weights, matrix = parse_matrix(sec)
    totals = weighted_totals(weights, matrix)

    for letter, (_scores, stated) in matrix.items():
        if abs(totals[letter] - stated) > TOLERANCE:
            fail(
                f"option {letter}: stated total {stated} does not match recomputed "
                f"{totals[letter]:.1f} from weights {weights}"
            )

    winner = max(totals, key=lambda k: (totals[k], k == "D"))
    # Deterministic tie-break: if two options tie, refuse rather than guess.
    tied = [k for k in totals if abs(totals[k] - totals[winner]) <= TOLERANCE]
    if len(tied) > 1:
        fail(f"weighted totals tie between {sorted(tied)}: {totals}")

    rec = named_recommendation(sec)
    if rec != winner:
        fail(f"recommendation {rec} is not the highest-scoring option {winner} ({totals})")

    ans = answer_recommendation(sec)
    if ans != rec:
        fail(f"'## Answer' says {ans} but '## Recommendation' says {rec}")
    memo = decision_memo_recommendation()
    if memo != rec:
        fail(f"DECISION.md says {memo} but REPORT.md says {rec}")

    if verbose:
        print(f"weights={weights} sum={sum(weights)}", file=sys.stderr)
        for letter in LETTERS:
            scores, stated = matrix[letter]
            print(f"{letter}: {scores} -> {totals[letter]:.1f} (stated {stated})", file=sys.stderr)
        print(f"winner={winner}", file=sys.stderr)

    print(f"OK options={len(options)} recommendation={rec}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
