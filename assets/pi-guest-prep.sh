#!/bin/sh
# pi-guest-prep: prepares a Microsandbox guest for the Pi harness (#176).
#
# The guest image is a generic Alpine base with the managed toolchain; unlike
# the OpenCode image it ships no agent CLI, so the pinned Pi release is
# installed from npm here. The version comes from the sandbox environment
# (JUST_CODE_PI_VERSION): the guest never invents one.
set -eu

if [ ! -f /var/lib/just-code/toolchain-ready ]; then
  apk add --no-cache bash build-base ca-certificates curl git nodejs npm procps python3 py3-pip
  mkdir -p /var/lib/just-code
  touch /var/lib/just-code/toolchain-ready
fi

# A private prefix keeps the managed install separate from anything a user's
# own template might ship under the default npm prefix.
if [ "$(pi --version 2>/dev/null || true)" != "${JUST_CODE_PI_VERSION}" ]; then
  npm install --global --prefix /opt/just-code/pi "@mariozechner/pi-coding-agent@${JUST_CODE_PI_VERSION}"
  ln -sf /opt/just-code/pi/bin/pi /usr/local/bin/pi
fi
test "$(pi --version)" = "${JUST_CODE_PI_VERSION}"

# The managed configuration is non-secret and travels through the sandbox
# environment, which the runtime keeps separate from the provider key: the key
# stays a network-proxy secret and only ever reaches the Albert host.
if [ -z "${HOME:-}" ]; then
  echo "pi-guest-prep: HOME is not set in the guest" >&2
  exit 1
fi
if [ -z "${JUST_CODE_PI_MODELS_JSON:-}" ] || [ -z "${JUST_CODE_PI_SETTINGS_JSON:-}" ]; then
  echo "pi-guest-prep: the managed Pi configuration is missing from the guest environment" >&2
  exit 1
fi
mkdir -p "$HOME/.pi/agent"
printf '%s\n' "${JUST_CODE_PI_MODELS_JSON}" > "$HOME/.pi/agent/models.json"
printf '%s\n' "${JUST_CODE_PI_SETTINGS_JSON}" > "$HOME/.pi/agent/settings.json"
chmod 600 "$HOME/.pi/agent/models.json" "$HOME/.pi/agent/settings.json"

# The identity configured by the global setup (P11) reaches the guest through
# the sandbox environment; these defaults keep an unconfigured machine usable.
git config --global user.name "${JUST_CODE_GIT_NAME:-Albert Code Agent}"
git config --global user.email "${JUST_CODE_GIT_EMAIL:-albert-code@noreply.etalab.gouv.fr}"
git config --global --add safe.directory '*'
