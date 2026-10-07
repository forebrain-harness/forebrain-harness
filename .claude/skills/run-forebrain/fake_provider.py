#!/usr/bin/env python3
"""An OpenAI-compatible provider that lets you park a turn wherever you want it.

Driving forebrain's TUI means controlling *when* the model answers, because most
interactive behaviour is defined relative to the first output-bearing event:
before it, Esc withdraws the submission; after it, Esc is ordinary
cancellation. A real provider answers too fast and too unpredictably to sit in
either state on purpose.

Modes:
  hang    accept the request and never answer - holds the pre-response window
          open indefinitely.
  stream  send exactly one content delta, then hang - crosses the response
          boundary and stops, leaving the turn in flight.
  reply   send one delta and finish normally.
  drip    send the answer one word at a time with a pause between each, then
          finish. This is the ONLY mode that can verify streaming rendering: a
          single delta cannot distinguish "painted as it arrives" from "held
          until some flush boundary", because both look identical when there is
          only one. Watch the screen grow between captures.
  shell
          answer the first request with a shell call running the answer text.
          Pass a JSON object instead of a bare command to set the other shell
          arguments, e.g. sandbox_permissions, which is what raises the
          approval overlay for an ordinary command.
  toolcall
          answer the first request with an enter_plan_mode call and the second
          with an exit_plan_mode call, then reply in plain text. Both calls are
          approval gates, so this is the mode that puts a real approval overlay
          on screen and leaves a real action row behind when it is answered.
  ask     answer the first request with a two-question user_interaction call,
          then reply in plain text. This is the mode that puts the real question
          form on screen - tab bar, options and the editable Other row - which
          is the only way to see where its caret actually is.
  goal    drive a /goal run: every request from the goal evaluator (it is the
          one whose system prompt asks to judge an objective) is answered with
          the next verdict from the answer text - a JSON array such as
          '["continue","continue","done"]', default exactly that - and every
          other request drips a short "round N" answer, so each continuation
          round is visible on screen.
  limit   refuse every request with a 429 "usage limit reached" until the
          window closes (FAKE_LIMIT_SECONDS after the first request, default
          20), then reply normally. The body carries resets_in_seconds and no
          Retry-After header, so the SDK's own short retry cannot sit out the
          wait: this is the mode that drives auto-continue for real.
  tool    answer the first request with whatever tool call the answer text
          names: a JSON object {"name": ..., "arguments": {...}}, or an array of
          them to script one call per turn. Use it to put a card that only one
          tool produces on screen - a memories_search result, say - without
          teaching the harness about that tool.
  subagent-net
          drive the "subagent dies on the network, then the user continues it
          from its own view" scenario, told apart by request content rather
          than request order. The conversation's first request is answered with
          a subagent_run dispatch of a general-purpose probe; the subagent's own
          request (recognised by its system prompt) is dropped by closing the
          connection with no bytes until its last user message is "continue",
          when it is answered with the answer text. A subagent request whose
          last user message is          "hold" is left hanging instead, so the subagent
          stays running and the surface's Esc "interrupt to send" window can be
          driven. The conversation's later requests answer with a fixed line.
  subagent-limit
          drive the "a subagent hits the usage limit, then continues itself"
          scenario, told apart by request content. The conversation's first
          request is answered with a subagent_run dispatch of a general-purpose
          probe; the subagent's own request (recognised by its system prompt) is
          refused with the "limit" 429 for FAKE_LIMIT_SECONDS (default 20)
          seconds after its first request and then answers with the answer text,
          which is the automatic continuation running. The conversation's later
          requests answer with a fixed line.

Every request is logged to stderr, which is the only reliable way to know the
request actually went out: forebrain buffers assistant deltas in the reducer and
paints nothing until the run ends, so the screen looks identical before and
after the model has started answering.
"""

import http.server
import itertools
import json
import os
import socket
import socketserver
import sys
import time

MODE = sys.argv[1] if len(sys.argv) > 1 else "hang"
PORT = int(sys.argv[2]) if len(sys.argv) > 2 else 8731
TEXT = sys.argv[3] if len(sys.argv) > 3 else "FAKE_ANSWER"
DRIP_DELAY = float(sys.argv[4]) if len(sys.argv) > 4 else 0.6

