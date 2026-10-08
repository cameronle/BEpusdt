#!/bin/sh
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
# Pinned release and checksum; do not fetch an unverified latest binary.
version=8.30.1
sha256=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb
base=4d88040fd4096e77e8fb9ad2650e775753a977b6
work=$(mktemp -d "${TMPDIR:-${RUNNER_TEMP:-/tmp}}/bepusdt-gitleaks.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
curl --fail --location --silent --show-error --retry 3 --output "$work/gitleaks.tar.gz" "https://github.com/gitleaks/gitleaks/releases/download/v${version}/gitleaks_${version}_linux_x64.tar.gz"
printf '%s  %s\n' "$sha256" "$work/gitleaks.tar.gz" | sha256sum --check --status
tar -xzf "$work/gitleaks.tar.gz" -C "$work" gitleaks
"$work/gitleaks" dir . --redact --no-banner
"$work/gitleaks" git . --log-opts="${base}..HEAD" --redact --no-banner
