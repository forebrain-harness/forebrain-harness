---
name: caveman-stats
description: >
  Show real token usage and an estimated savings figure for the current
  caveman session. Reads forebrain's sqlite store (fb_runs) and the hook
  transcript JSONL — no LLM estimation. Triggered by /caveman-stats when
  the UserPromptSubmit hook (scripts/caveman-mode-tracker.py) is installed
  in ~/.forebrain/forebrain.yaml.
---

# caveman-stats

## How it runs

forebrain fires the `UserPromptSubmit` hook on every user message and passes
`{prompt, session_id, transcript_path, cwd}` to it. `scripts/caveman-mode-tracker.py`
intercepts `/caveman-stats`, runs `scripts/caveman-stats.py --transcript
<transcript_path> --session-id <session_id>`, and emits
`{"decision":"block","reason":<stats>}` so forebrain renders the formatted
banner directly without an LLM round-trip.

When no hook is configured, the user can still run the stats script by
hand: `python3 scripts/caveman-stats.py --session-id <id>`.

## Data sources

| Source | Used for |
|---|---|
| `$FOREBRAIN_HOME/state/forebrain.state.sqlite` table `fb_runs` (sum of `usage_prompt_tokens`, `usage_completion_tokens` per `session_id`) | input/output token totals |
| `$FOREBRAIN_HOME/state/hook-transcripts/<id>.jsonl` (written by `hooks.WriteSessionTranscriptArtifact`) | assistant turn count |
| `$FOREBRAIN_HOME/state/.caveman-active` | active mode (lite/full/ultra/wenyan*) |
| `$FOREBRAIN_HOME/state/.caveman-history.jsonl` | lifetime aggregation across sessions |

Savings estimate uses the benchmarked ratio `0.65` for mode `full`; other
modes show no estimate until benchmarked.

## Install the hook

Append to `$FOREBRAIN_HOME/forebrain.yaml`:

```yaml
hooks:
  UserPromptSubmit:
    - matcher: "*"
      hooks:
        - type: command
          command: python3 $FOREBRAIN_HOME/skills/.system/caveman/caveman-stats/scripts/caveman-mode-tracker.py
          timeout: 5
```

After install, `/caveman-stats` prints the banner inline; `/caveman <mode>`
toggles the flag; natural-language phrases ("activate caveman", "stop
caveman", "normal mode") also work.

## Flags

- `--share` — single-line tweet-sized summary
- `--all` — lifetime totals from history log
- `--since 7d` / `--since 24h` — lifetime totals over window
