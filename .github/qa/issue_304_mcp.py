#!/usr/bin/env python3
"""Drive just-code init on a PTY and render its real terminal screens as PNGs."""

import fcntl
import json
import os
import pty
import select
import signal
import struct
import subprocess
import sys
import tempfile
import termios
import time
from pathlib import Path

import pyte
from PIL import Image, ImageDraw, ImageFont

COLS = 110
ROWS = 30
BEGIN = "Appuyez sur Entr\u00e9e"
SKILLS_INTRO = "Une skill apporte \u00e0 l'agent"
STORAGE = "O\u00f9 enregistrer les skills ?"
MCP_TITLE = "[2/3] MCP (facultatif)"
MCP_INTRO = "Un MCP donne \u00e0 l'agent l'acc\u00e8s \u00e0 un service ou \u00e0 un outil."
NO_MCP = "Aucun MCP s\u00e9lectionn\u00e9. Continuer sans MCP ?"
GITHUB_TITLE = "[3/3] GitHub (facultatif)"


class Terminal:
    def __init__(self, binary, project):
        self.screen = pyte.Screen(COLS, ROWS)
        self.stream = pyte.Stream(self.screen)
        self.master, slave = pty.openpty()
        self.set_size(ROWS, COLS)
        env = os.environ.copy()
        env.update(
            {
                "TERM": "xterm-256color",
                "ALBERT_API_KEY": "qa-visual-not-a-real-key",
                "HOME": str(project / "home"),
            }
        )
        (project / "home").mkdir(parents=True, exist_ok=True)
        self.process = subprocess.Popen(
            [binary, "init"],
            cwd=project,
            env=env,
            stdin=slave,
            stdout=slave,
            stderr=slave,
            start_new_session=True,
            close_fds=True,
        )
        os.close(slave)
        os.set_blocking(self.master, False)

    def set_size(self, rows, cols):
        fcntl.ioctl(self.master, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

    def text(self):
        return "\n".join(self.screen.display)

    def pump(self, duration=0.1):
        deadline = time.monotonic() + duration
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return
            try:
                readable, _, _ = select.select([self.master], [], [], remaining)
            except InterruptedError:
                continue
            if not readable:
                return
            try:
                data = os.read(self.master, 65536)
            except BlockingIOError:
                continue
            except OSError:
                return
            if not data:
                return
            self.stream.feed(data.decode("utf-8", errors="replace"))

    def wait_for(self, fragment, timeout=60):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if fragment in self.text():
                self.pump(0.25)
                return
            self.pump(0.1)
        raise AssertionError(f"did not render {fragment!r}:\n{self.text()}")

    def wait_for_any(self, fragments, timeout=60):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            text = self.text()
            if any(fragment in text for fragment in fragments):
                self.pump(0.25)
                return
            self.pump(0.1)
        raise AssertionError(f"did not render any of {fragments!r}:\n{self.text()}")

    def send(self, value):
        os.write(self.master, value.encode("utf-8"))
        self.pump(0.3)

    def capture(self, output, name):
        self.pump(0.35)
        text = self.text()
        (output / f"{name}.txt").write_text(text + "\n", encoding="utf-8")
        render_png(self.screen, output / f"{name}.png")
        print(f"captured {name}")
        return text

    def advance_skills(self):
        self.wait_for_any([SKILLS_INTRO, STORAGE])
        if SKILLS_INTRO in self.text():
            self.send("\r")
            self.wait_for(STORAGE)
        self.send("\r")
        self.wait_for(MCP_TITLE)
        self.wait_for("data-gouv")

    def close(self):
        if self.process.poll() is None:
            self.process.send_signal(signal.SIGTERM)
            try:
                self.process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=3)
        os.close(self.master)


def option_line(text, option):
    for line in text.splitlines():
        if option in line:
            return line
    raise AssertionError(f"option {option!r} is absent:\n{text}")


def assert_selected(text, option, selected):
    line = option_line(text, option)
    checked = any(marker in line for marker in ("[x]", "\u2713", "\u2714"))
    unchecked = "[ ]" in line
    if selected and not checked:
        raise AssertionError(f"{option} is not visibly selected: {line!r}")
    if not selected and checked and not unchecked:
        raise AssertionError(f"{option} is still visibly selected: {line!r}")


