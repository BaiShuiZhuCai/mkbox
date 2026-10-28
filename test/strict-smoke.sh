#!/usr/bin/env bash
# Strict smoke tests for mkbox release artifacts.
#
# Usage:
#   bash test/strict-smoke.sh [dist-dir]
#
# Environment:
#   QEMU=0       skip ARM emulation tests
#   VERSION      expected release version (default 0.2.0)
#
# The script only performs host-mutating commands with --dry-run. Real writes
# are confined to a temporary directory. It exits non-zero if any check fails.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="${1:-${DIST_DIR:-$ROOT/dist}}"
VERSION="${VERSION:-0.2.0}"
RUN_QEMU="${QEMU:-1}"

WORK="$(mktemp -d)"
SERVER_PIDS=()
FAILED=()
PASS=0
FAIL=0
SKIP=0
LAST_OUT=""

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
	C_RESET=$'\033[0m'
	C_RED=$'\033[31m'
	C_GREEN=$'\033[32m'
	C_YELLOW=$'\033[33m'
	C_BOLD=$'\033[1m'
else
	C_RESET=""
	C_RED=""
	C_GREEN=""
	C_YELLOW=""
	C_BOLD=""
fi

cleanup() {
	local pid
	for pid in ${SERVER_PIDS[@]+"${SERVER_PIDS[@]}"}; do
		kill "$pid" 2>/dev/null || true
	done
	wait 2>/dev/null || true
	rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

section() {
	printf '\n%s== %s ==%s\n' "$C_BOLD" "$*" "$C_RESET"
}

ok() {
	PASS=$((PASS + 1))
	printf '  %sPASS%s %s\n' "$C_GREEN" "$C_RESET" "$*"
}

bad() {
	FAIL=$((FAIL + 1))
	FAILED+=("$*")
	printf '  %sFAIL%s %s\n' "$C_RED" "$C_RESET" "$*"
}

skip() {
	SKIP=$((SKIP + 1))
	printf '  %sSKIP%s %s\n' "$C_YELLOW" "$C_RESET" "$*"
}

dump() {
	printf '%s\n' "$1" | tail -n 20 | sed 's/^/      /'
}

check_ok() {
	local desc="$1"
	shift
	local out rc
	out=$("$@" 2>&1)
	rc=$?
	LAST_OUT="$out"
	if [ "$rc" -eq 0 ]; then
		ok "$desc"
	else
		bad "$desc (rc=$rc)"
		dump "$out"
	fi
	return 0
}

check_fail() {
	local desc="$1"
	shift
	local out rc
	out=$("$@" 2>&1)
	rc=$?
	LAST_OUT="$out"
	if [ "$rc" -ne 0 ]; then
		ok "$desc (rc=$rc)"
	else
		bad "$desc: expected a non-zero exit code"
		dump "$out"
	fi
	return 0
}

check_fail_with() {
	local desc="$1" needle="$2"
	shift 2
	local out rc
	out=$("$@" 2>&1)
	rc=$?
	LAST_OUT="$out"
	if [ "$rc" -eq 0 ]; then
		bad "$desc: expected failure"
		dump "$out"
	elif ! printf '%s' "$out" | grep -qF -- "$needle"; then
		bad "$desc: missing error text '$needle'"
		dump "$out"
	else
		ok "$desc"
	fi
	return 0
}

check_out() {
	local desc="$1" needle="$2"
	shift 2
	local out rc
	out=$("$@" 2>&1)
	rc=$?
	LAST_OUT="$out"
	if [ "$rc" -ne 0 ]; then
		bad "$desc (rc=$rc)"
		dump "$out"
	elif ! printf '%s' "$out" | grep -qF -- "$needle"; then
		bad "$desc: output does not contain '$needle'"
		dump "$out"
	else
		ok "$desc"
	fi
	return 0
}

assert_eq() {
	local desc="$1" want="$2" got="$3"
	if [ "$want" = "$got" ]; then
		ok "$desc"
	else
		bad "$desc: want '$want', got '$got'"
	fi
}

assert_file() {
	local desc="$1" path="$2"
	if [ -s "$path" ]; then
		ok "$desc"
	else
		bad "$desc: missing or empty $path"
	fi
}

assert_mode() {
	local desc="$1" path="$2" want="$3" got
	got=$(stat -c '%a' "$path" 2>/dev/null || echo missing)
	assert_eq "$desc" "$want" "$got"
}

assert_absent() {
	local desc="$1" path="$2"
	if [ -e "$path" ]; then
		bad "$desc: $path exists"
	else
		ok "$desc"
	fi
}

free_port() {
	python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

wait_http() {
	local url="$1" i
	for i in $(seq 1 100); do
		if curl -s -o /dev/null --max-time 1 "$url"; then
			return 0
		fi
		sleep 0.05
	done
	return 1
}

start_bg() {
	local log="$1"
	shift
	"$@" >"$log" 2>&1 &
	SERVER_PIDS+=("$!")
}

stop_servers() {
	local pid
	for pid in ${SERVER_PIDS[@]+"${SERVER_PIDS[@]}"}; do
		kill "$pid" 2>/dev/null || true
	done
	wait 2>/dev/null || true
	SERVER_PIDS=()
}

snapshot_host() {
	local out="$1" p f
	{
		echo "== directories =="
		for p in \
			/etc/docker \
			/etc/containerd \
			/etc/makebox \
			/var/backups/makebox \
			/etc/kubernetes \
			/etc/rancher/k3s \
			/etc/systemd/system/docker.service.d \
			/etc/systemd/system/containerd.service.d \
			/etc/apt/sources.list.d; do
			if [ -e "$p" ]; then
				find "$p" -maxdepth 4 -printf '%p|%y|%m|%s|%T@\n' 2>/dev/null | sort
			else
				echo "$p|ABSENT"
			fi
		done
		echo "== files =="
		for f in \
			/etc/environment \
			/etc/apt/sources.list \
			/etc/apt/apt.conf.d/95mkbox-proxy \
			/etc/pip.conf \
			/etc/profile.d/mkbox-proxy.sh \
			/etc/profile.d/mkbox-go.sh \
			/etc/sysctl.d/99-mkbox-k8s.conf \
			/etc/modules-load.d/mkbox-k8s.conf \
			/etc/docker/daemon.json \
			/etc/containerd/config.toml; do
			if [ -e "$f" ]; then
				stat -c '%n|%a|%s|%Y' "$f"
				sha256sum "$f"
			else
				echo "$f|ABSENT"
			fi
		done
	} >"$out"
}

# ---------------------------------------------------------------------------
section "1. Artifact inventory and integrity"

if [ ! -d "$DIST" ]; then
	bad "dist directory not found: $DIST"
	printf '\n%s%d passed, %d failed, %d skipped%s\n' "$C_BOLD" "$PASS" "$FAIL" "$SKIP" "$C_RESET"
	exit 1
fi

EXPECTED=(
	"mkbox-linux-amd64.tar.gz"
	"mkbox-linux-arm64.tar.gz"
	"mkbox-linux-arm.tar.gz"
	"makebox-$VERSION-source.tar.gz"
)
for f in "${EXPECTED[@]}"; do
	if [ -s "$DIST/$f" ]; then
		ok "artifact exists: $f"
	else
		bad "artifact missing or empty: $f"
	fi
done

for f in "${EXPECTED[@]}"; do
	if [ -s "$DIST/$f" ] && gzip -t "$DIST/$f" 2>/dev/null; then
		ok "gzip integrity: $f"
	elif [ ! -s "$DIST/$f" ]; then
		bad "gzip integrity: $f (missing)"
	else
		bad "gzip integrity: $f"
	fi
done
if [ -s "$DIST/checksums.txt" ]; then
	ok "checksums.txt is present"
else
	bad "checksums.txt is missing"
fi

(
	cd "$DIST" || exit 1
	sha256sum -c checksums.txt
) >"$WORK/checksums.log" 2>&1
if [ $? -eq 0 ]; then
	ok "checksums.txt verifies all archives"
else
	bad "checksums.txt verification failed"
	dump "$(cat "$WORK/checksums.log")"
fi

if [ "$(grep -cE '\.tar\.gz$' "$DIST/checksums.txt" 2>/dev/null)" -eq 4 ]; then
	ok "checksums.txt lists four artifacts"
else
	bad "checksums.txt should list exactly four archives"
fi

# ---------------------------------------------------------------------------
section "2. Binary archives: structure, architecture and runtime"

declare -A BINS=()
for arch in amd64 arm64 arm; do
	tgz="$DIST/mkbox-linux-$arch.tar.gz"
	list=$(tar -tzf "$tgz" 2>/dev/null)
	if [ "$list" = "mkbox" ]; then
		ok "$arch archive contains only mkbox"
	else
		bad "$arch archive unexpected contents: $list"
	fi
	if printf '%s\n' "$list" | grep -Eq '(^/|(^|/)\.\.(/|$))'; then
		bad "$arch archive contains absolute or parent paths"
	else
		ok "$arch archive has safe paths"
	fi

	dir="$WORK/bin/$arch"
	mkdir -p "$dir"
	if tar -xzf "$tgz" -C "$dir" 2>/dev/null; then
		ok "$arch archive extracts"
	else
		bad "$arch archive failed to extract"
	fi
	bin="$dir/mkbox"
	BINS[$arch]="$bin"
	if [ -x "$bin" ]; then
		ok "$arch binary is executable"
	else
		bad "$arch binary is not executable"
	fi
	if [ "$(stat -c '%s' "$bin" 2>/dev/null || echo 0)" -gt 1000000 ]; then
		ok "$arch binary size is plausible"
	else
		bad "$arch binary is unexpectedly small"
	fi

	case "$arch" in
	amd64) want_class="ELF64"; want_machine="Advanced Micro Devices X86-64" ;;
	arm64) want_class="ELF64"; want_machine="AArch64" ;;
	arm) want_class="ELF32"; want_machine="ARM" ;;
	esac
	header=$(readelf -h "$bin" 2>/dev/null)
	if printf '%s' "$header" | grep -qE "Class:[[:space:]]+$want_class"; then
		ok "$arch is $want_class"
	else
		bad "$arch has wrong ELF class"
	fi
	if printf '%s' "$header" | grep -qE "Machine:[[:space:]]+$want_machine"; then
		ok "$arch is $want_machine"
	else
		bad "$arch has wrong machine"
	fi
	if readelf -l "$bin" 2>/dev/null | grep -q 'INTERP'; then
		bad "$arch binary is dynamically linked"
	else
		ok "$arch binary is static (no INTERP)"
	fi
	if file -b "$bin" 2>/dev/null | grep -q 'statically linked'; then
		ok "$arch file(1) reports statically linked"
	else
		bad "$arch file(1) does not report static linkage"
	fi
