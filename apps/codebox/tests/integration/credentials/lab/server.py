#!/usr/bin/env python3
"""Echo HTTPS server for the P02 credential lab.

Listens on 0.0.0.0:8443, logs each request as JSON ({method, path, headers,
body}) to requests.jsonl, and answers 302 to https://evil.test:8443/
redirect-target on /redirect-to-evil. See the suite README for the contract.
"""

import datetime
import json
import ssl
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class EchoHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _handle(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length).decode("utf-8", "replace") if length else ""
        entry = {
            "time": datetime.datetime.utcnow().isoformat(),
            "method": self.command,
            "path": self.path,
            "headers": {k.lower(): v for k, v in self.headers.items()},
            "body": body,
        }
        with open("requests.jsonl", "a") as fh:
            fh.write(json.dumps(entry) + "\n")
        if self.path == "/redirect-to-evil":
            self.send_response(302)
            self.send_header("Location", "https://evil.test:8443/redirect-target")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        payload = b'{"status":"echo"}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    do_GET = _handle
    do_POST = _handle
    do_HEAD = _handle

    def log_message(self, *args):
        pass


def main():
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8443
    httpd = ThreadingHTTPServer(("0.0.0.0", port), EchoHandler)
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain("srv.crt", "srv.key")
    httpd.socket = ctx.wrap_socket(httpd.socket, server_side=True)
    with open("requests.jsonl", "w"):
        pass
    httpd.serve_forever()


if __name__ == "__main__":
    main()
