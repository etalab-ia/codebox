#!/bin/sh
# agentvm-base-prep: provisions the codebox agent-vm base template.
#
# Runs inside the base VM as the regular user (piped in by
# `limactl shell <template> sh -s`), invoking sudo for system-level changes.
# Running as the user matters: OpenCode installs under $HOME, which is root's
# home if the script itself runs as root.
#
# codebox builds this template itself so it never has to execute a
# third-party provisioning script that cannot be pinned to a release.
#
# Scope is deliberately minimal: base packages plus the toolchain the backend
# needs. Anything else (Docker, Chromium, other agent CLIs, MCP wiring)
# belongs in a user-maintained template referenced through AGENT_VM_TEMPLATE.
set -eu

export DEBIAN_FRONTEND=noninteractive
SUDO="sudo -n"

# Debian's needrestart can prompt mid-upgrade about restarting services, which
# would hang a non-interactive provisioning run. Same guard agent-vm uses.
# shellcheck disable=SC2016  # Perl syntax, must stay literal.
$SUDO mkdir -p /etc/needrestart/conf.d
$SUDO tee /etc/needrestart/conf.d/no-prompt.conf > /dev/null <<'NRCONF'
$nrconf{restart} = 'a';
NRCONF

# Node floor required by the pi and letta coding agents (see the project
# design notes). Debian 13 ships Node 20, which is below that floor, so Node
# comes from NodeSource rather than apt.
NODE_MAJOR=24

echo "codebox: installing base packages..."
# unzip and tar are required by the OpenCode installer; ca-certificates and
# curl by NodeSource and by the installer's own downloads.
$SUDO apt-get update
$SUDO apt-get install -y --no-install-recommends \
  ca-certificates \
  curl \
  git \
  jq \
  sudo \
  tar \
  unzip \
  xz-utils

# PATH for interactive login shells. The authoritative mechanism is the
# /usr/local/bin symlink created below (it is in every shell's default PATH);
# this block only keeps the private install directories visible in a shell a
# user opens themselves, and mirrors what the OpenCode installer would have
# written had we let it touch the shell rc files.
$SUDO tee /etc/profile.d/codebox.sh > /dev/null <<'PROFILE'
# Managed by codebox. Keeps the per-user toolchain directories on PATH.
if [ -d "$HOME/.opencode/bin" ]; then
  PATH="$HOME/.opencode/bin:$PATH"
fi
if [ -d "$HOME/.local/bin" ]; then
  PATH="$HOME/.local/bin:$PATH"
fi
export PATH
PROFILE
$SUDO chmod 0644 /etc/profile.d/codebox.sh

echo "codebox: installing Node.js ${NODE_MAJOR}..."
# `sudo -E` keeps DEBIAN_FRONTEND set for the setup script's apt calls.
curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | $SUDO -E bash -
$SUDO apt-get install -y --no-install-recommends nodejs

echo "codebox: installing OpenCode..."
# --no-modify-path: codebox owns PATH below. Letting the installer edit shell
# rc files would tie the install to the installer's shell detection.
curl -fsSL https://opencode.ai/v2/install | bash -s -- --version 2.2.5 --no-modify-path

# Fail loudly here rather than at backend launch: a base template without
# OpenCode is unusable, and the failure would otherwise surface much later as
# an opaque health timeout.
if [ ! -x "$HOME/.opencode/bin/opencode" ]; then
  echo "codebox: OpenCode did not install to \$HOME/.opencode/bin/opencode" >&2
  exit 1
fi

# Put the binary on the system PATH. /usr/local/bin is in the default PATH of
# every shell — login or not, bash or sh, interactive or not — so the backend
# launch finds `opencode` without depending on which startup files a given
# shell happens to read. The installer's own $HOME/.opencode/bin is left in
# place, and the profile.d entry below still covers interactive shells.
$SUDO ln -sf "$HOME/.opencode/bin/opencode" /usr/local/bin/opencode

# Verify the system-wide path resolves, not just the private one: this is the
# one the backend actually uses.
if ! command -v opencode > /dev/null 2>&1; then
  echo "codebox: opencode is not resolvable on the system PATH after install" >&2
  exit 1
fi

echo "codebox: base template provisioning complete."