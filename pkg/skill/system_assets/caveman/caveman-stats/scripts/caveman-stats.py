#!/usr/bin/env python3
"""caveman-stats — read forebrain session state, print real token usage and an
estimated savings figure for caveman mode.

Usage:
    caveman-stats.py [--transcript <path>] [--session-id <id>] [--share]
                     [--all] [--since <Nd|Nh>]

Token totals come from forebrain's sqlite store ($FOREBRAIN_HOME/state/forebrain.state.sqlite,
table fb_runs) joined on session_id. Turn count comes from the transcript JSONL
written by hooks.WriteSessionTranscriptArtifact ($FOREBRAIN_HOME/state/hook-transcripts/<id>.jsonl).
Mode flag at $FOREBRAIN_HOME/state/.caveman-active drives the savings estimate.
"""
from __future__ import annotations

import json
import os
import sqlite3
import sys
import time
from pathlib import Path

COMPRESSION = {"full": 0.65}

MODEL_OUTPUT_PRICE_PER_M = [
    ("claude-opus-4", 75.00),
    ("claude-sonnet-4", 15.00),
    ("claude-haiku-4", 4.00),
    ("claude-3-5-sonnet", 15.00),
    ("claude-3-5-haiku", 4.00),
    ("claude-3-opus", 75.00),
]

VALID_MODES = {"lite", "full", "ultra", "wenyan", "wenyan-lite", "wenyan-full", "wenyan-ultra"}


def forebrain_home() -> Path:
    return Path(os.environ.get("FOREBRAIN_HOME") or (Path.home() / ".forebrain"))


def price_for_model(model: str | None) -> float | None:
    if not model:
        return None
    for prefix, price in MODEL_OUTPUT_PRICE_PER_M:
        if model.startswith(prefix):
            return price
    return None


def format_usd(amount: float) -> str:
    if amount >= 1:
        return f"${amount:.2f}"
    if amount >= 0.01:
        return f"${amount:.3f}"
    return f"${amount:.4f}"


def read_mode_flag(home: Path) -> str | None:
    p = home / "state" / ".caveman-active"
    try:
        if p.is_symlink():
            return None
        raw = p.read_text(encoding="utf-8").strip()
    except OSError:
        return None
    if raw in VALID_MODES:
        return raw
    return None


def parse_transcript(path: str | None) -> tuple[int, int]:
    """Return (turn_count, user_turn_count). turn_count counts assistant turns."""
    if not path:
        return 0, 0
    try:
        raw = Path(path).read_text(encoding="utf-8")
    except OSError:
        return 0, 0
    assistant = 0
    user = 0
    for line in raw.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            entry = json.loads(line)
        except json.JSONDecodeError:
            continue
        role = entry.get("role")
        if role == "assistant":
            assistant += 1
        elif role == "user":
            user += 1
    return assistant, user


def query_session_usage(home: Path, session_id: str) -> tuple[int, int]:
    """Sum prompt+completion tokens across all runs for this session."""
    if not session_id:
        return 0, 0
    db_path = home / "memory" / "forebrain.state.sqlite"
    if not db_path.exists():
        return 0, 0
    try:
        with sqlite3.connect(f"file:{db_path}?mode=ro", uri=True, timeout=2.0) as conn:
            cur = conn.execute(
                "SELECT IFNULL(SUM(usage_prompt_tokens),0), IFNULL(SUM(usage_completion_tokens),0) "
                "FROM fb_runs WHERE session_id = ?",
                (session_id,),
            )
            row = cur.fetchone() or (0, 0)
            return int(row[0] or 0), int(row[1] or 0)
    except sqlite3.Error:
        return 0, 0


def query_last_model(home: Path, session_id: str) -> str | None:
    """Best-effort model lookup — not stored on fb_runs, fallback to env."""
    return os.environ.get("FOREBRAIN_MODEL") or None


def derive_savings(output_tokens: int, mode: str | None, model: str | None) -> tuple[int, float, int]:
    ratio = COMPRESSION.get(mode) if mode else None
    price = price_for_model(model)
    if ratio is None or output_tokens <= 0:
        return 0, 0.0, 0
    est_normal = round(output_tokens / (1 - ratio))
    est_saved = est_normal - output_tokens
    est_usd = (est_saved / 1_000_000) * price if price is not None else 0.0
    return est_saved, est_usd, est_normal


