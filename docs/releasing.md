# Releasing

Releases are GitHub Releases of this repository. Pushing a tag such as
`v0.1.0` runs [.github/workflows/release.yaml](../.github/workflows/release.yaml),
which runs the unit tests, then [goreleaser](../.goreleaser.yaml), which
builds `kubectl-dra` for Linux, macOS and Windows on amd64 and arm64 and
uploads one `.tar.gz` per platform (binary plus `LICENSE`) and a checksums
file to the release for that tag. Krew installs from those URLs; nothing is
hosted anywhere else.

Tags must be [semantic versions](https://semver.org) with a `v` prefix. A tag
with a suffix, such as `v0.2.0-rc.1`, is published as a pre-release and is
never sent to krew.

## Before the first release

- The repository must be public. Krew users download release archives
  without credentials, and krew-index only accepts open source plugins.
- CI on `main` should be green.

## Check a release locally

Nothing here publishes anything.

```bash
make release-check      # validate .goreleaser.yaml
make release-snapshot   # build every archive into dist/
make krew-check         # validate the krew manifest and install every archive
```

`krew-check` renders [.krew.yaml](../.krew.yaml) against the archives in
`dist/` the way krew-release-bot does on a real release
([hack/krew-manifest.sh](../hack/krew-manifest.sh)), runs krew-index's own
manifest validation on it, and installs the archive for every platform with
`kubectl krew install --manifest --archive` into a throwaway `KREW_ROOT`
([hack/krew-install-test.sh](../hack/krew-install-test.sh)). Krew checks each
archive against the manifest's sha256 and extracts the binary and `LICENSE`.
Your own krew installation is not touched. The install test is skipped if
krew is not installed.

Snapshot archives are named after the latest tag (`v0.0.0` before the first
one) and are not the archives a real release publishes.

## Cut a release

```bash
git tag -s v0.1.0 -m "v0.1.0"
```

```bash
git push origin v0.1.0
```

Then watch the Release workflow under the repository's Actions tab. When it
finishes, the release page lists six archives and
`kubectl-dra_v0.1.0_checksums.txt`.

## Submit to krew (first release only)

[krew-release-bot](https://github.com/rajatjindal/krew-release-bot) only
updates plugins that are already in krew-index, so the first version goes in
by hand.

1. Render the manifest from the published checksums:

   ```bash
   mkdir -p /tmp/kubectl-dra-v0.1.0
   ```

   ```bash
   curl -fsSL -o /tmp/kubectl-dra-v0.1.0/kubectl-dra_v0.1.0_checksums.txt https://github.com/kphatak/kubectl-dra/releases/download/v0.1.0/kubectl-dra_v0.1.0_checksums.txt
   ```

   ```bash
   hack/krew-manifest.sh v0.1.0 /tmp/kubectl-dra-v0.1.0 > /tmp/kubectl-dra-v0.1.0/dra.yaml
   ```

2. Validate it, this time including the download from the real URLs:

   ```bash
   bin/validate-krew-manifest-v0.5.0 -manifest /tmp/kubectl-dra-v0.1.0/dra.yaml
   ```

3. Install it once as a user would, then remove it:

   ```bash
   kubectl krew install --manifest=/tmp/kubectl-dra-v0.1.0/dra.yaml
   ```

   ```bash
   kubectl krew uninstall dra
   ```

4. Fork [kubernetes-sigs/krew-index](https://github.com/kubernetes-sigs/krew-index),
   add the manifest as `plugins/dra.yaml`, and open a pull request. A krew
   maintainer reviews it against the
   [naming guide](https://krew.sigs.k8s.io/docs/developer-guide/develop/naming-guide/)
   and [best practices](https://krew.sigs.k8s.io/docs/developer-guide/develop/best-practices/).

## Later releases

Once the first submission is merged, set the repository variable
`KREW_INDEX_PUBLISHED` to `true` (Settings, Secrets and variables, Actions,
Variables). From then on, the Release workflow's last step runs
krew-release-bot, which opens a pull request in krew-index for every new
tag. Version bumps are usually merged automatically within minutes.

## Tool versions

goreleaser, the krew manifest validator, golangci-lint and kind are pinned in
the [Makefile](../Makefile) and built from source into `bin/` on first use.
