# Shared by the browser guest start script and the guest-capability harness.
# The caller supplies the CA paths and NSS database so the same implementation
# can be exercised against synthetic certificates without a Microsandbox mount.
codebox_import_browser_cas() {
  ca_file=$1
  host_cas_file=$2
  nssdb=$3

  if ! command -v certutil >/dev/null 2>&1; then
    return 0
  fi
  if [ ! -f "$ca_file" ] && [ ! -f "$host_cas_file" ]; then
    return 0
  fi

  mkdir -p "$nssdb"
  if [ ! -f "$nssdb/cert9.db" ]; then
    certutil -d "sql:$nssdb" -N --empty-password
  fi
  if [ -f "$ca_file" ]; then
    certutil -d "sql:$nssdb" -D -n microsandbox-ca >/dev/null 2>&1 || true
    certutil -d "sql:$nssdb" -A -t "C,," -n microsandbox-ca -i "$ca_file"
  fi
  if [ -f "$host_cas_file" ]; then
    ca_tmp=$(mktemp -d)
    awk -v dir="$ca_tmp" '/-----BEGIN CERTIFICATE-----/{n++; f=dir "/ca-" n ".pem"} f{print > f} /-----END CERTIFICATE-----/{close(f); f=""}' "$host_cas_file"
    for host_ca_file in "$ca_tmp"/ca-*.pem; do
      [ -f "$host_ca_file" ] || continue
      ca_nick="microsandbox-host-$(basename "$host_ca_file" .pem)"
      certutil -d "sql:$nssdb" -D -n "$ca_nick" >/dev/null 2>&1 || true
      certutil -d "sql:$nssdb" -A -t "C,," -n "$ca_nick" -i "$host_ca_file"
    done
    rm -rf "$ca_tmp"
  fi
}