def humanize(n: int) -> str:
    if n <= 0:
        return "0"
    if n >= 1_000_000:
        return f"{n/1_000_000:.1f}M"
    if n >= 1_000:
        return f"{n/1_000:.1f}k"
    return str(int(n))


def parse_duration(spec: str | None) -> int | None:
    if not spec:
        return None
    spec = spec.strip()
    if len(spec) < 2 or not spec[:-1].isdigit() or spec[-1] not in ("h", "d"):
        return None
    n = int(spec[:-1])
    return n * 86_400_000 if spec[-1] == "d" else n * 3_600_000


def append_history(home: Path, record: dict) -> None:
    p = home / "state" / ".caveman-history.jsonl"
    try:
        p.parent.mkdir(parents=True, exist_ok=True)
        with p.open("a", encoding="utf-8") as f:
            f.write(json.dumps(record, separators=(",", ":")) + "\n")
    except OSError:
        pass


def write_statusline_suffix(home: Path, est_saved_tokens: int) -> None:
    p = home / "state" / ".caveman-statusline-suffix"
    try:
        p.parent.mkdir(parents=True, exist_ok=True)
        if p.is_symlink():
            return
        suffix = f"⛏ {humanize(est_saved_tokens)}" if est_saved_tokens > 0 else ""
        p.write_text(suffix, encoding="utf-8")
    except OSError:
        pass