# The tool calls a scripted mode hands out, in order. Each request past the end
# of its list is answered in plain text, so a cancelled gate leaves the run
# stopped rather than looping.
ASK_ARGUMENTS = """{"questions": [{"header": "轮询方案", "question": "若选择「请求内同步轮询」：单个安装/卸载请求最多等待多久？", "multiSelect": false, "options": [{"label": "60 秒", "description": "轮询间隔 2 秒，最多 60 秒。兼顾安装耗时与网关等待", "recommended": true}, {"label": "15 秒", "description": "轮询间隔 1 秒，最多 15 秒。请求快，但较慢的安装会超时"}, {"label": "180 秒", "description": "轮询间隔 3 秒，最多 180 秒。等待充分，但请求长时间占用连接"}]}, {"header": "等待上限", "question": "安装失败时如何回报给调用方？", "multiSelect": true, "options": [{"label": "原始报错", "description": "把底层工具的报错原文透传出去"}, {"label": "结构化错误", "description": "带错误码与阶段信息"}]}]}"""

def _shell_calls(text):
    """The shell calls the "shell" mode hands out, in order.

    A bare command is the common case. A JSON object sets the other shell
    arguments too - sandbox_permissions is the one that raises the approval
    overlay for an ordinary command - and a JSON array scripts one call per
    turn, which is how a remembered approval is shown to cover the next
    command as well as the one it was granted for.
    """
    text = text.lstrip()
    if text.startswith("["):
        return json.loads(text)
    if text.startswith("{"):
        return [json.loads(text)]
    return [{"command": text}]


SHELL_CALLS = _shell_calls(TEXT)


def _scripted_tool_calls(text):
    """The (name, arguments) pairs the "tool" mode hands out, in order.

    The answer text is a JSON object naming one call, or an array of them.
    `arguments` may be given as an object, which is encoded here, or already as
    the JSON string the wire format carries.
    """
    text = text.strip()
    if not text.startswith(("{", "[")):
        return []
    calls = json.loads(text)
    if isinstance(calls, dict):
        calls = [calls]
    pairs = []
    for call in calls:
        arguments = call.get("arguments", {})
        if not isinstance(arguments, str):
            arguments = json.dumps(arguments)
        pairs.append((call["name"], arguments))
    return pairs

TOOL_SCRIPTS = {
    "toolcall": [
        ("enter_plan_mode", '{"reason":"drive the approval overlay"}'),
        ("exit_plan_mode", "{}"),
    ],
    "ask": [("user_interaction", ASK_ARGUMENTS)],
    # The command is whatever was passed as the answer text, so one mode drives
    # every shape of shell approval the overlay has to word differently.
    "shell": [("shell", json.dumps(call)) for call in SHELL_CALLS],
    "tool": _scripted_tool_calls(TEXT) if MODE == "tool" else [],
}
TOOL_SCRIPT = TOOL_SCRIPTS.get(MODE, [])
REQUESTS = itertools.count()
DUMPS = itertools.count(1)

LIMIT_SECONDS = float(os.environ.get("FAKE_LIMIT_SECONDS") or 20)
LIMIT_RESETS_AT = []

GOAL_VERDICTS = json.loads(TEXT) if MODE == "goal" and TEXT.strip().startswith("[") else ["continue", "continue", "done"]
GOAL_JUDGED = itertools.count()
GOAL_ROUNDS = itertools.count(1)


def _is_goal_evaluation(body):
    """Whether a request comes from the goal evaluator, by its system prompt."""
    try:
        messages = json.loads(body).get("messages") or []
    except Exception:
        return False
    for message in messages:
        content = message.get("content")
        if isinstance(content, list):
            content = " ".join(part.get("text", "") for part in content if isinstance(part, dict))
        if isinstance(content, str) and "check whether an autonomous coding agent" in content:
            return True
    return False


