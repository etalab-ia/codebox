#!/bin/sh
# Exercise the production NSS import with real Chromium and synthetic CAs.
set -eu

trust_script=${1:?usage: browser-proxy-ca.sh <browser-guest-nss-trust.sh>}
for tool in chromium certutil curl openssl; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "$tool is required for the Chromium CA trust test" >&2
    exit 2
  }
done

lab=$(mktemp -d)
trusted_server_pid=
untrusted_server_pid=
cleanup() {
  for pid in "$trusted_server_pid" "$untrusted_server_pid"; do
    if [ -n "$pid" ]; then
      kill "$pid" >/dev/null 2>&1 || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  rm -rf "$lab"
}
trap cleanup EXIT HUP INT TERM

generate_ca() {
  name=$1
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
    -keyout "$lab/$name-ca.key" -out "$lab/$name-ca.crt" \
    -subj "/CN=Codebox $name browser test CA" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" >/dev/null 2>&1
}

generate_server() {
  name=$1
  openssl req -newkey rsa:2048 -nodes \
    -keyout "$lab/$name-server.key" -out "$lab/$name-server.csr" \
    -subj "/CN=127.0.0.1" >/dev/null 2>&1
  openssl x509 -req -in "$lab/$name-server.csr" \
    -CA "$lab/$name-ca.crt" -CAkey "$lab/$name-ca.key" -CAcreateserial \
    -out "$lab/$name-server.crt" -days 1 \
    -extfile "$lab/server.ext" >/dev/null 2>&1
}

cat > "$lab/server.ext" <<'EOF'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=IP:127.0.0.1
EOF

generate_ca trusted
generate_ca untrusted
generate_server trusted
generate_server untrusted

mkdir -p "$lab/trusted-root" "$lab/untrusted-root" "$lab/home"
printf '%s\n' 'codebox-trusted-ca-ok' > "$lab/trusted-root/index.html"
printf '%s\n' 'codebox-untrusted-ca-must-not-render' > "$lab/untrusted-root/index.html"

trusted_port=${CODEBOX_BROWSER_TEST_TRUSTED_PORT:-19443}
untrusted_port=${CODEBOX_BROWSER_TEST_UNTRUSTED_PORT:-19444}
(
  cd "$lab/trusted-root"
  exec openssl s_server -quiet -WWW -accept "127.0.0.1:$trusted_port" \
    -cert "$lab/trusted-server.crt" -key "$lab/trusted-server.key"
) >"$lab/trusted-server.log" 2>&1 &
trusted_server_pid=$!
(
  cd "$lab/untrusted-root"
  exec openssl s_server -quiet -WWW -accept "127.0.0.1:$untrusted_port" \
    -cert "$lab/untrusted-server.crt" -key "$lab/untrusted-server.key"
) >"$lab/untrusted-server.log" 2>&1 &
untrusted_server_pid=$!

wait_for_server() {
  ca_file=$1
  port=$2
  marker=$3
  attempt=0
  until curl -fsS --max-time 2 --cacert "$ca_file" \
    "https://127.0.0.1:$port/index.html" 2>/dev/null | grep -q "$marker"; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 10 ]; then
      echo "synthetic HTTPS server on port $port did not become ready" >&2
      return 1
    fi
    sleep 1
  done
}
wait_for_server "$lab/trusted-ca.crt" "$trusted_port" codebox-trusted-ca-ok
wait_for_server "$lab/untrusted-ca.crt" "$untrusted_port" codebox-untrusted-ca-must-not-render

# Source the exact production helper; do not duplicate its certutil commands.
. "$trust_script"
nssdb="$lab/home/.pki/nssdb"
codebox_import_browser_cas "$lab/trusted-ca.crt" "$lab/missing-host-cas.pem" "$nssdb"

load_page() {
  port=$1
  output=$2
  log=$3
  HOME="$lab/home" chromium --headless --no-sandbox --disable-gpu \
    --disable-dev-shm-usage --no-first-run --no-default-browser-check \
    --user-data-dir="$lab/chromium-profile" --dump-dom \
    "https://127.0.0.1:$port/index.html" >"$output" 2>"$log"
}

if ! load_page "$trusted_port" "$lab/trusted-dom.html" "$lab/trusted-chromium.log" || \
  ! grep -q codebox-trusted-ca-ok "$lab/trusted-dom.html"; then
  echo "Chromium rejected the synthetic CA imported into its NSS database" >&2
  exit 1
fi
printf '%s\n' 'ok - Chromium accepts HTTPS through the imported CA'

untrusted_status=0
load_page "$untrusted_port" "$lab/untrusted-dom.html" "$lab/untrusted-chromium.log" || untrusted_status=$?
if grep -q codebox-untrusted-ca-must-not-render "$lab/untrusted-dom.html"; then
  echo "Chromium rendered a page signed by an untrusted CA" >&2
  exit 1
fi
if ! grep -q ERR_CERT_AUTHORITY_INVALID "$lab/untrusted-dom.html" "$lab/untrusted-chromium.log"; then
  echo "Chromium did not report the expected certificate-authority rejection (status $untrusted_status)" >&2
  exit 1
fi
printf '%s\n' 'ok - Chromium rejects HTTPS through an untrusted CA'