def aggregate_history(home: Path, since_ms: int | None) -> dict:
    p = home / "state" / ".caveman-history.jsonl"
    if not p.exists():
        return {"sessions": 0, "output_tokens": 0, "est_saved_tokens": 0, "est_saved_usd": 0.0}
    cutoff = (int(time.time() * 1000) - since_ms) if since_ms else None
    latest: dict[str, dict] = {}
    try:
        with p.open("r", encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    e = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if cutoff is not None and int(e.get("ts", 0)) < cutoff:
                    continue
                sid = e.get("session_id") or "_"
                prev = latest.get(sid)
                if prev is None or int(e.get("ts", 0)) >= int(prev.get("ts", 0)):
                    latest[sid] = e
    except OSError:
        return {"sessions": 0, "output_tokens": 0, "est_saved_tokens": 0, "est_saved_usd": 0.0}
    out = {"sessions": len(latest), "output_tokens": 0, "est_saved_tokens": 0, "est_saved_usd": 0.0}
    for e in latest.values():
        out["output_tokens"] += int(e.get("output_tokens", 0) or 0)
        out["est_saved_tokens"] += int(e.get("est_saved_tokens", 0) or 0)
        out["est_saved_usd"] += float(e.get("est_saved_usd", 0) or 0)
    return out


def format_stats(turns: int, input_tokens: int, output_tokens: int,
                 mode: str | None, model: str | None, session_id: str) -> str:
    sep = "──────────────────────────────────"
    if turns == 0 and input_tokens == 0 and output_tokens == 0:
        return (f"\nCaveman Stats\n{sep}\n"
                f"No conversation usage yet — stats available after first response.\n{sep}\n")
    est_saved, est_usd, est_normal = derive_savings(output_tokens, mode, model)
    lines = [
        "",
        "Caveman Stats",
        sep,
        f"Session:  {session_id or '(unknown)'}",
        f"Turns:    {turns}",
        sep,
        f"Input tokens:          {input_tokens:,}",
        f"Output tokens:         {output_tokens:,}",
        sep,
    ]
    if est_saved > 0:
        lines.append(f"Est. without caveman:  {est_normal:,}")
        ratio = COMPRESSION.get(mode or "", 0)
        lines.append(f"Est. tokens saved:     {est_saved:,} (~{round(ratio*100)}%)")
        if est_usd > 0:
            lines.append(f"Est. saved (USD):      ~{format_usd(est_usd)}")
        lines.append("Savings est. from benchmarks/ (mean per-task). Actual varies.")
    elif mode and mode != "off":
        lines.append(f"No savings estimate for '{mode}' mode — only 'full' has benchmark data.")
    else:
        lines.append("Caveman not active this session.")
    lines.append("")
    return "\n".join(lines) + "\n"


def format_share(turns: int, output_tokens: int, mode: str | None, model: str | None) -> str:
    if turns == 0:
        return "🪨 caveman armed but no turns yet — caveman.sh"
    est_saved, est_usd, _ = derive_savings(output_tokens, mode, model)
    if est_saved > 0:
        usd = f" (~{format_usd(est_usd)})" if est_usd > 0 else ""
        return f"🪨 Saved {est_saved:,} output tokens{usd} across {turns} turns this session — caveman.sh"
    return f"🪨 {turns} turns, {output_tokens:,} output tokens this session — caveman.sh"


def format_history(agg: dict, since: str | None) -> str:
    sep = "──────────────────────────────────"
    window = f" (last {since})" if since else ""
    if agg["sessions"] == 0:
        return (f"\nCaveman Stats — Lifetime{window}\n{sep}\n"
                "No sessions logged yet — run /caveman-stats inside any session.\n"
                f"{sep}\n")
    usd_line = f"Est. saved (USD):      ~{format_usd(agg['est_saved_usd'])}\n" if agg["est_saved_usd"] > 0 else ""
    return (
        f"\nCaveman Stats — Lifetime{window}\n{sep}\n"
        f"Sessions:   {agg['sessions']:,}\n{sep}\n"
        f"Output tokens:         {agg['output_tokens']:,}\n"
        f"Est. tokens saved:     {agg['est_saved_tokens']:,}\n"
        f"{usd_line}{sep}\n"
    )


def parse_args(argv: list[str]) -> dict:
    out = {"transcript": None, "session_id": "", "share": False, "all": False, "since": None}
    i = 0
    while i < len(argv):
        a = argv[i]
        if a in ("--transcript", "--session-file") and i + 1 < len(argv):
            out["transcript"] = argv[i + 1]
            i += 2
            continue
        if a == "--session-id" and i + 1 < len(argv):
            out["session_id"] = argv[i + 1]
            i += 2
            continue
        if a == "--since" and i + 1 < len(argv):
            out["since"] = argv[i + 1]
            i += 2
            continue
        if a == "--share":
            out["share"] = True
        elif a == "--all":
            out["all"] = True
        i += 1
    return out


def main() -> int:
    args = parse_args(sys.argv[1:])
    home = forebrain_home()

    if args["all"] or args["since"]:
        since_ms = parse_duration(args["since"]) if args["since"] else None
        if args["since"] and since_ms is None:
            sys.stderr.write(f"caveman-stats: --since takes Nh or Nd (e.g. 7d, 24h), got: {args['since']}\n")
            return 2
        agg = aggregate_history(home, since_ms)
        sys.stdout.write(format_history(agg, args["since"]))
        return 0

    session_id = args["session_id"]
    if not session_id and args["transcript"]:
        session_id = Path(args["transcript"]).stem

    turns, _ = parse_transcript(args["transcript"])
    input_tokens, output_tokens = query_session_usage(home, session_id)
    mode = read_mode_flag(home)
    model = query_last_model(home, session_id)

    if output_tokens > 0 or turns > 0:
        est_saved, est_usd, _ = derive_savings(output_tokens, mode, model)
        append_history(home, {
            "ts": int(time.time() * 1000),
            "session_id": session_id,
            "mode": mode,
            "model": model,
            "input_tokens": input_tokens,
            "output_tokens": output_tokens,
            "turns": turns,
            "est_saved_tokens": est_saved,
            "est_saved_usd": est_usd,
        })
        agg = aggregate_history(home, None)
        write_statusline_suffix(home, agg["est_saved_tokens"])

    if args["share"]:
        sys.stdout.write(format_share(turns, output_tokens, mode, model) + "\n")
    else:
        sys.stdout.write(format_stats(turns, input_tokens, output_tokens, mode, model, session_id))
    return 0


if __name__ == "__main__":
    sys.exit(main())
