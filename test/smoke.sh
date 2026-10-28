#!/usr/bin/env bash
# Build mkbox and exercise commands that must not modify the host.
set -euo pipefail

cd "$(dirname "$0")/.."

TMP_DIR="$(mktemp -d)"
BIN="$TMP_DIR/mkbox"
CFG="$TMP_DIR/config.json"
trap 'rm -rf "$TMP_DIR"' EXIT

CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=smoke" -o "$BIN" ./cmd/mkbox

export MKBOX_CONFIG="$CFG"
export NO_COLOR=1

"$BIN" --help >/dev/null
"$BIN" version >/dev/null
"$BIN" config show >/dev/null
"$BIN" completion bash >/dev/null
"$BIN" doctor >/dev/null 2>&1 || true
"$BIN" docker install --dry-run --no-color >/dev/null
"$BIN" docker mirror set --dry-run --no-color >/dev/null
"$BIN" proxy set http://127.0.0.1:7890 --dry-run --no-color >/dev/null
"$BIN" mirror os aliyun --dry-run --no-color >/dev/null
"$BIN" k8s up --dry-run --no-color >/dev/null
"$BIN" k8s install --dry-run --distro k3s --no-color >/dev/null
"$BIN" k8s join-command --dry-run --no-color >/dev/null
"$BIN" backup create --dry-run --no-color >/dev/null
"$BIN" plan docker --json >/dev/null
"$BIN" diff docker --json >/dev/null
"$BIN" runtime detect --json >/dev/null
"$BIN" runtime plan --json >/dev/null
"$BIN" report -o "$TMP_DIR/report.json" >/dev/null
printf 'smoke\n' >"$TMP_DIR/input.txt"
"$BIN" bundle keygen --out-dir "$TMP_DIR/keys" >/dev/null
"$BIN" bundle build --name smoke --version 0.0.0 --file "$TMP_DIR/input.txt" \
	-o "$TMP_DIR/smoke.tar.gz" --json >/dev/null
if "$BIN" bundle verify "$TMP_DIR/smoke.tar.gz" --require-signature >/dev/null 2>&1; then
	echo "unsigned bundle unexpectedly passed --require-signature" >&2
	exit 1
fi
"$BIN" bundle inspect "$TMP_DIR/smoke.tar.gz" --json >/dev/null

echo "smoke tests passed"
