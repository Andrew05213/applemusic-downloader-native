#!/usr/bin/env python3
"""Fixed loopback-only responses for the test-only native ABI harness."""

from __future__ import annotations

import argparse
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path


HOST = "127.0.0.1"
PORT = 18911


class FixtureServer(HTTPServer):
    allow_reuse_address = True


class FixtureHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, _format: str, *_args: object) -> None:
        pass

    def send_payload(self, status: int, body: bytes) -> None:
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        if body:
            self.wfile.write(body)

    def do_GET(self) -> None:
        if self.path == "/fixture/get":
            self.send_payload(200, b'{"ok":"get"}')
            return
        if self.path == "/fixture/empty":
            self.send_payload(200, b"")
            return
        if self.path == "/fixture/invalid-json":
            self.send_payload(200, b"not-json")
            return
        if self.path == "/fixture/remote-error":
            self.send_payload(500, b'{"error":"fixture"}')
            return
        self.send_payload(404, b'{"error":"not-found"}')

    def do_POST(self) -> None:
        try:
            length = int(self.headers.get("Content-Length", "-1"))
        except ValueError:
            length = -1
        body = self.rfile.read(length) if 0 <= length <= 16 else b""
        if (self.path == "/fixture/post" and self.headers.get("Content-Type") == "application/json"
                and body == b"{}"):
            self.send_payload(200, b'{"ok":"post"}')
            return
        self.send_payload(400, b'{"error":"invalid-post"}')


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--ready-file", required=True)
    arguments = parser.parse_args()
    ready_file = Path(arguments.ready_file)
    with FixtureServer((HOST, PORT), FixtureHandler) as server:
        ready_file.write_text("ready\n", encoding="ascii")
        server.serve_forever()


if __name__ == "__main__":
    main()
