#!/usr/bin/env python3
"""Phase 3 qualification guest application (TEST FIXTURE ONLY).

Reads a codebox-managed dotenv file with the REAL python-dotenv==1.2.4 and
issues one HTTPS request to the test lab carrying the parsed value in a
header. The file holds an opaque marker; the sandbox proxy — not this app —
substitutes the real value at the network boundary, and only toward the
approved destination.

usage: python3 app.py <dotenv-path> <url>
"""
import ssl
import sys
import urllib.request

from dotenv import dotenv_values


def main():
    if len(sys.argv) != 3:
        print("usage: app.py <dotenv-path> <url>", file=sys.stderr)
        return 2
    dotenv_path, url = sys.argv[1], sys.argv[2]
    # dotenv_values reads the file itself; the marker is dollar-free, so
    # interpolation cannot rewrite it from the process environment.
    values = dotenv_values(dotenv_path, interpolate=True)
    value = values.get("API_KEY")
    if not value:
        print("API_KEY missing from " + dotenv_path, file=sys.stderr)
        return 2
    # The value here is the marker, not a credential: printing it lets the
    # probe prove the app parsed the file before substitution happens.
    print("API_KEY=" + value)
    request = urllib.request.Request(url, headers={"X-Api-Key": value})
    # The proxy intercepts TLS with the run's temporary CA; like the Phase 0
    # guest, this fixture disables client verification instead of integrating
    # that CA into the guest store. Upstream verification stays with the proxy.
    context = ssl._create_unverified_context()
    try:
        with urllib.request.urlopen(request, context=context, timeout=12) as response:
            response.read()
            print("HTTP_STATUS=" + str(response.status))
            return 0 if response.status == 200 else 1
    except Exception as error:
        # Exception text can carry the URL, never the header value.
        print("REQUEST_FAILED=" + type(error).__name__, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
