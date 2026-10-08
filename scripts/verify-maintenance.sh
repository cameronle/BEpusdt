#!/bin/sh
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
export GOMAXPROCS="${GOMAXPROCS:-2}"
export GOMEMLIMIT="${GOMEMLIMIT:-800MiB}"
# These tests use isolated fixtures; live RPC tests remain opt-in.
unset BEPUSDT_RUN_LIVE_RPC_TESTS BEPUSDT_QA_ATOM_USDT
go test -p 1 -count=1 ./...
go test -p 1 -race -count=1 ./app/task
go vet -p 1 ./...
git diff --check
