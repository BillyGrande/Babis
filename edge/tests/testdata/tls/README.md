# Test certificates

These files are for `tests/tls.rs` only. They are not secret.
The CA keys are deleted after signing. The server key stays, because the
fake hub needs it.

All keys are ECDSA P-256. Validity is 20 years (7305 days).
Run the commands in this directory.

```sh
# Test CA (the edge trusts this CA)
openssl ecparam -name prime256v1 -genkey -noout -out ca.key
openssl req -x509 -new -key ca.key -sha256 -days 7305 -subj "/CN=Babis Test CA" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" -out ca.crt

# Server key (PKCS#8) and certificate for localhost and 127.0.0.1
openssl ecparam -name prime256v1 -genkey -noout -out server.ec.key
openssl pkcs8 -topk8 -nocrypt -in server.ec.key -out server.key
openssl req -new -key server.key -subj "/CN=localhost" -out server.csr
cat > server.ext <<'EXT'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=DNS:localhost,IP:127.0.0.1
EXT
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -sha256 -days 7305 -extfile server.ext -out server.crt

# Second CA, unrelated to the first (the "wrong CA" case)
openssl ecparam -name prime256v1 -genkey -noout -out other-ca.key
openssl req -x509 -new -key other-ca.key -sha256 -days 7305 -subj "/CN=Other Test CA" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" -out other-ca.crt

# Clean up
rm server.csr server.ext ca.srl server.ec.key ca.key other-ca.key
```