done

# Native amd64 runtime checks.
AMD64="${BINS[amd64]}"
check_out "amd64 version reports $VERSION" "$VERSION" "$AMD64" version
check_out "amd64 version reports platform" "linux/amd64" "$AMD64" version
check_out "amd64 --version" "mkbox version $VERSION" "$AMD64" --version
check_ok "amd64 --help" "$AMD64" --help
check_ok "amd64 config show with isolated config" env MKBOX_CONFIG="$WORK/version-config.json" "$AMD64" config show

# ARM binaries under QEMU user emulation.
if [ "$RUN_QEMU" = "1" ]; then
	for pair in "arm64:qemu-aarch64-static:linux/arm64" "arm:qemu-arm-static:linux/arm"; do
		arch="${pair%%:*}"
		rest="${pair#*:}"
		emu="${rest%%:*}"
		platform="${rest#*:}"
		bin="${BINS[$arch]}"
		if ! command -v "$emu" >/dev/null 2>&1; then
			skip "$arch runtime (install $emu to enable)"
			continue
		fi
		out=$("$emu" "$bin" version 2>&1)
		rc=$?
		if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q "$VERSION" && printf '%s' "$out" | grep -q "$platform"; then
			ok "$arch runs under $emu and reports $platform"
		else
			bad "$arch failed under $emu (rc=$rc)"
			dump "$out"
		fi
		out=$("$emu" "$bin" --help 2>&1)
		if [ $? -eq 0 ] && printf '%s' "$out" | grep -q 'Usage:'; then
			ok "$arch --help runs under $emu"
		else
			bad "$arch --help failed under $emu"
			dump "$out"
		fi
	done
else
	skip "ARM runtime tests disabled by QEMU=0"
fi

# ---------------------------------------------------------------------------
section "3. Source archive"

SRC_TGZ="$DIST/makebox-$VERSION-source.tar.gz"
SRC_LIST=$(tar -tzf "$SRC_TGZ" 2>/dev/null)
if [ -n "$SRC_LIST" ]; then
	ok "source archive is readable"
else
	bad "source archive is empty or unreadable"
fi

if printf '%s\n' "$SRC_LIST" | grep -Eq '^makebox-[^/]+/(bin|dist|\.git)(/|$)'; then
	bad "source archive contains build/VCS output"
else
	ok "source archive excludes bin/dist/.git"
fi
if printf '%s\n' "$SRC_LIST" | grep -Eq '(^|/)\.\.(/|$)|^/'; then
	bad "source archive contains unsafe paths"
else
	ok "source archive has safe paths"
fi

for f in \
	go.mod go.sum Makefile install.sh README.md LICENSE \
	cmd/mkbox/main.go internal/cli/root.go \
	internal/cli/reconcile.go internal/cli/bundle.go \
	internal/reconcile/journal.go internal/reconcile/apply.go \
	internal/bundle/bundle.go internal/bundle/oci.go \
	test/smoke.sh test/strict-smoke.sh scripts/verify-release.sh \
	docs/architecture.md docs/commands.md docs/troubleshooting.md; do
	if printf '%s\n' "$SRC_LIST" | grep -qx "makebox-$VERSION/$f"; then
		ok "source contains $f"
	else
		bad "source missing $f"
	fi
done

mkdir -p "$WORK/src"
if tar -xzf "$SRC_TGZ" -C "$WORK/src" 2>/dev/null; then
	ok "source archive extracts"
else
	bad "source archive failed to extract"
fi
SRC="$WORK/src/makebox-$VERSION"
if [ -f "$SRC/go.mod" ]; then
	ok "extracted source has go.mod"
else
	bad "extracted source missing go.mod"
fi
if [ -x "$SRC/install.sh" ] && [ -x "$SRC/test/strict-smoke.sh" ]; then
	ok "source scripts keep executable mode"
else
	bad "source scripts lost executable mode"
fi

out=$(cd "$SRC" && GOCACHE="$WORK/gocache" go test ./... 2>&1)
rc=$?
if [ "$rc" -eq 0 ]; then
	ok "source: go test ./..."
else
	bad "source: go test ./... (rc=$rc)"
	dump "$out"
fi
out=$(cd "$SRC" && GOCACHE="$WORK/gocache" go vet ./... 2>&1)
rc=$?
if [ "$rc" -eq 0 ]; then
	ok "source: go vet ./..."
else
	bad "source: go vet ./... (rc=$rc)"
	dump "$out"
fi
out=$(cd "$SRC" && GOCACHE="$WORK/gocache" CGO_ENABLED=0 go build -trimpath -o "$WORK/src-mkbox" ./cmd/mkbox 2>&1)
rc=$?
if [ "$rc" -eq 0 ] && [ -x "$WORK/src-mkbox" ] && "$WORK/src-mkbox" --help >/dev/null 2>&1; then
	ok "source: go build and run"
else
	bad "source: go build failed (rc=$rc)"
	dump "$out"
fi
out=$(cd "$SRC" && bash test/smoke.sh 2>&1)
if [ $? -eq 0 ]; then
	ok "source: test/smoke.sh"
else
	bad "source: test/smoke.sh"
	dump "$out"
fi

# ---------------------------------------------------------------------------
section "4. Help, completion and version matrix"

export NO_COLOR=1
export NO_PROXY="127.0.0.1,localhost${NO_PROXY:+,$NO_PROXY}"
export no_proxy="$NO_PROXY"
export MKBOX_CONFIG="$WORK/main-config.json"

CMDS=(
	"backup"
	"backup create"
	"backup list"
	"backup restore"
	"completion"
	"config"
	"config show"
	"config get"
	"config set"
	"config path"
	"config init"
	"doctor"
	"help"
	"k8s"
	"kubernetes"
	"k8s addon"
	"k8s addon list"
	"k8s addon install"
	"k8s addon apply"
	"k8s init"
	"k8s install"
	"k8s join"
	"k8s join-command"
	"k8s kubeconfig"
	"k8s preflight"
	"k8s reset"
	"k8s status"
	"k8s up"
	"mirror"
	"mirror dev"
	"mirror list"
	"mirror os"
	"mirror status"
	"net"
	"net test"
	"proxy"
	"proxy set"
	"proxy show"
	"proxy unset"
	"registry"
	"registry list"
	"registry test"
	"version"
	"apply"
	"bundle"
	"bundle build"
	"bundle inspect"
	"bundle keygen"
	"bundle pull"
	"bundle push"
	"bundle verify"
	"diff"
	"plan"
	"report"
	"rollback"
	"runtime"
	"runtime detect"
	"runtime plan"
	"docker"
	"docker data-root"
	"docker install"
	"docker mirror"
	"docker mirror list"
	"docker mirror set"
	"docker mirror show"
	"docker mirror test"
	"docker mirror unset"
	"docker proxy"
	"docker proxy set"
	"docker proxy show"
	"docker proxy unset"
	"docker status"
	"docker uninstall"
)
for spec in "${CMDS[@]}"; do
	read -r -a args <<<"$spec"
	out=$(env -u MKBOX_CONFIG "$AMD64" --no-color "${args[@]}" --help 2>&1)
	rc=$?
	if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q 'Usage:'; then
		ok "help: mkbox $spec"
	else
		bad "help: mkbox $spec (rc=$rc)"
		dump "$out"
	fi
done

check_ok "help: mkbox root" "$AMD64" --help
check_out "help: mkbox help k8s init" "Usage:" "$AMD64" help k8s init
check_fail "help: unknown command" "$AMD64" no-such-command

BASH_COMPLETION="$WORK/mkbox.bash"
if "$AMD64" completion bash >"$BASH_COMPLETION" 2>/dev/null && [ -s "$BASH_COMPLETION" ] && bash -n "$BASH_COMPLETION"; then
	ok "completion bash is valid"
else
	bad "completion bash is invalid"
fi
for shell in zsh fish powershell; do
	out=$("$AMD64" completion "$shell" 2>&1)
	if [ $? -eq 0 ] && [ -n "$out" ]; then
		ok "completion $shell is non-empty"
	else
		bad "completion $shell failed or is empty"
	fi
