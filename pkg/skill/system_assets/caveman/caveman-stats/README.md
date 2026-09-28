# caveman-stats

Real session token receipts for forebrain. No LLM estimation.

## What it does

Reads forebrain's sqlite store and hook transcript JSONL directly and reports
actual input/output token usage plus an estimated savings versus a
non-caveman baseline. Numbers come from `fb_runs` rows and JSONL turns on
disk — the model itself does not compute or estimate them.

Each run also writes a lifetime-savings suffix file used by the statusline
badge (`⛏ 12.4k`).

## Quick start

1. Install the UserPromptSubmit hook (see `SKILL.md`).
2. In any forebrain chat, type `/caveman-stats`.

## Manual invocation

```bash
python3 scripts/caveman-stats.py \
  --transcript $FOREBRAIN_HOME/state/hook-transcripts/<session>.jsonl \
  --session-id <session>
```

## Example output

```
Caveman Stats
──────────────────────────────────
Session:  abc123
Turns:    47
──────────────────────────────────
Input tokens:          12,304
Output tokens:          3,891
──────────────────────────────────
Est. without caveman:  11,118
Est. tokens saved:      7,227 (~65%)
Est. saved (USD):       ~$0.108
Savings est. from benchmarks/ (mean per-task). Actual varies.
```

## See also

- [`SKILL.md`](./SKILL.md) — hook contract and data-source mapping
- [Caveman README](../README.md) — repo overview
