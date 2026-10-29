#!/usr/bin/env python3
"""An OpenAI-compatible provider that lets you park a turn wherever you want it.

Copy of the repo's run-forebrain fake provider; see that skill for the full
rationale. Modes: hang, stream, reply, drip, toolcall.
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
PORT = int(sys.argv[2]) if len(sys.argv) > 2 else 8741
TEXT = sys.argv[3] if len(sys.argv) > 3 else "FAKE_ANSWER"
DRIP_DELAY = float(sys.argv[4]) if len(sys.argv) > 4 else 0.6

TOOL_SCRIPT = [
    ("enter_plan_mode", '{"reason":"drive the approval overlay"}'),
    ("exit_plan_mode", "{}"),
]
# FAKE_TOOL_NAME / FAKE_TOOL_ARGS replace the default script when set, so a case
# can drive one named tool of its own without a new provider. The defaults above
# are unchanged for every existing caller.
if os.environ.get("FAKE_TOOL_NAME"):
    TOOL_SCRIPT = [(os.environ["FAKE_TOOL_NAME"], os.environ.get("FAKE_TOOL_ARGS", "{}"))]
# FAKE_TOOL_SCRIPT points at a JSON file holding the whole script as
# [["name", "arguments-json"], ...] and replaces it outright, which is what a
# case needs when the turn has to reach a tool only after an earlier one has
# been answered. REQUESTS is one counter for the whole provider process, so the
# main agent and any subagent consume the same script in arrival order: a case
# that wants a subagent to fall through to the plain text leaves the script
# shorter than the request number that subagent will make. Unset, the two
# single-shot switches above and the default script behave exactly as before.
SCRIPT_MULTI_SHOT = False
if os.environ.get("FAKE_TOOL_SCRIPT"):
    with open(os.environ["FAKE_TOOL_SCRIPT"], "r", encoding="utf-8") as _script_file:
        TOOL_SCRIPT = [(name, arguments) for name, arguments in json.load(_script_file)]
    SCRIPT_MULTI_SHOT = True
REQUESTS = itertools.count()
# How many netdrop-marker requests have been cut so far; the marker's own
# counter counts only requests that carried it.
NETDROP_MARKER_COUNT = itertools.count()


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
            request = json.loads(body)
        except Exception:
            request = {}
        streamed = request.get("stream") if isinstance(request, dict) else None
        sys.stderr.write("REQUEST %s stream=%s mode=%s\n" % (self.path, streamed, MODE))
        sys.stderr.flush()
        # FAKE_DUMP_DIR keeps every request body as one JSON line, which is how
        # a case checks what the model was actually sent - the system prompt,
        # the reminder appended to it, the task a subagent was handed. Unset,
        # nothing is written and the behaviour is unchanged.
        dump_dir = os.environ.get("FAKE_DUMP_DIR")
        if dump_dir:
            os.makedirs(dump_dir, exist_ok=True)
            with open(os.path.join(dump_dir, "requests.jsonl"), "ab") as dump_file:
                dump_file.write(body + b"\n")

        if MODE == "toolcall":
            self.serve_tool_script(next(REQUESTS), streamed is True and SCRIPT_MULTI_SHOT)
            return

        # In `reply` mode one process also serves the subagent-net cases: it
        # tells the main agent and a subagent apart by their system prompt (not
        # by request order, since a dropped-connection retry would arrive out
        # of order), dispatches a probe when asked, drops the subagent's
        # connection until it is told to continue, and answers `continue`. Any
        # other request falls through to the ordinary reply.
        if MODE == "reply" and self.serve_subagent_steer(request):
            return
        if MODE == "reply" and self.serve_subagent_net(request):
            return
        if MODE == "reply" and self.serve_netdrop_marker(request):
            return
        if MODE == "reply" and self.serve_tool_call_marker(request):
            return
        if MODE == "reply" and self.serve_batch(request):
            return

        if MODE == "netdrop" and self.serve_netdrop():
            return

        if MODE == "hang":
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
            while True:
                time.sleep(3600)

        self.write_event(chunk(finish="stop"))
        self.write_raw(b"data: [DONE]\n\n")
        self.write_raw(b"")

    # The marker a caller puts in its message to ask the main agent for the
    # subagent-net probe, and the system-prompt fragment that identifies a
    # general-purpose subagent's own request.
    SUBAGENT_NET_MARKER = "[[e2e:subagent-net]]"
    SUBAGENT_SYSTEM = "You are a general-purpose subagent"
    # The marker for the subagent-steer case. The dispatched task carries it
    # too, so the subagent's own requests are recognised by it.
    SUBAGENT_STEER_MARKER = "[[e2e:subagent-steer]]"
    # The marker for the run-line network-retry case: the first three
    # requests that carry it are cut mid-answer, every later one answers
    # normally, so the retry shows on the status line and the turn recovers.
    NETDROP_MARKER = "[[e2e:netdrop]]"
    NETDROP_MARKER_DROPS = 3
    # How long the subagent holds its answer after the delta that takes the
    # steer: the page is checked inside this window, while it still runs.
    SUBAGENT_STEER_HOLD = 30
    # The marker a caller puts in its message to have the model call one tool of
    # the caller's choosing. The JSON that follows it names the tool and its
    # arguments, so a spec scripts a call without a new provider:
    #   [[e2e:tool-call]]{"name":"present_files","arguments":{"files":[…]}}
    # The conversation's first request answers with that call; the request that
    # carries its result gets the ordinary reply, so the turn ends the way a
    # turn that ran the tool does.
    TOOL_CALL_MARKER = "[[e2e:tool-call]]"
    # The marker a batch task's input carries. A turn carrying it holds its
    # answer long enough to be stopped from the task page; the continue turn of
    # a stopped execution is refused, so the execution ends failed and the page
    # offers 重新执行.
    BATCH_MARKER = "[[e2e:batch]]"
    BATCH_HOLD = 30
    # The marker that makes a batch turn keep writing while it holds: the words
    # arrive one step at a time after the first, so a page that opens mid-run
    # has something to see grow. Without it the turn writes once and is silent,
    # which is all a case that only stops it needs.
    BATCH_LIVE_MARKER = "[[e2e:batch-live]]"
    BATCH_LIVE_WORDS = (" slowly", " but", " surely", " and", " steadily")
    BATCH_LIVE_STEP = 3
    # The live case does not have to stay stoppable for half a minute: the
    # watcher needs the run to still be going when it attaches, and then to end
    # by itself, which is what this shorter hold leaves it to do.
    BATCH_LIVE_HOLD = 10
    # What the engine continues a stopped execution with (pkg/turn/batch.go,
    # BatchContinuePrompt): the one human text a batch turn must not be.
    CONTINUE_PROMPT = "Continue this task from where it stopped"

    @staticmethod
    def text_of(content):
        if isinstance(content, str):
            return content
        if isinstance(content, list):
            return " ".join(
                str(part.get("text") or "")
                for part in content
                if isinstance(part, dict)
            )
        return ""

    def last_role_text(self, messages, role):
        for message in reversed(messages):
            if message.get("role") == role:
                return self.text_of(message.get("content"))
        return ""

    def human_user_texts(self, messages):
        """The user's own messages, excluding the framework-injected context.

        The runtime appends an environment-context message with the user role
        right after what the person typed, so "the last user message" is that
        boilerplate rather than the message under test.
        """
        out = []
        for message in messages:
            if message.get("role") != "user":
                continue
            text = self.text_of(message.get("content"))
            if text.lstrip().startswith("<forebrain_"):
                continue
            out.append(text)
        return out

    def serve_subagent_net(self, request):
        """Answers the subagent-net cases in `reply` mode; False to fall through.

        A subagent's request (system prompt names general-purpose) answers the
        configured text only once the user has told it to continue; before that
        its connection is dropped with no bytes to stand in for a network
        failure. The main agent dispatches the probe when the caller's message
        carries the marker, then answers a fixed line once the tool result is
        in.
        """
        messages = request.get("messages") if isinstance(request, dict) else None
        if not isinstance(messages, list) or not messages:
            return False
        system = " ".join(
            self.text_of(message.get("content"))
            for message in messages
            if message.get("role") == "system"
        )
        human = self.human_user_texts(messages)
        if self.SUBAGENT_SYSTEM in system:
            last = human[-1] if human else ""
            if last.strip().lower() == "continue":
                self.stream_text(TEXT)
            else:
                # Simulate the network dropping: close with no response bytes.
                self.close_connection = True
            return True
        if not any(self.SUBAGENT_NET_MARKER in text for text in human):
            return False
        if any(message.get("role") == "tool" for message in messages):
            # The probe's result is in; the main agent says how it went.
            self.stream_text("primary noted the failure")
            return True
        self.stream_tool_call("subagent_run", {
            "title": "network probe",
            "task": "answer briefly",
            "subagent_type": "general-purpose",
        })
        return True

    def serve_subagent_steer(self, request):
        """Answers the subagent-steer case in `reply` mode; False to fall through.

        The main agent dispatches a subagent whose task carries the marker. The
        subagent's first request runs a slow shell call, the window a steer is
        queued in from its tab; the request after it carries the call's result
        and the steer, and gets one delta - which commits the steer's delivery
        - and then holds its finish for SUBAGENT_STEER_HOLD seconds, so the
        subagent is still running while the page is checked.
        """
        messages = request.get("messages") if isinstance(request, dict) else None
        if not isinstance(messages, list) or not messages:
            return False
        human = self.human_user_texts(messages)
        if not any(self.SUBAGENT_STEER_MARKER in text for text in human):
            return False
        system = " ".join(
            self.text_of(message.get("content"))
            for message in messages
            if message.get("role") == "system"
        )
        has_tool_result = any(message.get("role") == "tool" for message in messages)
        if self.SUBAGENT_SYSTEM not in system:
            if has_tool_result:
                self.stream_text("primary done")
            else:
                self.stream_tool_call("subagent_run", {
                    "title": "slow probe",
                    "task": "run the slow step " + self.SUBAGENT_STEER_MARKER,
                    "subagent_type": "general-purpose",
                })
            return True
        if not has_tool_result:
            self.stream_tool_call("shell", {"command": "sleep 8"})
            return True
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        self.write_event(chunk(delta={"role": "assistant", "content": "steer received"}))
        time.sleep(self.SUBAGENT_STEER_HOLD)
        self.write_event(chunk(finish="stop"))
        self.write_raw(b"data: [DONE]\n\n")
        self.write_raw(b"")
        return True

    def serve_netdrop_marker(self, request):
        """Answers the run-line network-retry case in `reply` mode; False to
        fall through.

        The first three requests whose last human message carries the marker
        each answer 200 with `text/event-stream`, write one assistant chunk,
        and close the connection without a finish frame — a proxy cutting an
        SSE stream mid-answer. Every request from the fourth on falls through
        to the ordinary reply, so the turn recovers on the run's own retry
        and the dead attempts' partial text must never survive anywhere.
        """
        messages = request.get("messages") if isinstance(request, dict) else None
        if not isinstance(messages, list) or not messages:
            return False
        human = self.human_user_texts(messages)
        if not any(self.NETDROP_MARKER in text for text in human):
            return False
        if next(NETDROP_MARKER_COUNT) >= self.NETDROP_MARKER_DROPS:
            return False
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        self.write_event(chunk(delta={"role": "assistant", "content": "partial answer that never"}))
        sys.stderr.write("NETDROP marker clean close without a finish frame\n")
        sys.stderr.flush()
        self.close_connection = True
        try:
            self.wfile.flush()
        except Exception:
            pass
        try:
            self.connection.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        try:
            self.connection.close()
        except OSError:
            pass
        return True

    def serve_tool_call_marker(self, request):
        """Answers a caller's scripted tool call in `reply` mode; False to fall
        through.

        The caller's message carries TOOL_CALL_MARKER followed by the JSON that
        names the tool and its arguments. The conversation's first request
        answers with exactly that call; the request after it carries the call's
        result and gets the ordinary reply.
        """
        messages = request.get("messages") if isinstance(request, dict) else None
        if not isinstance(messages, list) or not messages:
            return False
        payload = ""
        for text in self.human_user_texts(messages):
            at = text.find(self.TOOL_CALL_MARKER)
            if at < 0:
                continue
            payload = text[at + len(self.TOOL_CALL_MARKER):].strip()
        if not payload:
            return False
        if any(message.get("role") == "tool" for message in messages):
            self.stream_text(TEXT)
            return True
        try:
            # The payload is one JSON object; whatever the person wrote after
            # it belongs to the message, not to the call.
            call, _ = json.JSONDecoder().raw_decode(payload)
        except ValueError:
            sys.stderr.write("TOOL_CALL marker payload is not JSON\n")
            sys.stderr.flush()
            self.stream_text(TEXT)
            return True
        name = str(call.get("name") or "").strip()
        if not name:
            sys.stderr.write("TOOL_CALL marker names no tool\n")
            sys.stderr.flush()
            self.stream_text(TEXT)
            return True
        arguments = call.get("arguments")
        self.stream_tool_call(name, arguments if arguments is not None else {}, "call_scripted")
        return True

    def serve_batch(self, request):
        """Answers the batch-task cases in `reply` mode; False to fall through.

        A batch turn whose conversation carries BATCH_MARKER holds: one delta,
        then BATCH_HOLD seconds of silence, so the page has a running execution
        to stop. The continue turn of that stopped execution is refused with a
        400 — the network boundary's non-retryable rejection — so the execution
        ends failed and the page offers 重新执行. A turn that also carries
        BATCH_LIVE_MARKER writes one word every BATCH_LIVE_STEP seconds while it
        holds, so a page opened mid-run has a live tail to draw.
        """
        messages = request.get("messages") if isinstance(request, dict) else None
        if not isinstance(messages, list) or not messages:
            return False
        human = self.human_user_texts(messages)
        if not any(self.BATCH_MARKER in text for text in human):
            return False
        if human and human[-1].strip().startswith(self.CONTINUE_PROMPT):
            self.refuse(400, "the fake provider refuses every continue turn")
            return True
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        self.write_event(chunk(delta={"role": "assistant", "content": "working"}))
        live = any(self.BATCH_LIVE_MARKER in text for text in human)
        if live:
            # The turn keeps talking while it holds: each word is its own
            # delta, so a watching page draws the answer grow instead of
            # receiving one finished block.
            for word in self.BATCH_LIVE_WORDS:
                time.sleep(self.BATCH_LIVE_STEP)
                try:
                    self.write_event(chunk(delta={"content": word}))
                except OSError:
                    return True
        time.sleep(self.BATCH_LIVE_HOLD if live else self.BATCH_HOLD)
        # The page may have stopped the run long ago; the socket it was written
        # to is gone, and that is this case's whole point.
        try:
            self.write_event(chunk(finish="stop"))
            self.write_raw(b"data: [DONE]\n\n")
            self.write_raw(b"")
        except OSError:
            pass
        return True

    def refuse(self, status, message):
        """Answers one request with an API error: no stream, no retry."""
        body = json.dumps({"error": {"message": message, "type": "invalid_request_error"}}).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
        self.wfile.flush()
        sys.stderr.write("REFUSED %d %s\n" % (status, message))
        sys.stderr.flush()

    def serve_netdrop(self):
        """Request 1 streams part of the answer and closes the connection
        cleanly with no finish frame — a proxy cutting an SSE stream
        mid-answer, which must surface as a truncation and be continued, not
        pass as a success. Requests from the second on answer normally, so
        the continuation succeeds. FAKE_NETDROP_EVERY=1 drops every request
        instead, for cancellation and exhaustion cases."""
        if next(REQUESTS) > 0 and not os.environ.get("FAKE_NETDROP_EVERY"):
            return False
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        self.write_event(chunk(delta={"role": "assistant", "content": "partial answer that never"}))
        sys.stderr.write("NETDROP clean close without a finish frame\n")
        sys.stderr.flush()
        self.close_connection = True
        try:
            self.wfile.flush()
        except Exception:
            pass
        try:
            self.connection.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        try:
            self.connection.close()
        except OSError:
            pass
        return True

    def stream_text(self, text):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        self.write_event(chunk(delta={"role": "assistant", "content": text}))
        self.write_event(chunk(finish="stop"))
        self.write_raw(b"data: [DONE]\n\n")
        self.write_raw(b"")

    def stream_tool_call(self, name, arguments, call_id="call_subagent_net"):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        self.write_event(chunk(delta={
            "role": "assistant",
            "content": "calling %s" % name,
            "tool_calls": [{
                "index": 0,
                "id": call_id,
                "type": "function",
                "function": {"name": name, "arguments": json.dumps(arguments)},
            }],
        }))
        self.write_event(chunk(finish="tool_calls"))
        self.write_raw(b"data: [DONE]\n\n")
        self.write_raw(b"")

    def serve_tool_script(self, nth, streaming=True):
        # A turn's first request streams; the request that carries a tool result
        # back does not, and must be answered with a JSON body rather than an
        # event stream. Without this the continuation is unparseable and the
        # client gives up after its retries, so no scripted sequence longer than
        # one call could ever run.
        if not streaming:
            name, arguments = TOOL_SCRIPT[nth] if nth < len(TOOL_SCRIPT) else (None, None)
            if arguments is not None and not isinstance(arguments, str):
                arguments = json.dumps(arguments)
            message = {"role": "assistant", "content": [{"type": "text", "text": TEXT}]}
            if name:
                message["tool_calls"] = [{
                    "id": "call_fake_%d" % nth,
                    "type": "function",
                    "function": {"name": name, "arguments": arguments},
                }]
            payload = json.dumps({
                "id": "fake-1",
                "object": "chat.completion",
                "created": 1,
                "model": "fake-model",
                "choices": [{"index": 0, "message": message,
                             "finish_reason": "tool_calls" if name else "stop"}],
            }).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            self.wfile.flush()
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        if nth >= len(TOOL_SCRIPT):
            self.write_event(chunk(delta={"role": "assistant", "content": TEXT}))
            self.write_event(chunk(finish="stop"))
            self.write_raw(b"data: [DONE]\n\n")
            self.write_raw(b"")
            return
        name, arguments = TOOL_SCRIPT[nth]
        # An empty name is a plain-text beat: it lets a multi-shot script place
        # one scripted answer between two scripted tool calls, which is how a
        # subagent's own request is kept out of the tool sequence.
        if not name:
            self.write_event(chunk(delta={"role": "assistant", "content": TEXT}))
            self.write_event(chunk(finish="stop"))
            self.write_raw(b"data: [DONE]\n\n")
            self.write_raw(b"")
            return
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