done

check_out "version output contains release version" "$VERSION" "$AMD64" version
check_out "version output contains platform" "linux/amd64" "$AMD64" version

# ---------------------------------------------------------------------------
section "5. Validation failures happen before prompts or side effects"

NEG_CFG="$WORK/negative/config.json"
mkdir -p "$WORK/negative"

check_fail_with "docker install rejects bad package mirror (before prompt)" \
	"--mirror 必须是" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" docker install --mirror bogus
check_fail_with "docker install rejects bad registry mirror (before prompt)" \
	"无效的镜像加速地址" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" docker install --registry-mirror ://bad
check_fail_with "docker mirror set rejects bad URL" \
	"无效的镜像加速地址" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y docker mirror set ://bad
check_fail_with "docker data-root rejects relative path (before prompt)" \
	"必须是绝对路径" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" docker data-root relative </dev/null
check_fail_with "docker data-root rejects /" \
	"不能是 /" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y docker data-root /
check_fail_with "k8s up rejects bad CNI (before prompt)" \
	"--cni 必须是" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" k8s up --cni bogus
check_fail_with "k8s up rejects bad repo mirror" \
	"--repo-mirror 必须是" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s up --repo-mirror bogus
check_fail_with "k8s up rejects bad pod CIDR" \
	"--pod-cidr 无效" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s up --pod-cidr nope
check_fail_with "k8s up rejects bad service CIDR" \
	"--service-cidr 无效" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s up --service-cidr 10.0.0.0/33
check_fail_with "k8s up rejects bad API address" \
	"--apiserver-address 必须是合法 IP" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s up --apiserver-address nope
check_fail_with "k8s up rejects bad endpoint host" \
	"无效的 control-plane-endpoint" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s up --control-plane-endpoint "bad endpoint:6443"
check_fail_with "k8s up rejects bad distro" \
	"--distro 必须是" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s up --distro nomad
check_fail_with "k8s preflight rejects bad CNI" \
	"--cni 必须是" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s preflight --cni bogus
check_fail_with "k8s join rejects bad CNI before preflight" \
	"--cni 必须是" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s join --cni bogus --distro kubeadm \
	--server https://127.0.0.1:6443 --token abcdef.0123456789abcdef \
	--ca-cert-hash sha256:0000000000000000000000000000000000000000000000000000000000000000
check_fail_with "k8s join rejects bad token before preflight" \
	"--token 格式无效" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s join --distro kubeadm \
	--server https://127.0.0.1:6443 --token BAD \
	--ca-cert-hash sha256:0000000000000000000000000000000000000000000000000000000000000000
check_fail_with "k8s join rejects bad server port before preflight" \
	"无效的 --server" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s join --distro kubeadm \
	--server https://127.0.0.1:99999 --token abcdef.0123456789abcdef \
	--ca-cert-hash sha256:0000000000000000000000000000000000000000000000000000000000000000
check_fail_with "k8s join k3s rejects bad port before preflight" \
	"无效的 k3s server 地址" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s join --distro k3s \
	--server host:99999 --token node-token
check_fail_with "k8s join-command rejects bad distro" \
	"--distro 必须是" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s join-command --distro bogus
check_fail_with "k8s reset rejects bad distro" \
	"--distro 必须是" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s reset --distro bogus
check_fail_with "k8s addon rejects bad pod CIDR" \
	"--pod-cidr 无效" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s addon install flannel --pod-cidr nope
check_fail_with "k8s addon rejects missing local manifest" \
	"读取清单" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s addon install metrics-server --source /nonexistent/manifest.yaml
check_fail_with "k8s addon rejects non-HTTP URL" \
	"必须使用 http 或 https" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y k8s addon install metrics-server --source ftp://example.com/m.yaml
check_fail_with "config rejects bad registry mirror" \
	"registry.mirrors" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" config set registry.mirrors ://bad
check_fail_with "config rejects bad CNI" \
	"k8s.cni 必须是" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" config set k8s.cni bogus
check_fail_with "config rejects relative data-root" \
	"docker.dataRoot 必须是" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" config set docker.dataRoot relative
check_fail_with "config rejects malformed k8s version" \
	"k8s.version 必须" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" config set k8s.version 1.35foo
check_fail_with "registry test rejects invalid-only list" \
	"没有可测试的镜像地址" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" registry test ://bad
check_fail_with "proxy set rejects unknown scope" \
	"未知 scope" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y proxy set http://127.0.0.1:7890 --scope bogus
check_fail_with "mirror os rejects unknown mirror" \
	"未知镜像站" env MKBOX_CONFIG="$NEG_CFG" "$AMD64" --dry-run -y mirror os bogus

assert_absent "negative tests did not create config" "$NEG_CFG"

# ---------------------------------------------------------------------------
section "6. Dry-run must not mutate the host"

DRY_CFG="$WORK/dryrun/config.json"
mkdir -p "$(dirname "$DRY_CFG")"
snapshot_host "$WORK/host-before.txt"

DRY_ENV=(env "MKBOX_CONFIG=$DRY_CFG")
check_ok "dry-run docker install" "${DRY_ENV[@]}" "$AMD64" --dry-run -y docker install
check_ok "dry-run docker install with explicit mirror" "${DRY_ENV[@]}" "$AMD64" --dry-run -y docker install --mirror official --registry-mirror http://127.0.0.1:1
check_ok "dry-run docker mirror set" "${DRY_ENV[@]}" "$AMD64" --dry-run -y docker mirror set https://mirror.example.com
check_ok "dry-run docker mirror unset" "${DRY_ENV[@]}" "$AMD64" --dry-run -y docker mirror unset
check_ok "dry-run docker proxy set" "${DRY_ENV[@]}" "$AMD64" --dry-run -y docker proxy set http://127.0.0.1:7890
check_ok "dry-run docker proxy unset" "${DRY_ENV[@]}" "$AMD64" --dry-run -y docker proxy unset
check_ok "dry-run docker data-root" "${DRY_ENV[@]}" "$AMD64" --dry-run -y docker data-root /var/lib/docker-test
check_ok "dry-run proxy set --scope all" "${DRY_ENV[@]}" "$AMD64" --dry-run -y proxy set http://127.0.0.1:7890 --scope all
check_ok "dry-run proxy unset --scope all" "${DRY_ENV[@]}" "$AMD64" --dry-run -y proxy unset --scope all
check_ok "dry-run mirror os aliyun" "${DRY_ENV[@]}" "$AMD64" --dry-run -y mirror os aliyun
check_ok "dry-run mirror dev --all" "${DRY_ENV[@]}" "$AMD64" --dry-run -y mirror dev --all
check_ok "dry-run k8s install k3s" "${DRY_ENV[@]}" "$AMD64" --dry-run -y k8s install --distro k3s
check_ok "dry-run k8s up" "${DRY_ENV[@]}" "$AMD64" --dry-run -y k8s up --skip-preflight
check_ok "dry-run k8s init" "${DRY_ENV[@]}" "$AMD64" --dry-run -y k8s init --skip-preflight
check_ok "dry-run k8s preflight" "${DRY_ENV[@]}" "$AMD64" --dry-run -y k8s preflight
check_ok "dry-run k8s join-command k3s" "${DRY_ENV[@]}" "$AMD64" --dry-run -y k8s join-command --distro k3s
check_ok "dry-run backup create" "${DRY_ENV[@]}" "$AMD64" --dry-run -y backup create
check_ok "dry-run config init" "${DRY_ENV[@]}" "$AMD64" --dry-run -y config init
check_ok "dry-run docker uninstall" "${DRY_ENV[@]}" "$AMD64" --dry-run -y docker uninstall
cat >"$WORK/local-addon.yaml" <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: mkbox-strict-smoke
  namespace: kube-system
data:
  probe: "true"
EOF
check_ok "dry-run k8s addon install from local manifest" "${DRY_ENV[@]}" "$AMD64" --dry-run -y \
	k8s addon install metrics-server --source "$WORK/local-addon.yaml"

snapshot_host "$WORK/host-after.txt"
if diff -u "$WORK/host-before.txt" "$WORK/host-after.txt" >"$WORK/host.diff"; then
	ok "dry-run matrix left host configuration unchanged"
else
	bad "dry-run matrix modified the host"
	dump "$(cat "$WORK/host.diff")"
fi
assert_absent "dry-run did not create mkbox config" "$DRY_CFG"

# ---------------------------------------------------------------------------
section "7. Configuration file handling"

CFG_DIR="$WORK/config dir"
CFG="$CFG_DIR/config.json"
check_ok "config init in path with spaces" env MKBOX_CONFIG="$CFG" "$AMD64" config init
assert_mode "config file mode is 0600" "$CFG" 600
assert_mode "config directory mode is 0700" "$CFG_DIR" 700
if python3 -m json.tool "$CFG" >/dev/null 2>&1; then
	ok "config is valid JSON"
else
	bad "config is not valid JSON"
fi

check_fail "config init refuses to overwrite" env MKBOX_CONFIG="$CFG" "$AMD64" config init
check_ok "config can be customised before force" env MKBOX_CONFIG="$CFG" "$AMD64" config set registry.maxMirrors 2
assert_mode "config file stays 0600 after set" "$CFG" 600

