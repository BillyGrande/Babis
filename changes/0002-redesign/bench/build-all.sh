#!/bin/sh
# Reproduce: ./make-fixtures.sh && ./build-all.sh && python3 harness.py 7
set -e
(cd go-stdlib && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ../bin-go-stdlib .)
(cd go-clientgo && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ../bin-go-clientgo .)
(cd go-stream && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ../bin-go-stream .)
./build-rust.sh rust-ureq ureqbench      # opt-level=3
./build-rust.sh rust-kube kubebench
gcc -O2 -static -o launcher launcher.c
