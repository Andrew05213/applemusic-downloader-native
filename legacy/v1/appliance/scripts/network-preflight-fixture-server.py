#!/usr/bin/env python3
"""One-shot loopback HTTPS-over-CONNECT fixture for the native CA harness."""

from __future__ import annotations

import argparse
import json
import pathlib
import socket
import socketserver
import ssl
import sys


HOST = "127.0.0.1"
EXPECTED_CONNECT_TARGET = b"init.itunes.apple.com:443"
MAX_HEADER_BYTES = 16 * 1024
BAG_BODY = (
    b'<?xml version="1.0" encoding="UTF-8"?>\n'
    b'<plist version="1.0"><dict><key>authenticateAccount</key>'
    b'<string>https://fixture.invalid/authenticate</string></dict></plist>\n'
)


def read_headers(connection: socket.socket) -> bytes:
    data = bytearray()
    while b"\r\n\r\n" not in data:
        chunk = connection.recv(min(4096, MAX_HEADER_BYTES - len(data)))
        if not chunk:
            raise ValueError("unexpected EOF")
        data.extend(chunk)
        if len(data) >= MAX_HEADER_BYTES:
            raise ValueError("headers exceed fixed limit")
    return bytes(data)


class FixtureServer(socketserver.TCPServer):
    allow_reuse_address = True

    def __init__(self, address: tuple[str, int], context: ssl.SSLContext):
        self.context = context
        self.result = {"connect": False, "tls": False, "request": False}
        super().__init__(address, FixtureHandler)


class FixtureHandler(socketserver.BaseRequestHandler):
    def handle(self) -> None:
        server = self.server
        assert isinstance(server, FixtureServer)
        request = read_headers(self.request)
        first_line = request.split(b"\r\n", 1)[0].split()
        if len(first_line) != 3 or first_line[0] != b"CONNECT" or first_line[1] != EXPECTED_CONNECT_TARGET:
            raise ValueError("unexpected CONNECT target")
        server.result["connect"] = True
        self.request.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
        with server.context.wrap_socket(self.request, server_side=True) as tls:
            server.result["tls"] = True
            request = read_headers(tls)
            first_line = request.split(b"\r\n", 1)[0].split()
            if len(first_line) != 3 or first_line[0] != b"GET":
                raise ValueError("unexpected HTTPS request")
            server.result["request"] = True
            response = (
                b"HTTP/1.1 200 OK\r\n"
                b"Content-Type: application/xml\r\n"
                + f"Content-Length: {len(BAG_BODY)}\r\n".encode("ascii")
                + b"Connection: close\r\n\r\n"
                + BAG_BODY
            )
            tls.sendall(response)


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("--cert", required=True)
    parser.add_argument("--key", required=True)
    parser.add_argument("--ready-file", required=True)
    parser.add_argument("--result-file", required=True)
    return parser.parse_args()


def main() -> int:
    args = parse_arguments()
    cert = pathlib.Path(args.cert)
    key = pathlib.Path(args.key)
    ready = pathlib.Path(args.ready_file)
    result = pathlib.Path(args.result_file)
    if not cert.is_file() or not key.is_file() or ready.exists() or result.exists():
        return 2
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    context.load_cert_chain(certfile=str(cert), keyfile=str(key))
    try:
        with FixtureServer((HOST, 0), context) as server:
            server.timeout = 20
            ready.write_text(str(server.server_address[1]) + "\n", encoding="ascii")
            server.handle_request()
            result.write_text(json.dumps(server.result, sort_keys=True) + "\n", encoding="ascii")
            return 0
    except (OSError, ValueError, ssl.SSLError):
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