def _system_and_last_user(body):
    """The first system message and the last user message, as plain text.

    subagent-net distinguishes the main agent from a subagent by what the
    request says, not by when it arrives: an LLM client retries a dropped
    connection, and issuing answers by request order would put the retry's
    answer on the wrong conversation.
    """
    try:
        messages = json.loads(body).get("messages") or []
    except Exception:
        return "", ""
    system = ""
    last_user = ""
    for message in messages:
        content = message.get("content")
        if isinstance(content, list):
            content = " ".join(part.get("text", "") for part in content if isinstance(part, dict))
        if not isinstance(content, str):
            continue
        role = message.get("role")
        if role == "system" and system == "":
            system = content
        elif role == "user":
            last_user = content
    return system, last_user


# The general-purpose subagent's system prompt leads with this line (see
# pkg/agent/subagent_defs.go); a request carrying it is the subagent's, not the
# conversation's.
SUBAGENT_SYSTEM_MARK = "You are a general-purpose subagent"
# The main agent's first request dispatches exactly this probe.
SUBAGENT_NET_PROBE_ARGS = json.dumps({
    "title": "network probe",
    "task": "answer briefly",
    "subagent_type": "general-purpose",
})
SUBAGENT_NET_MAIN = itertools.count()
SUBAGENT_LIMIT_RESETS_AT = []
SUBAGENT_LIMIT_MAIN = itertools.count()