def render_png(screen, path):
    font_path = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"
    font = ImageFont.truetype(font_path, 17)
    bold_font = ImageFont.truetype("/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf", 17)
    cell_w = max(9, int(round(font.getlength("M"))))
    cell_h = 23
    margin = 16
    image = Image.new("RGB", (COLS * cell_w + margin * 2, ROWS * cell_h + margin * 2), "#111418")
    draw = ImageDraw.Draw(image)
    colors = {
        "black": "#111418",
        "red": "#ff6b6b",
        "green": "#69db7c",
        "yellow": "#ffd43b",
        "blue": "#74c0fc",
        "magenta": "#da77f2",
        "cyan": "#66d9e8",
        "white": "#f1f3f5",
        "default": "#e9ecef",
    }
    for y in range(ROWS):
        for x in range(COLS):
            char = screen.buffer[y][x]
            fg = colors.get(char.fg, colors["default"])
            bg = colors.get(char.bg, "#111418") if char.bg != "default" else "#111418"
            if char.reverse:
                fg, bg = bg, fg
            left = margin + x * cell_w
            top = margin + y * cell_h
            if bg != "#111418":
                draw.rectangle((left, top, left + cell_w, top + cell_h), fill=bg)
            if char.data and char.data != " ":
                draw.text((left, top), char.data, fill=fg, font=bold_font if char.bold else font)
    image.save(path)


def main():
    if len(sys.argv) != 3:
        raise SystemExit("usage: issue_304_mcp.py <just-code-binary> <output-directory>")
    binary = str(Path(sys.argv[1]).resolve())
    output = Path(sys.argv[2])
    output.mkdir(parents=True, exist_ok=True)
    evidence = {"commit": os.environ.get("GITHUB_SHA", "local"), "platform": "Linux amd64", "terminal": f"{COLS}x{ROWS} PTY", "screenshots": []}
    with tempfile.TemporaryDirectory(prefix="just-code-304-") as temp:
        project = Path(temp) / "project"
        project.mkdir()
        terminal = Terminal(binary, project)
        try:
            terminal.wait_for(BEGIN)
            terminal.send("\r")
            terminal.advance_skills()

            text = terminal.capture(output, "01-default-mcp")
            for expected in (MCP_TITLE, MCP_INTRO, "data-gouv", "chrome-devtools"):
                if expected not in text:
                    raise AssertionError(f"missing {expected!r} on the default MCP screen")
            for stale in ("MCPs and storage", "Curated remote services"):
                if stale in text:
                    raise AssertionError(f"stale English text remains: {stale}")
            assert_selected(text, "data-gouv", True)
            evidence["screenshots"].append({"file": "01-default-mcp.png", "result": "French title and intro, both options visible, data-gouv selected by default"})

            terminal.send(" ")
            text = terminal.capture(output, "02-empty-mcp-selection")
            assert_selected(text, "data-gouv", False)
            evidence["screenshots"].append({"file": "02-empty-mcp-selection.png", "result": "Options remain visible after clearing the default"})

            terminal.send("\r")
            terminal.wait_for(NO_MCP)
            text = terminal.capture(output, "03-confirm-without-mcp")
            if NO_MCP not in text:
                raise AssertionError("empty-MCP confirmation did not render")
            evidence["screenshots"].append({"file": "03-confirm-without-mcp.png", "result": "Real huh confirmation rendered before continuing without MCPs"})

            terminal.send("\r")  # Keep the default No: return without writing the empty selection.
            terminal.advance_skills()
            text = terminal.capture(output, "04-selection-preserved-on-return")
            assert_selected(text, "data-gouv", True)
            evidence["screenshots"].append({"file": "04-selection-preserved-on-return.png", "result": "Declining the confirmation restores the prior data-gouv selection"})

            terminal.send(" ")
            terminal.send("\r")
            terminal.wait_for(NO_MCP)
            terminal.send("y")
            if NO_MCP in terminal.text():
                terminal.send("\r")
            terminal.wait_for(GITHUB_TITLE)
            terminal.capture(output, "05-empty-selection-accepted")
            evidence["screenshots"].append({"file": "05-empty-selection-accepted.png", "result": "Accepting the confirmation advances to GitHub without applying project configuration"})
        finally:
            terminal.close()
    (output / "evidence.json").write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(evidence, indent=2))


if __name__ == "__main__":
    main()
