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
            streamed = json.loads(body).get("stream")
        except Exception:
            streamed = None
        sys.stderr.write("REQUEST %s stream=%s mode=%s\n" % (self.path, streamed, MODE))
        sys.stderr.flush()

        if MODE == "toolcall":
            self.serve_tool_script(next(REQUESTS))
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
