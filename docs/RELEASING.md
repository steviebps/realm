# Releasing realm

One tag releases everything, at one version:

| Artifact | Where | Published by |
|---|---|---|
| Binaries for Linux, macOS, Windows (macOS ones signed and notarized, once set up: see [macOS signing](#macos-signing)), and a GitHub release with a changelog | [Releases](https://github.com/steviebps/realm/releases) | GoReleaser (`.goreleaser.yml`) |
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
- **macOS signing.** See [macOS signing](#macos-signing).
- **Required check.** In branch protection for `master`, require the `ci-ok`
  status check. It passes only when every CI job passed.

## macOS signing

Unsigned macOS binaries are blocked by Gatekeeper: after a Homebrew install
or a browser download, macOS refuses to run `realm` until the user allows it
in System Settings → Privacy & Security. GoReleaser signs the macOS binaries
with a Developer ID certificate and notarizes them with Apple (from the
Linux runner; no Mac needed in CI) when the repository variable
`MACOS_NOTARIZE` is `true`. With it on, a missing secret or a rejection by
Apple fails the release instead of shipping unsigned binaries. To turn it
on:

1. **Join the [Apple Developer Program](https://developer.apple.com/programs/)**
   ($99 a year). An individual membership works; the certificate then
   carries your name.
2. **Create a Developer ID Application certificate.** Only the account
   holder can.
   1. In Keychain Access, choose Certificate Assistant → Request a
      Certificate From a Certificate Authority, enter your email, and save
      the request to disk.
   2. On [developer.apple.com](https://developer.apple.com/account/resources/certificates/list),
      add a certificate of type **Developer ID Application** and upload the
      request.
   3. Download the certificate and open it to add it to your keychain.
   4. In Keychain Access → My Certificates, right-click
      "Developer ID Application: …" and export it as a `.p12` file with a
      password.
3. **Create an App Store Connect API key** for notarization. In
   [App Store Connect](https://appstoreconnect.apple.com/access/integrations/api),
   under Users and Access → Integrations → App Store Connect API, generate a
   team key with the **Developer** role. Download the `AuthKey_<key ID>.p8`
   (Apple lets you download it once) and note the key ID and the issuer ID
   shown above the list.
4. **Save the secrets and turn it on:**

   ```bash
   base64 -i developer-id.p12 | gh secret set MACOS_SIGN_P12
   gh secret set MACOS_SIGN_PASSWORD   # prompts for the .p12 password
   base64 -i AuthKey_<key ID>.p8 | gh secret set MACOS_NOTARY_KEY
   gh secret set MACOS_NOTARY_KEY_ID --body <key ID>
   gh secret set MACOS_NOTARY_ISSUER_ID --body <issuer ID>
   gh variable set MACOS_NOTARIZE --body true
   ```

   Then delete the `.p12` and `.p8` files, or keep them only in a password
   manager.
5. **Check it with a prerelease.** Tag a `-rc.1`, download the macOS archive
   from the GitHub release in a browser, and unpack it. Then:

   ```bash
   codesign -dvv realm 2>&1 | grep Authority   # Developer ID Application: …
   spctl -a -vv -t install realm              # accepted, source=Notarized Developer ID
   ```

   and `./realm --version` runs without a prompt. A command-line binary
   can't carry its notarization ticket, so macOS checks with Apple online
   the first time it runs.

The certificate is valid for five years, and the API key doesn't expire.
If the membership lapses, binaries already released keep working, but new
releases fail until it is renewed or `MACOS_NOTARIZE` is turned off.

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