FLAG_CFG="$WORK/flag/config.json"
check_ok "root --config flag initialises alternate file" env MKBOX_CONFIG="$WORK/ignored-config.json" \
	"$AMD64" --config "$FLAG_CFG" config init
assert_file "root --config flag created file" "$FLAG_CFG"
assert_absent "root --config flag ignores MKBOX_CONFIG" "$WORK/ignored-config.json"

check_ok "config init --force overwrites" env MKBOX_CONFIG="$CFG" "$AMD64" config init --force
if compgen -G "$CFG.mkbox.bak.*" >/dev/null; then
	ok "config init --force created a backup"
	bak=$(compgen -G "$CFG.mkbox.bak.*" | head -1)
	assert_mode "config backup mode is 0600" "$bak" 600
else
	bad "config init --force did not create a backup"
fi

check_ok "config set proxy with credentials" env MKBOX_CONFIG="$CFG" "$AMD64" config set proxy.http 'http://alice:secret@127.0.0.1:7890'
out=$(env MKBOX_CONFIG="$CFG" "$AMD64" config show 2>&1)
if printf '%s' "$out" | grep -q 'REDACTED' && ! printf '%s' "$out" | grep -q 'secret'; then
	ok "config show redacts proxy password"
else
	bad "config show did not redact proxy password"
	dump "$out"
fi
if env MKBOX_CONFIG="$CFG" "$AMD64" config show --raw 2>&1 | grep -q 'secret'; then
	ok "config show --raw reveals password"
else
	bad "config show --raw did not reveal password"
fi

check_ok "config accepts k3s full version" env MKBOX_CONFIG="$CFG" "$AMD64" config set k8s.version v1.28.15+k3s1
check_out "config persists k3s full version" "v1.28.15+k3s1" env MKBOX_CONFIG="$CFG" "$AMD64" config get k8s.version
check_out "config default language" "zh-CN" env MKBOX_CONFIG="$CFG" "$AMD64" config get language
check_ok "config accepts language en" env MKBOX_CONFIG="$CFG" "$AMD64" config set language en
if env MKBOX_CONFIG="$CFG" "$AMD64" --help 2>&1 | grep -q 'One-command Docker and Kubernetes'; then
	ok "config language applies to help output"
else
	bad "config language did not localize help output"
fi
if MKBOX_LANG=en "$AMD64" --help 2>&1 | grep -q 'One-command Docker and Kubernetes'; then
	ok "MKBOX_LANG applies to help output"
else
	bad "MKBOX_LANG did not localize help output"
fi
check_fail "config rejects unknown language" env MKBOX_CONFIG="$CFG" "$AMD64" config set language fr
check_ok "config language reset" env MKBOX_CONFIG="$CFG" "$AMD64" config set language zh-CN
check_fail_with "config rejects malformed k3s version" "k8s.version 必须" \
	env MKBOX_CONFIG="$CFG" "$AMD64" config set k8s.version v1.28.15+k3s

printf '{bad' >"$CFG"
check_ok "version works with malformed config" env MKBOX_CONFIG="$CFG" "$AMD64" version
check_fail "config command reports malformed config" env MKBOX_CONFIG="$CFG" "$AMD64" config show

# ---------------------------------------------------------------------------
section "8. Backup and restore security"

DATA="$WORK/data"
BACKUP_DIR="$WORK/backups"
RESTORE_DIR="$WORK/restore"
mkdir -p "$DATA/sub" "$BACKUP_DIR" "$RESTORE_DIR"
printf 'alpha\n' >"$DATA/a.conf"
chmod 600 "$DATA/a.conf"
printf 'beta\n' >"$DATA/sub/b.txt"
ln -s a.conf "$DATA/link"

ARCHIVE="$BACKUP_DIR/test.tar.gz"
check_ok "backup create explicit paths" env MKBOX_CONFIG="$WORK/backup-config.json" "$AMD64" -y backup create --path "$DATA" --dir "$BACKUP_DIR" -o "$ARCHIVE"
assert_file "backup archive exists" "$ARCHIVE"
assert_mode "backup archive mode is 0600" "$ARCHIVE" 600
listing=$(tar -tzf "$ARCHIVE" 2>/dev/null)
if printf '%s\n' "$listing" | grep -q 'a.conf' && printf '%s\n' "$listing" | grep -q 'sub/b.txt'; then
	ok "backup contains regular files"
else
	bad "backup is missing expected files"
fi
if printf '%s\n' "$listing" | grep -q '/link$'; then
	bad "backup followed or stored a symlink"
else
	ok "backup skips symlinks"
fi

check_ok "backup list" env MKBOX_CONFIG="$WORK/backup-config.json" "$AMD64" backup list --dir "$BACKUP_DIR"
check_out "backup list shows archive" "test.tar.gz" env MKBOX_CONFIG="$WORK/backup-config.json" "$AMD64" backup list --dir "$BACKUP_DIR"

check_ok "restore roundtrip" env MKBOX_CONFIG="$WORK/backup-config.json" "$AMD64" -y backup restore "$ARCHIVE" --dest "$RESTORE_DIR" --force
restored=$(find "$RESTORE_DIR" -name a.conf -type f | head -1)
if [ -n "$restored" ] && [ "$(cat "$restored")" = "alpha" ]; then
	ok "restored file content matches"
else
	bad "restored file content mismatch"
fi
if [ -n "$restored" ]; then
	assert_mode "restore preserves file mode" "$restored" 600
fi
if find "$RESTORE_DIR" -type l | grep -q .; then
	bad "restore created a symlink"
else
	ok "restore did not create symlinks"
fi

check_fail_with "backup rejects relative explicit path" "--path 必须使用绝对路径" \
	env MKBOX_CONFIG="$WORK/backup-config.json" "$AMD64" --dry-run -y backup create --path relative
check_fail_with "backup rejects empty explicit path set" "没有可备份的普通文件" \
	env MKBOX_CONFIG="$WORK/backup-config.json" "$AMD64" --dry-run -y backup create --path /nonexistent/path
check_fail_with "restore rejects relative destination before prompt" "恢复目标必须是绝对路径" \
	env MKBOX_CONFIG="$WORK/backup-config.json" "$AMD64" backup restore "$ARCHIVE" --dest relative </dev/null

if command -v python3 >/dev/null 2>&1; then
	python3 - "$WORK/traversal.tar.gz" <<'PY'
import io
import sys
import tarfile

with tarfile.open(sys.argv[1], "w:gz") as tar:
    data = b"owned"
    info = tarfile.TarInfo("../evil.txt")
    info.size = len(data)
    tar.addfile(info, io.BytesIO(data))
PY
	check_fail_with "restore rejects path traversal" "拒绝不安全的归档路径" \
		env MKBOX_CONFIG="$WORK/backup-config.json" "$AMD64" -y backup restore "$WORK/traversal.tar.gz" --dest "$WORK/evil-dest" --force
	assert_absent "path traversal did not write outside destination" "$WORK/evil.txt"

	python3 - "$WORK/backslash.tar.gz" <<'PY'
import io
import sys
import tarfile

with tarfile.open(sys.argv[1], "w:gz") as tar:
    data = b"owned"
    info = tarfile.TarInfo(r"..\evil.txt")
    info.size = len(data)
    tar.addfile(info, io.BytesIO(data))
PY
	check_fail_with "restore rejects Windows-style path traversal" "拒绝不安全的归档路径" \
		env MKBOX_CONFIG="$WORK/backup-config.json" "$AMD64" -y backup restore "$WORK/backslash.tar.gz" --dest "$WORK/evil-dest" --force
	assert_absent "backslash traversal did not write outside destination" "$WORK/evil.txt"

	python3 - "$WORK/absolute.tar.gz" <<'PY'
import io
import sys
import tarfile

with tarfile.open(sys.argv[1], "w:gz") as tar:
    data = b"owned"
    info = tarfile.TarInfo("/tmp/mkbox-absolute-evil.txt")
    info.size = len(data)
    tar.addfile(info, io.BytesIO(data))
PY
	check_fail_with "restore rejects absolute archive path" "不允许绝对路径" \
		env MKBOX_CONFIG="$WORK/backup-config.json" "$AMD64" -y backup restore "$WORK/absolute.tar.gz" --dest "$WORK/evil-dest" --force
	assert_absent "absolute path was not written" "/tmp/mkbox-absolute-evil.txt"

	python3 - "$WORK/symlink.tar.gz" <<'PY'
import sys
import tarfile

with tarfile.open(sys.argv[1], "w:gz") as tar:
    info = tarfile.TarInfo("link")
    info.type = tarfile.SYMTYPE
    info.linkname = "/etc/passwd"
    tar.addfile(info)
PY
	check_ok "restore skips symlink entries" env MKBOX_CONFIG="$WORK/backup-config.json" \
		"$AMD64" -y backup restore "$WORK/symlink.tar.gz" --dest "$WORK/symlink-dest" --force
	if find "$WORK/symlink-dest" -type l 2>/dev/null | grep -q .; then
		bad "symlink archive created a link"
	else
		ok "symlink archive did not create a link"
	fi
else
	skip "backup archive attack tests (python3 unavailable)"
fi

printf 'not a gzip archive' >"$WORK/corrupt.tar.gz"
check_fail_with "restore rejects corrupt archive" "不是有效的 gzip" \
	env MKBOX_CONFIG="$WORK/backup-config.json" "$AMD64" -y backup restore "$WORK/corrupt.tar.gz" --dest "$WORK/corrupt-dest" --force

