#!/usr/bin/env bash
# Renders .krew.yaml for the archives in dist/ the way krew-release-bot does
# on a real release: {{ .TagName }} becomes the tag, and each
# {{addURIAndSha "URL" .TagName}} becomes the URL and the archive's sha256
# from the goreleaser checksums file.
#
# Usage: hack/krew-manifest.sh TAG [DIST_DIR] > dra.yaml

set -euo pipefail

tag="${1:?usage: $0 TAG [DIST_DIR]}"
dist="${2:-dist}"
checksums="${dist}/kubectl-dra_${tag}_checksums.txt"
[[ -f "${checksums}" ]] || { echo "missing ${checksums}; run make release-snapshot first" >&2; exit 1; }

placeholder='{{ .TagName }}'
uri_and_sha='^([[:space:]]*)\{\{addURIAndSha "([^"]+)" \.TagName \}\}$'

while IFS= read -r line; do
	if [[ "${line}" =~ ${uri_and_sha} ]]; then
		indent="${BASH_REMATCH[1]}"
		url="${BASH_REMATCH[2]//"${placeholder}"/${tag}}"
		file="${url##*/}"
		sha="$(awk -v f="${file}" '$2 == f { print $1 }' "${checksums}")"
		[[ -n "${sha}" ]] || { echo "no checksum for ${file} in ${checksums}" >&2; exit 1; }
		printf '%suri: %s\n%ssha256: %s\n' "${indent}" "${url}" "${indent}" "${sha}"
	else
		printf '%s\n' "${line//"${placeholder}"/${tag}}"
	fi
done < .krew.yaml
