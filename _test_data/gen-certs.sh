#!/bin/sh
# Regenerates the test CA and the localhost certificate it signs. The CA key is
# discarded, so rerun this script to replace both rather than reissuing one.
set -eu

cd "$(dirname "$0")"

days=3650
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days "$days" \
	-keyout "$tmp/ca.key" -out ca.crt -subj "/CN=gofix test CA" \
	-addext "basicConstraints=critical,CA:TRUE" \
	-addext "keyUsage=critical,keyCertSign,cRLSign"

openssl req -newkey rsa:2048 -nodes -sha256 \
	-keyout localhost.key -out "$tmp/localhost.csr" -subj "/CN=localhost"

openssl x509 -req -sha256 -days "$days" \
	-in "$tmp/localhost.csr" -CA ca.crt -CAkey "$tmp/ca.key" -CAserial "$tmp/ca.srl" -CAcreateserial \
	-out localhost.crt -extfile /dev/stdin <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth,clientAuth
subjectAltName=DNS:localhost,IP:127.0.0.1,IP:::1
EOF
