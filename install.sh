#!/bin/sh
# Install mkbox on Linux from a release archive or a local build.
set -eu

REPO="${MKBOX_REPO:-makebox/makebox}"
BASE_URL="${MKBOX_BASE_URL:-https://github.com/${REPO}/releases/latest/download}"
PREFIX="/usr/local/bin"
LOCAL=""

usage() {
	cat <<'EOF'
用法: sudo ./install.sh [选项]

选项:
  --prefix <目录>   安装目录（默认 /usr/local/bin）
  --local <文件>    从本地二进制或 tar.gz 安装，不联网
  -h, --help        显示帮助

环境变量:
  MKBOX_VERSION     指定发布版本标签，如 v0.2.0
  MKBOX_BASE_URL    覆盖下载基础地址
  MKBOX_REPO        GitHub 仓库，默认 makebox/makebox
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--prefix)
		[ $# -ge 2 ] || { echo "缺少 --prefix 参数" >&2; exit 2; }
		PREFIX="$2"
		shift 2
		;;
	--local)
		[ $# -ge 2 ] || { echo "缺少 --local 参数" >&2; exit 2; }
		LOCAL="$2"
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "未知参数: $1" >&2
		usage >&2
		exit 2
		;;
	esac
done

if [ "$(uname -s)" != "Linux" ]; then
	echo "mkbox 仅支持 Linux，当前系统: $(uname -s)" >&2
	exit 1
fi

case "$(uname -m)" in
x86_64 | amd64) ARCH="amd64" ;;
aarch64 | arm64) ARCH="arm64" ;;
armv7l | armv7 | armhf | armv6l) ARCH="arm" ;;
*)
	echo "不支持的 CPU 架构: $(uname -m)" >&2
	exit 1
	;;
esac

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT HUP INT TERM

fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 --connect-timeout 15 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		echo "需要 curl 或 wget 才能下载" >&2
		return 1
	fi
}

verify_checksum() {
	archive="$1"
	checksums="$2"
	[ -s "$checksums" ] || { echo "checksums.txt 为空，拒绝安装未校验的产物" >&2; return 1; }
	want="$(awk -v name="mkbox-linux-${ARCH}.tar.gz" '$2 == name { print $1; exit }' "$checksums")"
	[ -n "$want" ] || { echo "校验文件中没有 mkbox-linux-${ARCH}.tar.gz" >&2; return 1; }
	if command -v sha256sum >/dev/null 2>&1; then
		got="$(sha256sum "$archive" | awk '{print $1}')"
	elif command -v shasum >/dev/null 2>&1; then
		got="$(shasum -a 256 "$archive" | awk '{print $1}')"
	elif command -v openssl >/dev/null 2>&1; then
		got="$(openssl dgst -sha256 "$archive" | awk '{print $NF}')"
	else
		echo "错误: 未找到 sha256sum、shasum 或 openssl，无法校验产物" >&2
		return 1
	fi
	[ "$want" = "$got" ] || {
		echo "SHA-256 校验失败" >&2
		echo "期望: $want" >&2
		echo "实际: $got" >&2
		return 1
	}
}

if [ -n "$LOCAL" ]; then
	[ -e "$LOCAL" ] || { echo "本地文件不存在: $LOCAL" >&2; exit 1; }
	case "$LOCAL" in
	*.tar.gz | *.tgz)
		tar -xzf "$LOCAL" -C "$TMP_DIR"
		;;
	*)
		cp "$LOCAL" "$TMP_DIR/mkbox"
		;;
	esac
else
	if [ -n "${MKBOX_VERSION:-}" ]; then
		BASE_URL="https://github.com/${REPO}/releases/download/${MKBOX_VERSION}"
	fi
	ARCHIVE_URL="${BASE_URL}/mkbox-linux-${ARCH}.tar.gz"
	echo "下载 ${ARCHIVE_URL}"
	fetch "$ARCHIVE_URL" "$TMP_DIR/mkbox.tar.gz"
	if ! fetch "${BASE_URL}/checksums.txt" "$TMP_DIR/checksums.txt"; then
		echo "错误: 无法下载 checksums.txt，拒绝安装未校验的产物" >&2
		echo "离线安装请使用 --local" >&2
		exit 1
	fi
	verify_checksum "$TMP_DIR/mkbox.tar.gz" "$TMP_DIR/checksums.txt"
	tar -xzf "$TMP_DIR/mkbox.tar.gz" -C "$TMP_DIR"
fi

if [ ! -f "$TMP_DIR/mkbox" ]; then
	echo "安装包中未找到 mkbox 可执行文件" >&2
	exit 1
fi

if [ ! -d "$PREFIX" ]; then
	mkdir -p "$PREFIX" 2>/dev/null || {
		echo "无法创建目录 $PREFIX，请使用 sudo 运行" >&2
		exit 1
	}
fi
if [ ! -w "$PREFIX" ] && [ "$(id -u)" -ne 0 ]; then
	echo "目录 $PREFIX 不可写，请使用 sudo 运行" >&2
	exit 1
fi

cp "$TMP_DIR/mkbox" "$PREFIX/mkbox"
chmod 0755 "$PREFIX/mkbox"
echo "已安装到 $PREFIX/mkbox"
"$PREFIX/mkbox" version || true
