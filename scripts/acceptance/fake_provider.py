#!/usr/bin/env python3
"""An OpenAI-compatible provider that lets you park a turn wherever you want it.

Copy of the repo's run-forebrain fake provider; see that skill for the full
rationale. Modes: hang, stream, reply, drip, toolcall.
"""

import http.server
import itertools
import json
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
REQUESTS = itertools.count()


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

        if MODE == "toolcall":
            self.serve_tool_script(next(REQUESTS))
            return

        # In `reply` mode one process also serves the subagent-net cases: it
        # tells the main agent and a subagent apart by their system prompt (not
        # by request order, since a dropped-connection retry would arrive out
        # of order), dispatches a probe when asked, drops the subagent's
        # connection until it is told to continue, and answers `continue`. Any
        # other request falls through to the ordinary reply.
        if MODE == "reply" and self.serve_subagent_net(request):
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

    def stream_text(self, text):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        self.write_event(chunk(delta={"role": "assistant", "content": text}))
        self.write_event(chunk(finish="stop"))
        self.write_raw(b"data: [DONE]\n\n")
        self.write_raw(b"")

    def stream_tool_call(self, name, arguments):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        self.write_event(chunk(delta={
            "role": "assistant",
            "content": "calling %s" % name,
            "tool_calls": [{
                "index": 0,
                "id": "call_subagent_net",
                "type": "function",
                "function": {"name": name, "arguments": json.dumps(arguments)},
            }],
        }))
        self.write_event(chunk(finish="tool_calls"))
        self.write_raw(b"data: [DONE]\n\n")
        self.write_raw(b"")

    def serve_tool_script(self, nth):
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
