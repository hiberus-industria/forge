#!/bin/sh
# forge installer — Linux, macOS and WSL2.
#
#   curl -fsSL https://raw.githubusercontent.com/hiberus-industria/forge/main/install.sh | sh
#
# The repository is private: the script uses the GitHub CLI (`gh`) when it is
# installed and authenticated, or GH_TOKEN/GITHUB_TOKEN with curl otherwise.
#
# Environment:
#   FORGE_VERSION      version to install (e.g. v1.2.0). Default: latest release.
#   FORGE_INSTALL_DIR  target directory. Default: ~/.local/bin
#   GH_TOKEN           GitHub token with read access to hiberus-industria/forge.
set -eu

REPO="hiberus-industria/forge"
INSTALL_DIR="${FORGE_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${FORGE_VERSION:-}"
TOKEN="${GH_TOKEN:-${GITHUB_TOKEN:-}}"

if [ -t 1 ]; then
	BOLD=$(printf '\033[1m'); DIM=$(printf '\033[2m'); GREEN=$(printf '\033[32m')
	RED=$(printf '\033[31m'); MAGENTA=$(printf '\033[35m'); RESET=$(printf '\033[0m')
else
	BOLD=""; DIM=""; GREEN=""; RED=""; MAGENTA=""; RESET=""
fi

say()  { printf '%s\n' "$*"; }
step() { printf '%s›%s %s\n' "$MAGENTA" "$RESET" "$*"; }
ok()   { printf '%s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
die()  { printf '%s✗ %s%s\n' "$RED" "$*" "$RESET" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"; }

detect_platform() {
	os=$(uname -s | tr '[:upper:]' '[:lower:]')
	case "$os" in
		linux) os=linux ;; # WSL2 reports linux as well
		darwin) os=darwin ;;
		*) die "unsupported OS: $os (forge supports Linux, macOS and WSL2)" ;;
	esac
	arch=$(uname -m)
	case "$arch" in
		x86_64|amd64) arch=amd64 ;;
		aarch64|arm64) arch=arm64 ;;
		*) die "unsupported architecture: $arch" ;;
	esac
	if [ "$os" = linux ] && grep -qi microsoft /proc/version 2>/dev/null; then
		PLATFORM_NOTE=" (WSL2)"
	else
		PLATFORM_NOTE=""
	fi
}

use_gh() {
	command -v gh >/dev/null 2>&1 && gh auth status -h github.com >/dev/null 2>&1
}

api_get() { # $1: path under https://api.github.com
	if [ -n "$TOKEN" ]; then
		curl -fsSL -H "Authorization: Bearer $TOKEN" -H "Accept: application/vnd.github+json" "https://api.github.com$1"
	else
		curl -fsSL -H "Accept: application/vnd.github+json" "https://api.github.com$1"
	fi
}

resolve_version() {
	[ -n "$VERSION" ] && return
	if use_gh; then
		VERSION=$(gh release view --repo "$REPO" --json tagName --jq .tagName) || die "cannot read latest release"
	else
		VERSION=$(api_get "/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
	fi
	[ -n "$VERSION" ] || die "cannot resolve latest version (private repo? set GH_TOKEN or run 'gh auth login')"
}

download() { # $1: asset name, $2: destination dir
	if use_gh; then
		gh release download "$VERSION" --repo "$REPO" --pattern "$1" --dir "$2" --clobber
		return
	fi
	# Private repositories need the asset API URL plus an octet-stream Accept header.
	url=$(api_get "/repos/$REPO/releases/tags/$VERSION" |
		tr ',' '\n' | grep -B3 "\"name\": *\"$1\"" | sed -n 's/.*"url": *"\([^"]*\/assets\/[0-9]*\)".*/\1/p' | head -n 1)
	[ -n "$url" ] || die "asset $1 not found in release $VERSION"
	if [ -n "$TOKEN" ]; then
		curl -fsSL -H "Authorization: Bearer $TOKEN" -H "Accept: application/octet-stream" -o "$2/$1" "$url"
	else
		curl -fsSL -H "Accept: application/octet-stream" -o "$2/$1" "$url"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

main() {
	need uname; need tar; need curl
	say ""
	say "${BOLD}${MAGENTA}  forge${RESET}${DIM} — trazabilidad del gasto IA por issue${RESET}"
	say ""

	detect_platform
	step "Platform: ${BOLD}${os}/${arch}${RESET}${PLATFORM_NOTE}"
	resolve_version
	step "Version:  ${BOLD}${VERSION}${RESET}"

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	asset="forge_${os}_${arch}.tar.gz"
	step "Downloading ${asset}"
	download "$asset" "$tmp"
	download checksums.txt "$tmp"

	expected=$(grep " ${asset}\$" "$tmp/checksums.txt" | cut -d' ' -f1)
	[ -n "$expected" ] || die "no checksum for $asset"
	actual=$(sha256 "$tmp/$asset")
	[ "$expected" = "$actual" ] || die "checksum mismatch for $asset"
	ok "Checksum verified"

	tar -xzf "$tmp/$asset" -C "$tmp" forge
	mkdir -p "$INSTALL_DIR"
	install -m 0755 "$tmp/forge" "$INSTALL_DIR/forge" 2>/dev/null || {
		cp "$tmp/forge" "$INSTALL_DIR/forge" && chmod 0755 "$INSTALL_DIR/forge"
	}
	ok "Installed ${BOLD}$INSTALL_DIR/forge${RESET}"

	case ":$PATH:" in
		*":$INSTALL_DIR:"*) ;;
		*) say ""; say "${DIM}Add $INSTALL_DIR to your PATH:${RESET}"
		   say "  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
	esac

	say ""
	say "${BOLD}Next steps${RESET}"
	say "  1. forge login            ${DIM}# Keycloak (browser) + link your GitHub account${RESET}"
	say "  2. forge setup            ${DIM}# wire Claude Code and OpenCode to the daemon${RESET}"
	say "  3. forge daemon install   ${DIM}# run the daemon as a user service${RESET}"
	say "  4. forge                  ${DIM}# open the TUI${RESET}"
	say ""
}

main "$@"
