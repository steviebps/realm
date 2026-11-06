# Releasing realm

One tag releases everything, at one version:

| Artifact | Where | Published by |
|---|---|---|
| Binaries for Linux, macOS, Windows, and a GitHub release with a changelog | [Releases](https://github.com/steviebps/realm/releases) | GoReleaser (`.goreleaser.yml`) |
| Homebrew cask | `steviebps/homebrew-realm` | GoReleaser |
| Container image (`linux/amd64`, `linux/arm64`), with provenance and an SBOM | `ghcr.io/steviebps/realm` | `docker/build-push-action` |
| TypeScript SDK, with npm provenance | [`@steviebps/realm-sdk`](https://www.npmjs.com/package/@steviebps/realm-sdk) | `npm publish`, once enabled (see [npm](#npm)) |

Pushing a tag such as `v0.3.0` runs `.github/workflows/release.yml`. It runs
every CI check again on the tagged commit (`ci.yml`), and only if they pass
does it publish the artifacts above, in parallel.

## Versions

realm follows [semantic versioning](https://semver.org). Before 1.0:

- **Minor** (`v0.3.0`) for new features **and** for changes that can break a
  caller: a stricter API, a changed default, a removed field. List those under
  "Behavior changes" in the release notes.
- **Patch** (`v0.3.1`) for fixes that change nothing callers rely on.

The Go module, the server, the image, and the npm package share the version,
even when only one of them changed.

## Cut a release

1. Make sure master is green (the `ci-ok` check) and holds what you want to
   ship.
2. Tag and push:

   ```bash
   git switch master && git pull
   git tag -a v0.3.0 -m "v0.3.0"
   git push origin v0.3.0
   ```

3. Watch the `release` workflow. When it finishes, check the GitHub release
   notes and edit in the behavior changes.

## Prereleases

Tag `v0.3.0-rc.1` (any `-suffix`) to try a release first. A prerelease:

- is marked as a prerelease on GitHub,
- does not update the Homebrew cask,
- gets only its own image tag (`0.3.0-rc.1`): not `0.3` or `latest`,
- is published to npm under the `next` dist-tag, so `npm install` keeps
  getting the latest release.

Use one before any release that changes the release pipeline itself.

## If a release fails

Each artifact is its own job. If one fails after others succeeded, fix the
cause and re-run the failed jobs from the workflow run. Re-run only the
failed jobs: GoReleaser refuses to replace an existing release, and npm
refuses to republish a version. If something wrong shipped, publish a new
patch version instead of moving the tag.

## One-time setup

- **Homebrew tap token.** Create a fine-grained personal access token with
  access to `steviebps/homebrew-realm` only and the **Contents: read and
  write** permission. Save it as the repository secret
  `HOMEBREW_TAP_TOKEN`. Set a calendar reminder for its expiry. The old
  `PUBLISH_TOKEN` secret is no longer used and can be deleted.
- **GHCR.** The image is pushed with the workflow's own token. After the
  first push, set the package's visibility to public in its settings if it
  isn't already.
- **Required check.** In branch protection for `master`, require the `ci-ok`
  status check. It passes only when every CI job passed.

## npm

The `npm` job is skipped until the repository variable `NPM_PUBLISH` is
`true`, so releases can ship the binaries and the image before the package
is set up on npm. To turn it on:

1. **Publish the first version from your machine.** npm only lets you add a
   trusted publisher to a package that already exists. Use a version that
   can't collide with a future tag, such as `0.0.1`:

   ```bash
   cd sdk/typescript && npm ci && npm version 0.0.1 --no-git-tag-version
   npm publish --access public
   git checkout package.json package-lock.json
   ```

2. **Add the trusted publisher, shortly before the next tag.** On
   npmjs.com, open the package's settings, add a trusted publisher for
   GitHub Actions with repository `steviebps/realm` and workflow
   `release.yml`, and disallow token publishing. npm expires a trusted
   publisher that hasn't published within 48 hours of being added, so do
   this when you are about to release, and add it again if it lapses.
3. **Enable the job.** Under Settings → Secrets and variables → Actions →
   Variables, add the repository variable `NPM_PUBLISH` with the value
   `true`.

Releases then need no npm token. Versions released while the job was
skipped are not on npm; the next tag publishes the SDK at that tag's
version.
