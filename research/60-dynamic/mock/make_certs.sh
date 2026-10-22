#!/bin/sh
# Generate a local CA + server certificate for the mock control plane.
# SANs cover the four API hostnames plus localhost/127.0.0.1.
set -eu
LAB="${AJS_LAB:-/tmp/ajs-lab}"
mkdir -p "$LAB/certs"
cd "$LAB/certs"
if [ ! -f server.key ]; then
  openssl req -x509 -newkey rsa:2048 -nodes -keyout ca.key -out ca.crt -days 2 \
    -subj "/CN=AJS Mock CA" >/dev/null 2>&1
  openssl req -newkey rsa:2048 -nodes -keyout server.key -out server.csr \
    -subj "/CN=localhost" >/dev/null 2>&1
  cat > san.cnf <<'EOF'
subjectAltName=DNS:login.ajs-app.com,DNS:login.mmegg.com,DNS:login.mbclubsh.com,DNS:localhost,IP:127.0.0.1
extendedKeyUsage=serverAuth
EOF
  openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
    -out server.crt -days 2 -extfile san.cnf >/dev/null 2>&1
fi
ls -l "$LAB/certs"
