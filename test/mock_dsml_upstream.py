#!/usr/bin/env python3
"""Tiny mock upstream that returns DSML tool-call text for rewrite testing."""

from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import sys

DSML = (
    "我来扫描。\n\n"
    "<｜｜DSML｜｜tool_calls>\n"
    "<｜｜DSML｜｜invoke name=\"Bash\">\n"
    "<｜｜DSML｜｜parameter name=\"command\" string=\"true\">ls -la</｜｜DSML｜｜parameter>\n"
    "</｜｜DSML｜｜invoke>\n"
    "</｜｜DSML｜｜tool_calls>"
)


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(n)
        try:
            req = json.loads(body)
        except Exception:
            req = {}
        stream = bool(req.get("stream"))
        if stream:
            chunks = [
                'data: {"type":"message_start","message":{"id":"msg_m","model":"deepseek-v4-flash-free","usage":{"input_tokens":3,"output_tokens":0}}}\n\n',
                "data: "
                + json.dumps(
                    {
                        "type": "content_block_delta",
                        "index": 0,
                        "delta": {"type": "text_delta", "text": DSML},
                    },
                    ensure_ascii=False,
                )
                + "\n\n",
                'data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}\n\n',
                'data: {"type":"message_stop"}\n\n',
            ]
            raw = "".join(chunks).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)
            return

        resp = {
            "id": "msg_m",
            "type": "message",
            "role": "assistant",
            "model": "deepseek-v4-flash-free",
            "content": [{"type": "text", "text": DSML}],
            "stop_reason": "end_turn",
            "usage": {"input_tokens": 3, "output_tokens": 12},
        }
        raw = json.dumps(resp, ensure_ascii=False).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *_args):
        pass


def main():
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 18081
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