# ---------------------------------------------------------------------------
section "9. Kubeconfig export"

KCFG="$WORK/kube"
mkdir -p "$KCFG"
cat >"$KCFG/source.yaml" <<'EOF'
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://127.0.0.1:6443
  name: kubernetes
contexts:
- context:
    cluster: kubernetes
    user: kubernetes-admin
  name: kubernetes-admin@kubernetes
current-context: kubernetes-admin@kubernetes
users:
- name: kubernetes-admin
  user:
    client-certificate-data: Zm9v
    client-key-data: YmFy
EOF

check_ok "kubeconfig export" env MKBOX_CONFIG="$WORK/kube-config.json" "$AMD64" k8s kubeconfig --source "$KCFG/source.yaml" -o "$KCFG/out.yaml" --server https://10.1.2.3
assert_mode "kubeconfig mode is 0600" "$KCFG/out.yaml" 600
if grep -q 'server: https://10.1.2.3:6443' "$KCFG/out.yaml"; then
	ok "kubeconfig server rewritten with default port"
else
	bad "kubeconfig server not rewritten"
fi
check_fail "kubeconfig refuses overwrite without --force" env MKBOX_CONFIG="$WORK/kube-config.json" "$AMD64" k8s kubeconfig --source "$KCFG/source.yaml" -o "$KCFG/out.yaml" --server https://10.1.2.4
check_ok "kubeconfig --force overwrites" env MKBOX_CONFIG="$WORK/kube-config.json" "$AMD64" k8s kubeconfig --source "$KCFG/source.yaml" -o "$KCFG/out.yaml" --server https://10.1.2.4 --force
if compgen -G "$KCFG/out.yaml.mkbox.bak.*" >/dev/null; then
	ok "kubeconfig --force created a backup"
	bak=$(compgen -G "$KCFG/out.yaml.mkbox.bak.*" | head -1)
	assert_mode "kubeconfig backup mode is 0600" "$bak" 600
else
	bad "kubeconfig --force did not create a backup"
fi

cp "$KCFG/source.yaml" "$KCFG/inplace.yaml"
chmod 644 "$KCFG/inplace.yaml"
check_fail "kubeconfig in-place rewrite requires --force" env MKBOX_CONFIG="$WORK/kube-config.json" "$AMD64" k8s kubeconfig --source "$KCFG/inplace.yaml" -o "$KCFG/inplace.yaml" --server https://10.9.9.9
check_ok "kubeconfig in-place rewrite with --force" env MKBOX_CONFIG="$WORK/kube-config.json" "$AMD64" k8s kubeconfig --source "$KCFG/inplace.yaml" -o "$KCFG/inplace.yaml" --server https://10.9.9.9 --force
assert_mode "in-place kubeconfig mode is 0600" "$KCFG/inplace.yaml" 600
if grep -q 'server: https://10.9.9.9:6443' "$KCFG/inplace.yaml"; then
	ok "in-place kubeconfig server updated"
else
	bad "in-place kubeconfig server not updated"
fi
check_out "kubeconfig stdout export" "server: https://127.0.0.1:6443" env MKBOX_CONFIG="$WORK/kube-config.json" "$AMD64" k8s kubeconfig --source "$KCFG/source.yaml" -o -

for server in "ftp://10.0.0.1" "https://10.0.0.1:99999" "https://user:pass@10.0.0.1:6443" "https://bad_host:6443"; do
	check_fail "kubeconfig rejects server $server" env MKBOX_CONFIG="$WORK/kube-config.json" \
		"$AMD64" k8s kubeconfig --source "$KCFG/source.yaml" -o "$KCFG/rejected.yaml" --server "$server" --force
	assert_absent "no output for rejected server $server" "$KCFG/rejected.yaml"
done

# ---------------------------------------------------------------------------
section "10. Registry probing with local HTTP servers"

if command -v python3 >/dev/null 2>&1 && command -v curl >/dev/null 2>&1; then
	cat >"$WORK/registry-server.py" <<'PY'
import http.server
import sys

code = int(sys.argv[1])
port = int(sys.argv[2])


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.rstrip("/").endswith("/v2"):
            self.send_response(code)
            if code == 401:
                self.send_header("WWW-Authenticate", 'Basic realm="registry"')
            self.send_header("Content-Length", "0")
            self.end_headers()
        else:
            self.send_response(404)
            self.send_header("Content-Length", "0")
            self.end_headers()

    def log_message(self, *args):
        pass


http.server.HTTPServer(("127.0.0.1", port), Handler).serve_forever()
PY
	P200=$(free_port)
	P401=$(free_port)
	P404=$(free_port)
	start_bg "$WORK/reg200.log" python3 "$WORK/registry-server.py" 200 "$P200"
	start_bg "$WORK/reg401.log" python3 "$WORK/registry-server.py" 401 "$P401"
	start_bg "$WORK/reg404.log" python3 "$WORK/registry-server.py" 404 "$P404"
	if wait_http "http://127.0.0.1:$P200/v2/" && wait_http "http://127.0.0.1:$P401/v2/" && wait_http "http://127.0.0.1:$P404/v2/"; then
		ok "local registry servers are ready"
	else
		bad "local registry servers failed to start"
	fi

	check_out "registry accepts HTTP 200" "HTTP 200" env MKBOX_CONFIG="$WORK/registry-config.json" "$AMD64" registry test --timeout 2s "http://127.0.0.1:$P200"
	check_out "registry accepts HTTP 401" "HTTP 401" env MKBOX_CONFIG="$WORK/registry-config.json" "$AMD64" registry test --timeout 2s "http://127.0.0.1:$P401"
	check_fail_with "registry rejects HTTP 404" "HTTP 404" env MKBOX_CONFIG="$WORK/registry-config.json" "$AMD64" registry test --timeout 2s "http://127.0.0.1:$P404"
	check_fail "registry rejects unreachable endpoint" env MKBOX_CONFIG="$WORK/registry-config.json" "$AMD64" registry test --timeout 1s http://127.0.0.1:1
	check_ok "docker mirror set with local registry (dry-run)" env MKBOX_CONFIG="$WORK/registry-config.json" "$AMD64" --dry-run -y docker mirror set "http://127.0.0.1:$P200"
	check_ok "docker install with local mirror (dry-run)" env MKBOX_CONFIG="$WORK/registry-config.json" "$AMD64" --dry-run -y docker install --registry-mirror "http://127.0.0.1:$P200"
	stop_servers
else
	skip "registry server tests (python3/curl unavailable)"
fi

# ---------------------------------------------------------------------------
section "11. install.sh"

INSTALLER="$ROOT/install.sh"
check_ok "install.sh --help" "$INSTALLER" --help
check_fail "install.sh rejects unknown option" "$INSTALLER" --bogus

PREFIX_ARCHIVE="$WORK/install-archive"
check_ok "install.sh local archive" "$INSTALLER" --local "$DIST/mkbox-linux-amd64.tar.gz" --prefix "$PREFIX_ARCHIVE"
check_out "installed archive binary runs" "mkbox version $VERSION" "$PREFIX_ARCHIVE/mkbox" --version

PREFIX_BINARY="$WORK/install-binary"
check_ok "install.sh local binary" "$INSTALLER" --local "$AMD64" --prefix "$PREFIX_BINARY"
check_out "installed local binary runs" "linux/amd64" "$PREFIX_BINARY/mkbox" version

mkdir -p "$WORK/fake-archive"
printf 'not a binary\n' >"$WORK/fake-archive/other"
tar -czf "$WORK/fake-archive.tar.gz" -C "$WORK/fake-archive" other
check_fail_with "install.sh rejects archive without mkbox" "未找到 mkbox" \
	"$INSTALLER" --local "$WORK/fake-archive.tar.gz" --prefix "$WORK/install-fake"

if command -v python3 >/dev/null 2>&1 && command -v curl >/dev/null 2>&1; then
	SRV="$WORK/http"
	mkdir -p "$SRV"
	cp "$DIST/mkbox-linux-amd64.tar.gz" "$SRV/"
	cp "$DIST/checksums.txt" "$SRV/"
	PORT=$(free_port)
	start_bg "$WORK/http.log" python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$SRV"
	if wait_http "http://127.0.0.1:$PORT/checksums.txt"; then
		ok "installer HTTP server is ready"
	else
		bad "installer HTTP server failed to start"
	fi

	PREFIX_REMOTE="$WORK/install-remote"
	check_ok "install.sh remote download with checksum" env MKBOX_BASE_URL="http://127.0.0.1:$PORT" \
		"$INSTALLER" --prefix "$PREFIX_REMOTE"
	check_out "remote installed binary runs" "mkbox version $VERSION" "$PREFIX_REMOTE/mkbox" --version

	cp "$SRV/checksums.txt" "$WORK/checksums.good"
	python3 - "$SRV/checksums.txt" <<'PY'
import sys

path = sys.argv[1]
lines = []
for line in open(path, encoding="utf-8"):
    if "mkbox-linux-amd64.tar.gz" in line:
        digest, rest = line.split(None, 1)
        digest = ("0" if digest[0] != "0" else "1") + digest[1:]
        line = digest + " " + rest
    lines.append(line)
