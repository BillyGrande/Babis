#!/bin/sh
# usage: build-rust.sh <dir> <outname> [extra RUSTFLAGS-free env]; run from bench dir
set -e
D=$1; OUT=$2
docker run --rm -v "$PWD/$D":/src -v bench-cargo:/usr/local/cargo/registry -v bench-target-$D:/src/target -w /src rust:1-alpine sh -c "apk add --no-cache build-base >/dev/null && cargo build --release --target x86_64-unknown-linux-musl $3 2>&1 | tail -15 && cp target/x86_64-unknown-linux-musl/release/$OUT /src/$OUT.bin && cargo tree --depth 1 | head -20"
mv $D/$OUT.bin bin-$D
