#!/bin/sh
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
export NODE_OPTIONS="${NODE_OPTIONS:---max-old-space-size=700}"
export GOMAXPROCS="${GOMAXPROCS:-2}"
export GOMEMLIMIT="${GOMEMLIMIT:-800MiB}"
(cd web && npx --yes pnpm@10.34.6 install --frozen-lockfile --shamefully-hoist)
(cd web && npx --yes pnpm@10.34.6 run build:prod)
test -s web/dist/secure.html
mkdir -p static/secure dist
cp -R web/dist/. static/secure/
version="${VERSION:-v1.24.2-shop-rpc3}"
CGO_ENABLED=0 go build -p 1 -trimpath -ldflags "-s -w -X github.com/v03413/bepusdt/app.Version=$version" -o dist/bepusdt ./main
./dist/bepusdt version
