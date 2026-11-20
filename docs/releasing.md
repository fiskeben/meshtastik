# Releasing

Releases are built by [GoReleaser](https://goreleaser.com) from `.goreleaser.yaml`. Pushing a `v*` tag runs `.github/workflows/release.yml` on a macOS runner, which:

1. runs `make check`;
2. builds darwin amd64/arm64 (with cgo), linux amd64/arm64 and windows amd64;
3. creates a GitHub release with the archives, `checksums.txt` and a changelog grouped from `feat:` / `fix:` commits;
4. commits `Casks/meshtastik.rb` to [fiskeben/homebrew-tap](https://github.com/fiskeben/homebrew-tap).

## One-time setup

The default `GITHUB_TOKEN` can't push to another repository, so the tap needs its own token.

1. Create a fine-grained personal access token at <https://github.com/settings/personal-access-tokens/new>. Limit it to the `fiskeben/homebrew-tap` repository and give it **Contents: Read and write**.
2. Store it as a secret on this repository:

   ```
   gh secret set HOMEBREW_TAP_GITHUB_TOKEN --repo fiskeben/meshtastik
   ```

When the token expires, the release itself still publishes but the tap step fails. Renew the token, update the secret, and re-run the failed job.

## Cutting a release

```
git checkout main && git pull
make check
git tag v0.1.0
git push origin v0.1.0
```

Follow the run under the repository's Actions tab. Tags with a pre-release suffix (`v0.2.0-rc1`) are marked as pre-releases on GitHub and don't update the tap.

The version shown by `meshtastik version` is the tag without its leading `v`.

## Dry run

```
make snapshot
```

This builds everything into `dist/` without publishing. The cask it would push is at `dist/homebrew/Casks/meshtastik.rb`. `make release-check` only validates the config. Both run GoReleaser through `go run`, so it doesn't need to be installed.

## The Homebrew cask

GoReleaser has deprecated formulas in favour of casks, so meshtastik ships as a cask. It covers macOS and Linux (Intel and ARM). The binaries aren't signed or notarized, so a post-install step clears `com.apple.quarantine`; without it, Gatekeeper refuses to run the downloaded binary. Signing would need a paid Apple Developer account.
