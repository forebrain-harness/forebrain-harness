#!/usr/bin/env python3
"""A minimal stdio MCP server for driving forebrain's project-level MCP path.

Speaks just enough of the MCP protocol over stdin/stdout for the client to
connect, list one tool, and call it: initialize -> initialized -> tools/list ->
tools/call. Used only by local acceptance runs.
"""
import json
import sys

TOOL_NAME = sys.argv[1] if len(sys.argv) > 1 else "echo"
SERVER_NAME = sys.argv[2] if len(sys.argv) > 2 else "fake-project-mcp"


def send(msg):
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()


def handle(req):
    method = req.get("method", "")
    req_id = req.get("id")
    if method == "initialize":
        send({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "protocolVersion": req.get("params", {}).get("protocolVersion", "2025-06-18"),
                "capabilities": {"tools": {}},
                "serverInfo": {"name": SERVER_NAME, "version": "1.0.0"},
            },
        })
    elif method == "tools/list":
        send({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "tools": [{
                    "name": TOOL_NAME,
                    "description": "Echoes its input back (fake project MCP server).",
                    "inputSchema": {
                        "type": "object",
                        "properties": {"text": {"type": "string"}},
                        "required": ["text"],
                    },
                }]
            },
        })
    elif method == "tools/call":
        args = req.get("params", {}).get("arguments", {})
        text = args.get("text", "")
        send({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "content": [{"type": "text", "text": "echo: " + str(text)}],
                "isError": False,
            },
        })
    elif method == "ping":
        send({"jsonrpc": "2.0", "id": req_id, "result": {}})
    else:
        # Anything else gets an answer. Silence is not one: a client that probes
        # a method this server does not implement — the stateless
        # server/discover handshake newer clients open with, for one — would
        # wait for a reply that never comes, and a hung handshake reads as "the
        # server is broken" instead of "the server is older". A JSON-RPC error
        # is what makes the client fall back to the initialize handshake.
        send({
            "jsonrpc": "2.0",
            "id": req_id,
            "error": {"code": -32601, "message": "method not found: " + method},
        })


def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except json.JSONDecodeError:
            continue
        # Notifications (no id) get no response.
        if "id" not in req:
            continue
        handle(req)


if __name__ == "__main__":
    main()