open(path, "w", encoding="utf-8").writelines(lines)
PY
	PREFIX_TAMPER="$WORK/install-tamper"
	mkdir -p "$PREFIX_TAMPER"
	check_fail_with "install.sh rejects tampered checksum" "SHA-256 校验失败" \
		env MKBOX_BASE_URL="http://127.0.0.1:$PORT" "$INSTALLER" --prefix "$PREFIX_TAMPER"
	assert_absent "tampered install did not write a binary" "$PREFIX_TAMPER/mkbox"

	cp "$WORK/checksums.good" "$SRV/checksums.txt"
	stop_servers
else
	skip "installer remote tests (python3/curl unavailable)"
fi

# ---------------------------------------------------------------------------
section "12. Reconciliation, runtime coexist and offline bundles"

REC="$WORK/reconcile"
REC_CFG="$REC/config.json"
mkdir -p "$REC"
check_ok "reconcile config init" env MKBOX_CONFIG="$REC_CFG" "$AMD64" config init
check_ok "reconcile config proxy with credentials" env MKBOX_CONFIG="$REC_CFG" "$AMD64" \
	config set proxy.http 'http://alice:supersecret@127.0.0.1:7890'
check_ok "reconcile config data-root" env MKBOX_CONFIG="$REC_CFG" "$AMD64" \
	config set docker.dataRoot /var/lib/mkbox-smoke
check_ok "reconcile config registry mirror" env MKBOX_CONFIG="$REC_CFG" "$AMD64" \
	config set registry.mirrors https://mirror.example.com
check_ok "reconcile config gh proxy" env MKBOX_CONFIG="$REC_CFG" "$AMD64" \
	config set network.ghProxy 'https://bob:ghsecret@ghproxy.example.com'

PLAN_JSON="$REC/plan-docker.json"
if env MKBOX_CONFIG="$REC_CFG" "$AMD64" plan docker --json >"$PLAN_JSON" 2>"$REC/plan.err"; then
	ok "plan docker --json exits 0"
else
	bad "plan docker --json failed"
	dump "$(cat "$REC/plan.err")"
fi
if [ -s "$PLAN_JSON" ] && python3 - "$PLAN_JSON" >"$REC/plan-check.err" 2>&1 <<'PY'
import json, sys
p = json.load(open(sys.argv[1], encoding="utf-8"))
assert p["scope"] == "docker", p.get("scope")
assert p.get("actions"), "plan has no actions"
assert "REDACTED" in p["desired"]["proxy"]["url"], p["desired"]["proxy"]["url"]
assert "supersecret" not in json.dumps(p), "proxy password leaked in plan"
assert "ghsecret" not in json.dumps(p), "GitHub proxy password leaked in plan"
assert p["host"]["isRoot"] in (True, False)
PY
then
	ok "plan JSON schema and proxy redaction"
else
	bad "plan JSON schema or redaction failed"
	dump "$(cat "$REC/plan-check.err")"
fi
out=$(env MKBOX_CONFIG="$REC_CFG" "$AMD64" plan docker 2>&1)
if [ $? -eq 0 ] && ! printf '%s' "$out" | grep -q 'supersecret'; then
	ok "plan text output redacts proxy password"
else
	bad "plan text output leaks proxy password"
	dump "$out"
fi
check_fail_with "plan rejects unknown scope" "scope 必须是" \
	env MKBOX_CONFIG="$REC_CFG" "$AMD64" plan nomad

DIFF_JSON="$REC/diff-docker.json"
if env MKBOX_CONFIG="$REC_CFG" "$AMD64" diff docker --json >"$DIFF_JSON" 2>"$REC/diff.err"; then
	ok "diff docker --json exits 0"
else
	bad "diff docker --json failed"
	dump "$(cat "$REC/diff.err")"
fi
if [ -s "$DIFF_JSON" ] && python3 - "$DIFF_JSON" >"$REC/diff-check.err" 2>&1 <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
assert isinstance(d, list) and d, d
assert all("path" in x and "changed" in x for x in d)
assert "supersecret" not in json.dumps(d), "proxy password leaked in diff"
PY
then
	ok "diff JSON lists managed file drift without secrets"
else
	bad "diff JSON schema or redaction failed"
	dump "$(cat "$REC/diff-check.err")"
fi
env MKBOX_CONFIG="$REC_CFG" "$AMD64" diff docker --exit-code >/dev/null 2>&1
assert_eq "diff --exit-code returns 2 on drift" "2" "$?"
check_fail_with "diff rejects unknown scope" "scope 必须是" \
	env MKBOX_CONFIG="$REC_CFG" "$AMD64" diff nomad

RT_JSON="$REC/runtime-detect.json"
if env HTTP_PROXY='http://alice:supersecret@127.0.0.1:7890' \
	HTTPS_PROXY='http://alice:supersecret@127.0.0.1:7890' \
	MKBOX_CONFIG="$REC_CFG" "$AMD64" runtime detect --json >"$RT_JSON" 2>"$REC/runtime.err"; then
	ok "runtime detect --json exits 0"
else
	bad "runtime detect --json failed"
	dump "$(cat "$REC/runtime.err")"
fi
if [ -s "$RT_JSON" ] && python3 - "$RT_JSON" >"$REC/runtime-check.err" 2>&1 <<'PY'
import json, sys
f = json.load(open(sys.argv[1], encoding="utf-8"))
for key in ("os", "arch", "kernel", "cgroupVersion", "dockerInstalled"):
    assert key in f, key
assert "supersecret" not in json.dumps(f), "runtime facts leaked proxy password"
assert "REDACTED" in json.dumps(f), "runtime facts did not redact proxy"
PY
then
	ok "runtime detect JSON schema and proxy redaction"
else
	bad "runtime detect JSON schema or redaction failed"
	dump "$(cat "$REC/runtime-check.err")"
fi

RTP_JSON="$REC/runtime-plan.json"
if env HTTP_PROXY='http://alice:supersecret@127.0.0.1:7890' \
	HTTPS_PROXY='http://alice:supersecret@127.0.0.1:7890' \
	MKBOX_CONFIG="$REC_CFG" "$AMD64" runtime plan --json >"$RTP_JSON" 2>"$REC/runtime-plan.err"; then
	ok "runtime plan --json exits 0"
else
	bad "runtime plan --json failed"
	dump "$(cat "$REC/runtime-plan.err")"
fi
if [ -s "$RTP_JSON" ] && python3 - "$RTP_JSON" >"$REC/runtime-plan-check.err" 2>&1 <<'PY'
import json, sys
p = json.load(open(sys.argv[1], encoding="utf-8"))
assert isinstance(p.get("topology"), str) and p["topology"], p.get("topology")
assert isinstance(p.get("facts"), dict), p.get("facts")
assert "supersecret" not in json.dumps(p), "runtime plan leaked proxy password"
PY
then
	ok "runtime plan JSON schema and redaction"
else
	bad "runtime plan JSON schema or redaction failed"
	dump "$(cat "$REC/runtime-plan-check.err")"
fi

REPORT="$REC/report.json"
if env MKBOX_CONFIG="$REC_CFG" "$AMD64" report -o "$REPORT" --state-dir "$REC/state" >"$REC/report.out" 2>&1; then
	ok "report writes evidence JSON"
else
	bad "report failed"
	dump "$(cat "$REC/report.out")"
fi
assert_mode "report file mode is 0600" "$REPORT" 600
if [ -s "$REPORT" ] && python3 - "$REPORT" >"$REC/report-check.err" 2>&1 <<'PY'
import json, sys
r = json.load(open(sys.argv[1], encoding="utf-8"))
for key in ("generatedAt", "version", "host", "plan", "runtime", "journals"):
    assert key in r, key
assert r["plan"]["scope"] == "all", r["plan"].get("scope")
assert "supersecret" not in json.dumps(r), "report leaked proxy password"
assert "ghsecret" not in json.dumps(r), "report leaked GitHub proxy password"
assert "REDACTED" in json.dumps(r), "report did not redact proxy"
PY
then
	ok "report JSON schema and proxy redaction"
else
	bad "report JSON schema or redaction failed"
	dump "$(cat "$REC/report-check.err")"
fi
if env MKBOX_CONFIG="$REC_CFG" "$AMD64" report --json --state-dir "$REC/state" >"$REC/report-stdout.json" 2>/dev/null &&
	python3 -m json.tool "$REC/report-stdout.json" >/dev/null 2>&1; then
	ok "report --json writes valid JSON to stdout"
else
	bad "report --json stdout is not valid JSON"
fi

if [ "$(id -u)" -eq 0 ]; then
	APPLY_STATE="$REC/apply-state"
	check_ok "apply docker --dry-run" env MKBOX_CONFIG="$REC_CFG" "$AMD64" \
		--dry-run -y apply docker --state-dir "$APPLY_STATE"
	assert_absent "apply --dry-run did not create journalled state" "$APPLY_STATE/journals"
else
	skip "apply dry-run tests (requires root)"
fi

if [ "$(id -u)" -eq 0 ]; then
	ROLLBACK_STATE="$REC/rollback-state"
	mkdir -p "$ROLLBACK_STATE/journals"
	if python3 - "$ROLLBACK_STATE/journals/smoke-rollback.json" <<'PY'
