#!/bin/sh
set -eu
if [ ! -f /var/lib/just-code/toolchain-ready ]; then
  apk add --no-cache bash build-base ca-certificates curl git nodejs npm procps python3 py3-pip
  mkdir -p /var/lib/just-code
  touch /var/lib/just-code/toolchain-ready
fi
# The base image contains the legacy v1 CLI. Install the managed v2 release
# before either serving the backend or announcing full-mode readiness.
if [ "$(opencode --version 2>/dev/null || true)" != "opencode v${JUST_CODE_OPENCODE_VERSION}" ]; then
  # Private prefix avoids the v1 image's unmanaged /usr/local/bin/opencode
  # and npm's EEXIST when two package namespaces expose the same bin.
  npm install --global --prefix /opt/just-code/opencode "@opencode/cli@${JUST_CODE_OPENCODE_VERSION}"
  ln -sf /opt/just-code/opencode/bin/opencode /usr/local/bin/opencode
fi
test "$(opencode --version)" = "opencode v${JUST_CODE_OPENCODE_VERSION}"
# The identity configured by the global setup (P11) reaches the guest through
# the sandbox environment; these defaults keep an unconfigured machine usable.
git config --global user.name "${JUST_CODE_GIT_NAME:-Albert Code Agent}"
git config --global user.email "${JUST_CODE_GIT_EMAIL:-albert-code@noreply.etalab.gouv.fr}"
git config --global --add safe.directory '*'
