#!/usr/bin/env bash
# Verify release archives and checksums produced by `make dist`.
set -euo pipefail

cd "$(dirname "$0")/.."

[ -d dist ] || { echo "dist/ 不存在，请先运行 make dist" >&2; exit 1; }
[ -f dist/checksums.txt ] || { echo "dist/checksums.txt 不存在" >&2; exit 1; }

(
	cd dist
	sha256sum -c checksums.txt
	for archive in mkbox-linux-*.tar.gz; do
		tar -tzf "$archive" | grep -qx "mkbox" || {
			echo "$archive 中缺少 mkbox" >&2
			exit 1
		}
		echo "verified $archive"
	done
)
