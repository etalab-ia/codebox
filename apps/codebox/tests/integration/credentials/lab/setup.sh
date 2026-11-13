#!/bin/sh
# Generates the CA and server certificate for the P02 credential lab.
# Usage: setup.sh <host-lan-ip>  (run from the lab directory)
set -eu

HOST_IP="${1:?usage: setup.sh <host-lan-ip>}"

cat > ca.cnf <<EOF
[req]
distinguished_name = dn
x509_extensions = ca_ext
[dn]
CN = P02 Lab CA
[ca_ext]
basicConstraints = critical,CA:TRUE
keyUsage = critical,keyCertSign,cRLSign
subjectKeyIdentifier = hash
EOF

openssl req -x509 -newkey rsa:2048 -nodes -keyout ca.key -out ca.crt \
  -subj "/CN=P02 Lab CA" -days 30 -config ca.cnf >/dev/null 2>&1

cat > srv.cnf <<EOF
[req]
distinguished_name = dn
[dn]
CN = p02lab.test
[ext]
basicConstraints = critical,CA:FALSE
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid,issuer
subjectAltName = DNS:p02lab.test,DNS:evil.test,IP:${HOST_IP},IP:172.16.0.5,IP:127.0.0.1
EOF

openssl req -newkey rsa:2048 -nodes -keyout srv.key -out srv.csr \
  -subj "/CN=p02lab.test" >/dev/null 2>&1
openssl x509 -req -in srv.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out srv.crt -days 30 -extfile srv.cnf -extensions ext >/dev/null 2>&1
rm -f srv.csr srv.cnf ca.cnf
echo "lab ready: ca.crt ca.key srv.crt srv.key"
