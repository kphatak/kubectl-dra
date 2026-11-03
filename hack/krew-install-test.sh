#!/usr/bin/env bash
# Installs the rendered krew manifest with every platform archive in dist/,
# the way `kubectl krew install` would for a user on that platform. Krew
# checks each archive against the manifest's sha256, extracts the files the
# manifest lists, and links the binary. Uses a throwaway KREW_ROOT, so the
# user's own krew installation is not touched. The binary for the current
# platform is also run.
#
# Usage: hack/krew-install-test.sh TAG [DIST_DIR]

set -euo pipefail

tag="${1:?usage: $0 TAG [DIST_DIR]}"
dist="${2:-dist}"
manifest="${dist}/dra.yaml"

if ! kubectl krew version >/dev/null 2>&1; then
	echo "kubectl krew is not installed; skipping the install test" >&2
	exit 0
fi

roots="$(mktemp -d)"
trap 'rm -rf "${roots}"' EXIT

host_os="$(go env GOOS)"
host_arch="$(go env GOARCH)"

for platform in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
	os="${platform%/*}"
	arch="${platform#*/}"
	# A fresh root per platform: krew uninstall needs a plugin index,
	# which a throwaway root does not have, and install --manifest does not.
	export KREW_ROOT="${roots}/${os}-${arch}"
	archive="${dist}/kubectl-dra_${tag}_${os}_${arch}.tar.gz"
	KREW_OS="${os}" KREW_ARCH="${arch}" kubectl krew install --manifest="${manifest}" --archive="${archive}" >/dev/null 2>&1 || {
		echo "${platform}: install failed; rerun with -v=4:" >&2
		KREW_OS="${os}" KREW_ARCH="${arch}" kubectl krew install --manifest="${manifest}" --archive="${archive}" -v=4 >&2 || true
		exit 1
	}
	bin="kubectl-dra"
	[[ "${os}" == windows ]] && bin="kubectl-dra.exe"
	[[ -f "${KREW_ROOT}/store/dra/${tag}/LICENSE" ]] || { echo "${platform}: LICENSE was not extracted" >&2; exit 1; }
	[[ -f "${KREW_ROOT}/store/dra/${tag}/${bin}" ]] || { echo "${platform}: ${bin} was not extracted" >&2; exit 1; }
	if [[ "${os}" == "${host_os}" && "${arch}" == "${host_arch}" ]]; then
		echo "${platform}: installed; kubectl-dra version prints $("${KREW_ROOT}/bin/kubectl-dra" version)"
	else
		echo "${platform}: installed"
	fi
done
