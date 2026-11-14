"""Exercise the real TLS server and its per-run journal lifecycle."""

import json
import pathlib
import socket
import ssl
import subprocess
import tempfile
import time
import unittest
import urllib.request


class JournalTests(unittest.TestCase):
    def test_occupied_port_preserves_previous_journal(self):
        here = pathlib.Path(__file__).resolve().parent
        with tempfile.TemporaryDirectory() as directory, socket.socket() as blocker:
            lab = pathlib.Path(directory)
            journal = lab / "requests.jsonl"
            evidence = '{"path": "/previous-run"}\n'
            journal.write_text(evidence)
            blocker.bind(("0.0.0.0", 0))
            blocker.listen()
            result = subprocess.run(
                ["python3", str(here / "server.py"), str(blocker.getsockname()[1])],
                cwd=lab, capture_output=True, timeout=5)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(journal.read_text(), evidence)

    def test_restart_discards_stale_evidence_and_keeps_stderr_separate(self):
        here = pathlib.Path(__file__).resolve().parent
        with tempfile.TemporaryDirectory() as directory:
            lab = pathlib.Path(directory)
            subprocess.run(["sh", str(here / "setup.sh"), "127.0.0.1"],
                           cwd=lab, check=True, capture_output=True)
            journal = lab / "requests.jsonl"
            for attempt in range(2):
                journal.write_text('{"path": "/t4", "headers": {"authorization": "stale"}}\n')
                with socket.socket() as sock:
                    sock.bind(("127.0.0.1", 0))
                    port = sock.getsockname()[1]
                with (lab / "server.log").open("w") as log:
                    server = subprocess.Popen(["python3", str(here / "server.py"), str(port)],
                                              cwd=lab, stdout=log, stderr=log)
                    try:
                        deadline = time.monotonic() + 5
                        while journal.read_text() and time.monotonic() < deadline:
                            if server.poll() is not None:
                                self.fail("server exited before initializing journal")
                            time.sleep(0.02)
                        self.assertEqual(journal.read_text(), "")
                        context = ssl.create_default_context(cafile=str(lab / "ca.crt"))
                        request = urllib.request.Request(
                            f"https://127.0.0.1:{port}/fresh-{attempt}",
                            headers={"Authorization": "Bearer fresh"})
                        with urllib.request.urlopen(request, context=context, timeout=3) as response:
                            self.assertEqual(response.status, 200)
                        entries = [json.loads(line) for line in journal.read_text().splitlines()]
                        self.assertEqual(len(entries), 1)
                        self.assertEqual(entries[0]["path"], f"/fresh-{attempt}")
                        self.assertEqual(entries[0]["headers"]["authorization"], "Bearer fresh")
                    finally:
                        server.terminate()
                        server.wait(timeout=5)


if __name__ == "__main__":
    unittest.main()
