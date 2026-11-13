"""Unit tests for the P02 lab DNS responses."""

import struct
import unittest

from dns import response_for_query


def query(name, qtype, qclass=1):
    qname = b"".join(
        bytes((len(label),)) + label.encode("ascii") for label in name.split(".")
    ) + b"\0"
    return (
        b"\x12\x34"
        + struct.pack(">HHHHH", 0x0100, 1, 0, 0, 0)
        + qname
        + struct.pack(">HH", qtype, qclass)
    )


class DNSResponseTests(unittest.TestCase):
    def test_known_aaaa_query_returns_nodata_and_echoes_question(self):
        request = query("p02lab.test", 28)
        response = response_for_query(request, {"p02lab.test": "192.0.2.1"})

        _, flags, question_count, answer_count, authority_count, additional_count = (
            struct.unpack(">HHHHHH", response[:12])
        )
        self.assertEqual(flags, 0x8180)
        self.assertEqual(
            (question_count, answer_count, authority_count, additional_count),
            (1, 0, 0, 0),
        )
        self.assertEqual(response[12:], request[12:])

    def test_unknown_aaaa_query_remains_refused(self):
        request = query("unknown.test", 28)
        response = response_for_query(request, {"p02lab.test": "192.0.2.1"})
        flags = struct.unpack(">H", response[2:4])[0]

        self.assertEqual(flags, 0x8185)
        self.assertEqual(response[12:], request[12:])


if __name__ == "__main__":
    unittest.main()