import base64, json, sys
journal = {
    "version": 1,
    "id": "smoke-rollback",
    "createdAt": "2026-01-01T00:00:00Z",
    "updatedAt": "2026-01-01T00:00:00Z",
    "scope": "docker",
    "status": "applied",
    "hostname": "smoke",
    "plan": {"generatedAt": "2026-01-01T00:00:00Z", "actions": [
        {"id": "docker.configure", "title": "配置 Docker", "risk": "medium", "reversible": True},
        {"id": "docker.install", "title": "安装 Docker", "risk": "high", "reversible": False},
    ]},
    "actions": [
        {"id": "docker.configure", "title": "配置 Docker", "risk": "medium", "reversible": True, "status": "applied"},
        {"id": "docker.install", "title": "安装 Docker", "risk": "high", "reversible": False, "status": "applied"},
    ],
    "files": [
        {"path": "/etc/mkbox-smoke.conf", "existed": True, "mode": 420,
         "data": base64.b64encode(b"old\n").decode(), "capturedAt": "2026-01-01T00:00:00Z"},
        {"path": "/tmp/mkbox-smoke-evil", "existed": True, "mode": 420,
         "data": base64.b64encode(b"evil\n").decode(), "capturedAt": "2026-01-01T00:00:00Z"},
    ],
    "servicesBefore": {"docker": False},
}
json.dump(journal, open(sys.argv[1], "w", encoding="utf-8"))
PY
	then
		ok "crafted rollback journal is valid"
	else
		bad "failed to craft rollback journal"
	fi
	check_fail_with "rollback requires --force for irreversible actions" "不可逆动作" \
		env MKBOX_CONFIG="$REC_CFG" "$AMD64" -y rollback smoke-rollback --dry-run --state-dir "$ROLLBACK_STATE"
	RB_JSON="$REC/rollback.json"
	if env MKBOX_CONFIG="$REC_CFG" "$AMD64" -y rollback smoke-rollback --dry-run --force --json \
		--state-dir "$ROLLBACK_STATE" >"$RB_JSON" 2>"$REC/rollback.err"; then
		ok "rollback --dry-run --force exits 0"
	else
		bad "rollback --dry-run --force failed"
		dump "$(cat "$REC/rollback.err")"
	fi
	if [ -s "$RB_JSON" ] && python3 - "$RB_JSON" >"$REC/rollback-check.err" 2>&1 <<'PY'
import json, sys
r = json.load(open(sys.argv[1], encoding="utf-8"))
assert r["journalId"] == "smoke-rollback", r
assert r["status"] == "dry-run", r.get("status")
assert "/etc/mkbox-smoke.conf" in r.get("restored", []), r
assert any("超出受管范围" in s for s in r.get("skipped", [])), r
assert any("无法自动撤销" in w for w in r.get("warnings", [])), r
PY
	then
		ok "rollback JSON reports restore, skip and irreversible warnings"
	else
		bad "rollback JSON content mismatch"
		dump "$(cat "$REC/rollback-check.err")"
	fi
	assert_absent "rollback --dry-run did not restore files" "/etc/mkbox-smoke.conf"
	assert_absent "rollback refused to write outside /etc" "/tmp/mkbox-smoke-evil"
	check_fail_with "rollback rejects traversal journal id" "无效的 journal ID" \
		env MKBOX_CONFIG="$REC_CFG" "$AMD64" -y rollback ../escape --dry-run --state-dir "$ROLLBACK_STATE"
	check_fail_with "rollback reports missing journal" "未找到 journal" \
		env MKBOX_CONFIG="$REC_CFG" "$AMD64" -y rollback no-such-journal --dry-run --state-dir "$ROLLBACK_STATE"
	REPORT2="$REC/report-with-journal.json"
	if env MKBOX_CONFIG="$REC_CFG" "$AMD64" report -o "$REPORT2" --state-dir "$ROLLBACK_STATE" >/dev/null 2>&1 &&
		python3 - "$REPORT2" >"$REC/report2-check.err" 2>&1 <<'PY'
import json, sys
r = json.load(open(sys.argv[1], encoding="utf-8"))
assert any(j["id"] == "smoke-rollback" for j in r["journals"]), r["journals"]
PY
	then
		ok "report includes journals from --state-dir"
	else
		bad "report did not list the journal"
		dump "$(cat "$REC/report2-check.err" 2>/dev/null)"
	fi
else
	skip "rollback journal tests (requires root)"
fi

BUNDLE="$REC/bundle"
mkdir -p "$BUNDLE/keys" "$BUNDLE/keys2" "$BUNDLE/files"
printf 'hello mkbox\n' >"$BUNDLE/files/hello.txt"
printf '{"a":1}\n' >"$BUNDLE/files/config.json"
check_ok "bundle keygen" "$AMD64" bundle keygen --out-dir "$BUNDLE/keys" --json
assert_mode "bundle private key mode is 0600" "$BUNDLE/keys/mkbox-bundle.key" 600
assert_mode "bundle public key mode is 0644" "$BUNDLE/keys/mkbox-bundle.pub" 644
check_fail_with "bundle keygen refuses overwrite" "已存在" \
	"$AMD64" bundle keygen --out-dir "$BUNDLE/keys" --json
check_ok "bundle keygen --force" "$AMD64" bundle keygen --out-dir "$BUNDLE/keys" --force --json

build_bundle() {
	local out="$1"
	"$AMD64" bundle build \
		--name demo --version 1.2.3 --platform linux/amd64 --license MIT \
		--source https://example.com/makebox --commit deadbeef \
		--created-at 2026-01-01T00:00:00Z \
		--key "$BUNDLE/keys/mkbox-bundle.key" \
		--file "$BUNDLE/files/hello.txt" --file "$BUNDLE/files/config.json" \
		-o "$out" --json
}
check_ok "bundle build with signature" build_bundle "$BUNDLE/signed.tar.gz"
check_ok "bundle reproducible rebuild" build_bundle "$BUNDLE/rebuilt.tar.gz"
if cmp -s "$BUNDLE/signed.tar.gz" "$BUNDLE/rebuilt.tar.gz"; then
	ok "bundle rebuild is byte-identical"
else
	bad "bundle rebuild differs (not reproducible)"
fi

VERIFY_JSON="$BUNDLE/verify.json"
if "$AMD64" bundle verify "$BUNDLE/signed.tar.gz" --key "$BUNDLE/keys/mkbox-bundle.pub" \
	--require-signature --json >"$VERIFY_JSON" 2>"$BUNDLE/verify.err"; then
	ok "bundle verify with trusted key"
else
	bad "bundle verify failed"
	dump "$(cat "$BUNDLE/verify.err")"
fi
if [ -s "$VERIFY_JSON" ] && python3 - "$VERIFY_JSON" >"$BUNDLE/verify-check.err" 2>&1 <<'PY'
import json, sys
r = json.load(open(sys.argv[1], encoding="utf-8"))
assert r["valid"] is True and r["signed"] is True and r["trusted"] is True, r
assert r["manifest"]["name"] == "demo" and r["manifest"]["version"] == "1.2.3", r["manifest"]
assert r["files"] == 2 and r["keyId"], r
PY
then
	ok "bundle verify JSON reports manifest, files and key"
else
	bad "bundle verify JSON content mismatch"
	dump "$(cat "$BUNDLE/verify-check.err")"
fi

INSPECT_JSON="$BUNDLE/inspect.json"
if "$AMD64" bundle inspect "$BUNDLE/signed.tar.gz" --json >"$INSPECT_JSON" 2>"$BUNDLE/inspect.err"; then
	ok "bundle inspect --json exits 0"
else
	bad "bundle inspect failed"
	dump "$(cat "$BUNDLE/inspect.err")"
fi
if [ -s "$INSPECT_JSON" ] && python3 - "$INSPECT_JSON" >"$BUNDLE/inspect-check.err" 2>&1 <<'PY'
import json, sys
r = json.load(open(sys.argv[1], encoding="utf-8"))
assert r["manifest"]["name"] == "demo", r
assert r["signature"]["algorithm"] == "ed25519", r.get("signature")
assert r["entries"] > 0, r
PY
then
	ok "bundle inspect JSON exposes SPDX/SBOM and signature"
else
	bad "bundle inspect JSON content mismatch"
	dump "$(cat "$BUNDLE/inspect-check.err")"
fi

check_ok "bundle build unsigned" "$AMD64" bundle build --name unsigned --version 0.0.1 \
	--file "$BUNDLE/files/hello.txt" -o "$BUNDLE/unsigned.tar.gz" --json
check_fail_with "unsigned bundle fails --require-signature" "要求签名" \
	"$AMD64" bundle verify "$BUNDLE/unsigned.tar.gz" --require-signature
check_fail_with "bundle build requires --file" "至少需要一个 --file" \
	"$AMD64" bundle build --name empty
check_fail_with "bundle build rejects bad platform" "--platform 必须" \
	"$AMD64" bundle build --file "$BUNDLE/files/hello.txt" --platform bogus -o "$BUNDLE/bad-platform.tar.gz"
printf 'reserved\n' >"$BUNDLE/files/manifest.json"
check_fail_with "bundle build rejects reserved file names" "与保留文件冲突" \
	"$AMD64" bundle build --file "$BUNDLE/files/manifest.json" -o "$BUNDLE/reserved.tar.gz"

