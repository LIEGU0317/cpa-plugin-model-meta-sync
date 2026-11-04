#!/usr/bin/env python3
"""Minimal OpenAI-compatible upstream used by scripts/smoke-test.sh.

Serves a single model with the metadata fields the plugin maps, so the smoke
test exercises a real fetch -> patch -> write cycle instead of only checking
that the plugin registered.
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

MODELS = {
    "object": "list",
    "data": [
        {
            "id": "smoke-model",
            "display_name": "Smoke Model",
            "context_length": 65536,
            "modalities": {"input": ["text"], "output": ["text"]},
            "reasoning": True,
            "reasoning_levels": ["low", "high"],
        }
    ],
}


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.rstrip("/") in ("/v1/models", "/models"):
            body = json.dumps(MODELS).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(404)
        self.end_headers()

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 19091
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()
