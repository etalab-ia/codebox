#!/bin/sh
set -eu

toolchain_marker=/var/lib/codebox/toolchain-ready
browser_marker=/var/lib/codebox/browser-mcps-ready
mkdir -p /var/lib/codebox

if [ ! -f "$toolchain_marker" ]; then
  rm -f /etc/apt/sources.list.d/debian.sources
  printf '%s\n' \
    "deb [check-valid-until=no] https://snapshot.debian.org/archive/debian/${CODEBOX_APT_SNAPSHOT} bookworm main" \
    "deb [check-valid-until=no] https://snapshot.debian.org/archive/debian-security/${CODEBOX_APT_SNAPSHOT} bookworm-security main" \
    > /etc/apt/sources.list
  apt-get -o Acquire::Check-Valid-Until=false update
  case "$(uname -m)" in
    x86_64|amd64)
      chromium_package_version=$CODEBOX_CHROMIUM_PACKAGE_VERSION_AMD64
      chromium_version=$CODEBOX_CHROMIUM_VERSION_AMD64
      ;;
    aarch64|arm64)
      chromium_package_version=$CODEBOX_CHROMIUM_PACKAGE_VERSION_ARM64
      chromium_version=$CODEBOX_CHROMIUM_VERSION_ARM64
      ;;
    *)
      echo "Chromium is not qualified for $(uname -m)" >&2
      exit 1
      ;;
  esac
  apt-get install -y --no-install-recommends \
    ca-certificates curl git xz-utils \
    "chromium=${chromium_package_version}" \
    "fonts-liberation=${CODEBOX_FONTS_LIBERATION_VERSION}"

  case "$(uname -m)" in
    x86_64|amd64)
      node_arch=x64
      node_sha256=$CODEBOX_NODE_SHA256_AMD64
      ;;
    aarch64|arm64)
      node_arch=arm64
      node_sha256=$CODEBOX_NODE_SHA256_ARM64
      ;;
    *)
      echo "Node.js ${CODEBOX_NODE_VERSION} is not qualified for $(uname -m)" >&2
      exit 1
      ;;
  esac
  node_archive="/tmp/node-v${CODEBOX_NODE_VERSION}-linux-${node_arch}.tar.xz"
  curl -fsSL "https://nodejs.org/dist/v${CODEBOX_NODE_VERSION}/node-v${CODEBOX_NODE_VERSION}-linux-${node_arch}.tar.xz" -o "$node_archive"
  printf '%s  %s\n' "$node_sha256" "$node_archive" | sha256sum -c -
  tar -xJf "$node_archive" --strip-components=1 -C /usr/local
  rm -f "$node_archive"

  test "$(node --version)" = "v${CODEBOX_NODE_VERSION}"
  test "$(npm --version)" = "$CODEBOX_NODE_NPM_VERSION"
  npm install --global --prefix /opt/codebox/opencode "@opencode/cli@${CODEBOX_OPENCODE_VERSION}"
  ln -sf /opt/codebox/opencode/bin/opencode /usr/local/bin/opencode
  test "$(opencode --version)" = "opencode v$CODEBOX_OPENCODE_VERSION"
  test "$(dpkg-query -W -f='${Version}' chromium)" = "$chromium_package_version"
  test "$(dpkg-query -W -f='${Version}' fonts-liberation)" = "$CODEBOX_FONTS_LIBERATION_VERSION"
  test "$(chromium --version | sed 's/^Chromium //; s/ .*//')" = "$chromium_version"

  printf '%s\n' "$CODEBOX_BROWSER_PROFILE" > "${toolchain_marker}.tmp"
  mv "${toolchain_marker}.tmp" "$toolchain_marker"
fi

current_fingerprint=
if [ -f "$browser_marker" ]; then
  current_fingerprint=$(cat "$browser_marker")
fi
if [ -n "${CODEBOX_BROWSER_MCP_FINGERPRINT:-}" ] && [ "$current_fingerprint" != "$CODEBOX_BROWSER_MCP_FINGERPRINT" ]; then
  for package in ${CODEBOX_BROWSER_MCP_PACKAGES:-}; do
    npm install --global "$package"
  done
  for binary in ${CODEBOX_BROWSER_MCP_BINARIES:-}; do
    command -v "$binary" >/dev/null
  done
  printf '%s\n' "$CODEBOX_BROWSER_MCP_FINGERPRINT" > "${browser_marker}.tmp"
  mv "${browser_marker}.tmp" "$browser_marker"
fi

git config --global user.name "${CODEBOX_GIT_NAME:-Albert Code Agent}"
git config --global user.email "${CODEBOX_GIT_EMAIL:-albert-code@noreply.etalab.gouv.fr}"
git config --global --add safe.directory '*'
