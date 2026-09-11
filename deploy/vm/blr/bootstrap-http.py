#!/usr/bin/env python3
"""Temporary HTTP-01 webroot listener used before the API has a certificate."""
import functools
import http.server
import sys


class ChallengeHandler(http.server.SimpleHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    # Send the small body immediately, even when the validator requests close.
    disable_nagle_algorithm = True
    timeout = 10


if __name__ == "__main__":
    handler = functools.partial(ChallengeHandler, directory=sys.argv[1])
    with http.server.ThreadingHTTPServer(("0.0.0.0", 80), handler) as server:
        server.serve_forever()