def chunk(delta=None, finish=None):
    return {
        "id": "fake-1",
        "object": "chat.completion.chunk",
        "created": 1,
        "model": "fake-model",
        "choices": [{"index": 0, "delta": delta or {}, "finish_reason": finish}],
    }


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
        try:
            streamed = json.loads(body).get("stream")
        except Exception:
            streamed = None
        sys.stderr.write("REQUEST %s stream=%s mode=%s\n" % (self.path, streamed, MODE))
        sys.stderr.flush()
        # FAKE_DUMP_DIR keeps every request body, numbered in arrival order, so
        # what the model was sent -- and whether one turn's request is the
        # byte-for-byte prefix of the next -- can be read back afterwards.
        dump_dir = os.environ.get("FAKE_DUMP_DIR")
        if dump_dir:
            with open(os.path.join(dump_dir, "request-%04d.json" % next(DUMPS)), "wb") as f:
                f.write(body)

        if MODE in TOOL_SCRIPTS:
            self.serve_tool_script(next(REQUESTS))
            return

        if MODE == "goal":
            self.serve_goal(body)
            return

        if MODE == "subagent-net":
            self.serve_subagent_net(body)
            return

        if MODE == "subagent-limit":
            self.serve_subagent_limit(body)
            return

        if MODE == "limit" and self.serve_limit():
            return

        if MODE == "hang":
            # Never answer. The socket stays open, so forebrain keeps the turn in
            # flight and the withdrawal window stays open until Esc closes it.
            while True:
                time.sleep(3600)

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()

        if MODE == "drip":
            first = True
            for word in TEXT.split():
                delta = {"content": ("" if first else " ") + word}
                if first:
                    delta["role"] = "assistant"
                    first = False
                self.write_event(chunk(delta=delta))
                time.sleep(DRIP_DELAY)
            self.write_event(chunk(finish="stop"))
            self.write_raw(b"data: [DONE]\n\n")
            self.write_raw(b"")
            return

        self.write_event(chunk(delta={"role": "assistant", "content": TEXT}))

        if MODE == "stream":
            # One delta and then nothing: the response boundary is crossed but
            # the turn never completes.
            while True:
                time.sleep(3600)

        final = chunk(finish="stop")
        if MODE == "usage":
            # DeepSeek-style usage with a cache split, so the persisted run
            # usage and /status's cache hit rate can be checked against known
            # numbers: 2000 prompt tokens, 1800 of them served from the cache.
            final["usage"] = {
                "prompt_tokens": 2000,
                "completion_tokens": 40,
                "total_tokens": 2040,
                "prompt_cache_hit_tokens": 1800,
                "prompt_cache_miss_tokens": 200,
            }
        self.write_event(final)
        self.write_raw(b"data: [DONE]\n\n")
        self.write_raw(b"")  # terminating zero-length chunk

    def serve_limit(self):
        """Refuse the request while the usage window is closed.

        Returns False once the window has reopened, so the request is answered
        like reply mode.
        """
        now = time.time()
        if not LIMIT_RESETS_AT:
            LIMIT_RESETS_AT.append(now + LIMIT_SECONDS)
        remaining = LIMIT_RESETS_AT[0] - now
        if remaining <= 0:
            return False
        self.refuse_usage_limit(remaining)
        return True

    def refuse_usage_limit(self, remaining):
        """Answer with the 429 body that drives auto-continue, carrying
        resets_in_seconds so the runtime schedules the continuation itself."""
        sys.stderr.write("LIMIT refused, resets in %.1fs\n" % remaining)
        sys.stderr.flush()
        payload = json.dumps({"error": {
            "type": "usage_limit_reached",
            "message": "The usage limit has been reached",
            "plan_type": "plus",
            "resets_in_seconds": int(remaining + 0.999),
        }}).encode()
        self.send_response(429)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)
        self.wfile.flush()

    def answer_text(self, text):
        """Answer one request with the answer text, in one delta and a finish."""
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        self.write_event(chunk(delta={"role": "assistant", "content": text}))
        self.write_event(chunk(finish="stop"))
        self.write_raw(b"data: [DONE]\n\n")
        self.write_raw(b"")

    def serve_subagent_limit(self, body):
        """Drive the "a subagent hits the usage limit, then continues itself"
        scenario, told apart by request content rather than request order.

        A request whose system prompt carries the general-purpose subagent's
        mark is the subagent's: for FAKE_LIMIT_SECONDS (default 20) after its
        first request it is refused with the same 429 the "limit" mode returns,
        and afterwards it answers with the answer text - which is the automatic
        continuation running. Every other request is the conversation's: the
        first dispatches exactly the network probe, the rest answer with a
        fixed line so the primary turn can end after the failure.
        """
        system, _ = _system_and_last_user(body)
        if SUBAGENT_SYSTEM_MARK in system:
            now = time.time()
            if not SUBAGENT_LIMIT_RESETS_AT:
                SUBAGENT_LIMIT_RESETS_AT.append(now + LIMIT_SECONDS)
            remaining = SUBAGENT_LIMIT_RESETS_AT[0] - now
            if remaining > 0:
                self.refuse_usage_limit(remaining)
                return
            sys.stderr.write("SUBAGENT-LIMIT answered after the reset\n")
            sys.stderr.flush()
            self.answer_text(TEXT)
            return

        nth = next(SUBAGENT_LIMIT_MAIN)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        if nth == 0:
            self.write_event(chunk(delta={
                "role": "assistant",
                "content": "delegating the probe",
                "tool_calls": [{
                    "index": 0,
                    "id": "call_limit_0",
                    "type": "function",
                    "function": {"name": "subagent_run", "arguments": SUBAGENT_NET_PROBE_ARGS},
                }],
            }))
            self.write_event(chunk(finish="tool_calls"))
        else:
            self.write_event(chunk(delta={"role": "assistant", "content": "primary noted the failure"}))
            self.write_event(chunk(finish="stop"))
        self.write_raw(b"data: [DONE]\n\n")
        self.write_raw(b"")

    def serve_goal(self, body):
        """Answer the evaluator with the next verdict, a round with text."""
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        if _is_goal_evaluation(body):
            n = next(GOAL_JUDGED)
            status = GOAL_VERDICTS[min(n, len(GOAL_VERDICTS) - 1)]
            verdict = json.dumps({"status": status, "why": "verdict %d: %s" % (n + 1, status)})
            sys.stderr.write("GOAL VERDICT %s\n" % verdict)
            sys.stderr.flush()
            self.write_event(chunk(delta={"role": "assistant", "content": verdict}))
        else:
            n = next(GOAL_ROUNDS)
            first = True
            for word in ("round %d: working toward the objective" % n).split():
                delta = {"content": ("" if first else " ") + word}
                if first:
                    delta["role"] = "assistant"
                    first = False
                self.write_event(chunk(delta=delta))
                time.sleep(DRIP_DELAY)
        self.write_event(chunk(finish="stop"))
        self.write_raw(b"data: [DONE]\n\n")
        self.write_raw(b"")

    def serve_subagent_net(self, body):
        """Drive the "subagent fails on the network, then the user continues it"
        scenario, told apart by request content rather than request order.

        A request whose system prompt carries the general-purpose subagent's
        mark is the subagent's: its last user message is "continue" once the
        user has driven it from its own view, and is answered with the answer
        text; any other subagent request is dropped by closing the connection
        with no bytes, the way a network interruption looks to the client — a
        retry is dropped the same way until it gives up. Every other request is
        the conversation's: the first dispatches exactly the network probe, the
        rest answer with a fixed line so the primary turn can end after the
        subagent's failure.
        """
        system, last_user = _system_and_last_user(body)
        if SUBAGENT_SYSTEM_MARK in system:
            if last_user.strip().lower() == "hold":
                # Keep the subagent running: the execution stays in flight so
                # the surface can be driven into Esc's "interrupt to send"
                # window, where a queued steer is what Esc flushes.
                sys.stderr.write("SUBAGENT-NET hold subagent request\n")
                sys.stderr.flush()
                while True:
                    time.sleep(3600)
            if last_user.strip().lower() == "continue":
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Transfer-Encoding", "chunked")
                self.end_headers()
                self.write_event(chunk(delta={"role": "assistant", "content": TEXT}))
                self.write_event(chunk(finish="stop"))
                self.write_raw(b"data: [DONE]\n\n")
                self.write_raw(b"")
                return
            # No bytes at all: the subagent's request dies on the wire, so the
            # run fails with a transport error and the turn ends.
            sys.stderr.write("SUBAGENT-NET drop subagent request\n")
            sys.stderr.flush()
            self.close_connection = True
            try:
                self.connection.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            try:
                self.connection.close()
            except OSError:
                pass
            return

        nth = next(SUBAGENT_NET_MAIN)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        if nth == 0:
            self.write_event(chunk(delta={
                "role": "assistant",
                "content": "delegating the probe",
                "tool_calls": [{
                    "index": 0,
                    "id": "call_net_0",
                    "type": "function",
                    "function": {"name": "subagent_run", "arguments": SUBAGENT_NET_PROBE_ARGS},
                }],
            }))
            self.write_event(chunk(finish="tool_calls"))
        else:
            self.write_event(chunk(delta={"role": "assistant", "content": "primary noted the failure"}))
            self.write_event(chunk(finish="stop"))
        self.write_raw(b"data: [DONE]\n\n")
        self.write_raw(b"")

    def serve_tool_script(self, nth):
        """Answer request `nth` with the scripted tool call, or with text."""
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        if nth >= len(TOOL_SCRIPT):
            # "tool" spends the answer text on the call itself, so its
            # follow-up turn must not echo that JSON back as an answer.
            text = "done" if MODE == "tool" else TEXT
            self.write_event(chunk(delta={"role": "assistant", "content": text}))
            self.write_event(chunk(finish="stop"))
            self.write_raw(b"data: [DONE]\n\n")
            self.write_raw(b"")
            return
        name, arguments = TOOL_SCRIPT[nth]
        self.write_event(chunk(delta={
            "role": "assistant",
            "content": "calling %s" % name,
            "tool_calls": [{
                "index": 0,
                "id": "call_fake_%d" % nth,
                "type": "function",
                "function": {"name": name, "arguments": arguments},
            }],
        }))
        self.write_event(chunk(finish="tool_calls"))
        self.write_raw(b"data: [DONE]\n\n")
        self.write_raw(b"")

    def write_event(self, payload):
        self.write_raw(("data: " + json.dumps(payload) + "\n\n").encode())

    def write_raw(self, payload):
        self.wfile.write(b"%x\r\n%s\r\n" % (len(payload), payload))
        self.wfile.flush()

    def log_message(self, *args):
        pass


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


if __name__ == "__main__":
    sys.stderr.write("LISTENING %d mode=%s\n" % (PORT, MODE))
    sys.stderr.flush()
    Server(("127.0.0.1", PORT), Handler).serve_forever()