if python3 - "$BUNDLE/signed.tar.gz" "$BUNDLE/tampered.tar.gz" <<'PY'
import sys
data = bytearray(open(sys.argv[1], "rb").read())
data[len(data) // 2] ^= 0xFF
open(sys.argv[2], "wb").write(data)
PY
then
	check_fail "bundle verify rejects tampered archive" \
		"$AMD64" bundle verify "$BUNDLE/tampered.tar.gz" --key "$BUNDLE/keys/mkbox-bundle.pub"
	check_ok "untrusted key generation" "$AMD64" bundle keygen --out-dir "$BUNDLE/keys2" --json
	check_fail "bundle verify rejects untrusted key" \
		"$AMD64" bundle verify "$BUNDLE/signed.tar.gz" --key "$BUNDLE/keys2/mkbox-bundle.pub"
else
	skip "bundle tamper tests (python3 unavailable)"
fi

if command -v python3 >/dev/null 2>&1; then
	cat >"$BUNDLE/registry.py" <<'PY'
import hashlib
import http.server
import sys
import urllib.parse
import uuid

blobs = {}
manifests = {}


class Handler(http.server.BaseHTTPRequestHandler):
    def respond(self, code, data=b"", headers=None):
        self.send_response(code)
        for key, value in (headers or {}).items():
            self.send_header(key, value)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        if self.command != "HEAD" and data:
            self.wfile.write(data)

    def do_HEAD(self):
        path = urllib.parse.urlparse(self.path).path
        if path == "/v2/":
            self.respond(200)
            return
        if "/blobs/" in path:
            digest = path.rsplit("/blobs/", 1)[1]
            if digest in blobs:
                self.respond(200, headers={"Docker-Content-Digest": digest})
            else:
                self.respond(404)
            return
        self.respond(404)

    def do_POST(self):
        path = urllib.parse.urlparse(self.path).path
        if path.endswith("/blobs/uploads/"):
            location = path + uuid.uuid4().hex
            self.respond(202, headers={"Location": location})
            return
        self.respond(404)

    def do_PUT(self):
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        size = int(self.headers.get("Content-Length", "0"))
        data = self.rfile.read(size)
        if "/blobs/uploads/" in path:
            digest = urllib.parse.parse_qs(parsed.query).get("digest", [""])[0]
            got = "sha256:" + hashlib.sha256(data).hexdigest()
            if digest and digest != got:
                self.respond(400, b"digest mismatch")
                return
            blobs[got] = data
            self.respond(201, headers={"Docker-Content-Digest": got})
            return
        if "/manifests/" in path:
            tag = urllib.parse.unquote(path.rsplit("/manifests/", 1)[1])
            digest = "sha256:" + hashlib.sha256(data).hexdigest()
            manifests[tag] = (data, digest)
            self.respond(201, headers={"Docker-Content-Digest": digest})
            return
        self.respond(404)

    def do_GET(self):
        path = urllib.parse.urlparse(self.path).path
        if path == "/v2/":
            self.respond(200)
            return
        if "/manifests/" in path:
            tag = urllib.parse.unquote(path.rsplit("/manifests/", 1)[1])
            if tag in manifests:
                data, digest = manifests[tag]
                self.respond(200, data, {
                    "Content-Type": "application/vnd.oci.image.manifest.v1+json",
                    "Docker-Content-Digest": digest,
                })
            else:
                self.respond(404)
            return
        if "/blobs/" in path:
            digest = path.rsplit("/blobs/", 1)[1]
            if digest in blobs:
                self.respond(200, blobs[digest], {"Docker-Content-Digest": digest})
            else:
                self.respond(404)
            return
        self.respond(404)

    def log_message(self, *args):
        pass


http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
PY
	REG_PORT=$(free_port)
	start_bg "$BUNDLE/registry.log" python3 "$BUNDLE/registry.py" "$REG_PORT"
	if wait_http "http://127.0.0.1:$REG_PORT/v2/"; then
		ok "local OCI registry is ready"
	else
		bad "local OCI registry failed to start"
	fi
	REG_REF="127.0.0.1:$REG_PORT/smoke/repo:1.2.3"
	PUSH_JSON="$BUNDLE/push.json"
	if "$AMD64" bundle push "$BUNDLE/signed.tar.gz" --ref "$REG_REF" --plain-http --json \
		>"$PUSH_JSON" 2>"$BUNDLE/push.err"; then
		ok "bundle push to local OCI registry"
	else
		bad "bundle push failed"
		dump "$(cat "$BUNDLE/push.err")"
	fi
	if [ -s "$PUSH_JSON" ] && python3 - "$PUSH_JSON" "$REG_REF" >"$BUNDLE/push-check.err" 2>&1 <<'PY'
import json, sys
r = json.load(open(sys.argv[1], encoding="utf-8"))
assert r["reference"] == sys.argv[2], (r.get("reference"), sys.argv[2])
assert r["manifestDigest"].startswith("sha256:"), r
assert r["bundleDigest"].startswith("sha256:"), r
assert r["size"] > 0, r
PY
	then
		ok "bundle push JSON reports digests"
	else
		bad "bundle push JSON content mismatch"
		dump "$(cat "$BUNDLE/push-check.err")"
	fi
	PULL_JSON="$BUNDLE/pull.json"
	if "$AMD64" bundle pull --ref "$REG_REF" -o "$BUNDLE/pulled.tar.gz" --plain-http --json \
		>"$PULL_JSON" 2>"$BUNDLE/pull.err"; then
		ok "bundle pull from local OCI registry"
	else
		bad "bundle pull failed"
		dump "$(cat "$BUNDLE/pull.err")"
	fi
	if cmp -s "$BUNDLE/signed.tar.gz" "$BUNDLE/pulled.tar.gz"; then
		ok "pulled bundle is byte-identical to the pushed artifact"
	else
		bad "pulled bundle differs from the pushed artifact"
	fi
	assert_mode "pulled bundle mode is 0644" "$BUNDLE/pulled.tar.gz" 644
	check_ok "pulled bundle verifies with trusted key" \
		"$AMD64" bundle verify "$BUNDLE/pulled.tar.gz" --key "$BUNDLE/keys/mkbox-bundle.pub" --require-signature
	check_fail_with "bundle push verifies before upload" "推送前校验失败" \
		"$AMD64" bundle push "$BUNDLE/tampered.tar.gz" --ref "127.0.0.1:$REG_PORT/smoke/repo:tampered" --plain-http
	check_fail_with "bundle pull reports missing tag" "HTTP 404" \
		"$AMD64" bundle pull --ref "127.0.0.1:$REG_PORT/smoke/repo:missing" \
		-o "$BUNDLE/missing.tar.gz" --plain-http
	stop_servers
else
	skip "OCI registry push/pull tests (python3 unavailable)"
fi

# ---------------------------------------------------------------------------
section "13. Unprivileged behaviour"

if [ "$(id -u)" -eq 0 ] && command -v setpriv >/dev/null 2>&1; then
	NB="$WORK/nobody"
	mkdir -p "$NB/data" "$NB/backups"
	cp "$AMD64" "$NB/mkbox"
	chmod 755 "$NB/mkbox"
	printf 'nobody data\n' >"$NB/data/file.txt"
	chown -R 65534:65534 "$NB"
	chmod 711 "$WORK"
	out=$(setpriv --reuid=65534 --regid=65534 --clear-groups \
		env HOME="$NB" MKBOX_CONFIG="$NB/config.json" NO_COLOR=1 \
		"$NB/mkbox" --dry-run -y docker install 2>&1)
	if [ $? -eq 0 ]; then
		ok "unprivileged dry-run is allowed"
	else
		bad "unprivileged dry-run failed"
		dump "$out"
	fi
	out=$(setpriv --reuid=65534 --regid=65534 --clear-groups \
		env HOME="$NB" MKBOX_CONFIG="$NB/config.json" NO_COLOR=1 \
		"$NB/mkbox" -y docker install --mirror official 2>&1)
	if [ $? -ne 0 ] && printf '%s' "$out" | grep -q '需要 root'; then
		ok "unprivileged real install is denied"
	else
		bad "unprivileged real install should be denied"
		dump "$out"
	fi
	out=$(setpriv --reuid=65534 --regid=65534 --clear-groups \
		env HOME="$NB" MKBOX_CONFIG="$NB/config.json" NO_COLOR=1 \
		"$NB/mkbox" -y backup create --path "$NB/data" --dir "$NB/backups" 2>&1)
	if [ $? -eq 0 ] && [ -n "$(find "$NB/backups" -name '*.tar.gz' 2>/dev/null)" ]; then
		ok "unprivileged backup create works"
	else
		bad "unprivileged backup create failed"
		dump "$out"
	fi
else
	skip "unprivileged tests (requires root and setpriv)"
fi

# ---------------------------------------------------------------------------
section "14. Environment-only diagnostics (non-fatal)"

out=$("$AMD64" doctor 2>&1)
rc=$?
if [ "$rc" -eq 0 ]; then
	ok "doctor reports a healthy host"
else
	skip "doctor exited $rc in this sandbox (systemd/port checks are environment-specific)"
fi

# ---------------------------------------------------------------------------
stop_servers
printf '\n%sSummary: %d passed, %d failed, %d skipped%s\n' "$C_BOLD" "$PASS" "$FAIL" "$SKIP" "$C_RESET"
if [ "$FAIL" -ne 0 ]; then
	printf '%sFailed checks:%s\n' "$C_RED" "$C_RESET"
	for f in "${FAILED[@]}"; do
		printf '  - %s\n' "$f"
	done
	exit 1
fi
printf '%sAll strict smoke checks passed.%s\n' "$C_GREEN" "$C_RESET"
