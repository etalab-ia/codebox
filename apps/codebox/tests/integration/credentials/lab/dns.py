#!/usr/bin/env python3
"""DNS UDP server for the P02 credential lab.

Answers p02lab.test and evil.test with the host LAN IP. Returns NODATA for
AAAA queries to those names and REFUSED (rcode 5) for other names so the
resolver falls back to 1.1.1.1. See the suite README.
"""

import socket
import struct
import sys


def parse_name(data, offset):
    labels = []
    while True:
        length = data[offset]
        if length == 0:
            offset += 1
            break
        labels.append(data[offset + 1:offset + 1 + length].decode("latin-1"))
        offset += 1 + length
    return ".".join(labels), offset


def response_for_query(data, known):
    if len(data) < 12:
        return None
    qid = data[:2]
    try:
        qname, offset = parse_name(data, 12)
        qtype, qclass = struct.unpack(">HH", data[offset:offset + 4])
    except (IndexError, struct.error):
        return None

    if qname in known and qtype == 1:
        return (
            qid
            + struct.pack(">HHHHH", 0x8180, 1, 1, 0, 0)
            + data[12:offset + 4]
            + b"\xc0\x0c"
            + struct.pack(">HHIH", 1, 1, 60, 4)
            + socket.inet_aton(known[qname])
        )
    if qname in known and qtype == 28 and qclass == 1:
        return (
            qid
            + struct.pack(">HHHHH", 0x8180, 1, 0, 0, 0)
            + data[12:offset + 4]
        )
    return qid + struct.pack(">HHHHH", 0x8185, 1, 0, 0, 0) + data[12:offset + 4]


def main():
    host_ip = sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1"
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("0.0.0.0", 5354))
    known = {"p02lab.test": host_ip, "evil.test": host_ip}
    print(f"dns lab on :5354, known={list(known)}", flush=True)
    while True:
        data, addr = sock.recvfrom(512)
        answer = response_for_query(data, known)
        if answer is None:
            continue
        sock.sendto(answer, addr)


if __name__ == "__main__":
    main()
