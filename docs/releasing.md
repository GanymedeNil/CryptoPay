# Versions and releases

CryptoPay uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html) and keeps a [changelog](../CHANGELOG.md) in the [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format. The project is **pre-1.0**: a minor release (`0.x.0`) may contain breaking changes, and they are listed under **Changed** or **Removed** in the changelog.

## Where the version lives

| Place | What it holds |
|---|---|
| [`VERSION`](../VERSION) | The one source of truth, for example `0.1.0` (no leading `v`; a pre-release looks like `0.2.0-rc.1`) |
| `web/package.json` | The same number, kept in step by hand |
| [`CHANGELOG.md`](../CHANGELOG.md) | A `## [0.1.0] - 2026-11-18` section per release and an `## [Unreleased]` section on top |
| the git tag | `v0.1.0` on the commit that was released |

`make check-version` (also run by CI, and by the release workflow against the tag) fails when they disagree. `make version` prints the current version.

The gateway reports its version: `gateway version` prints `cryptopay gateway 0.1.0 (commit e0945b3fdaa7, built 2026-11-18T04:12:30Z, go1.27.3)`, and the same values appear in the startup log. `make up` and `make build` stamp them; a plain `docker compose build` produces a `dev` build.

## Day to day

Every pull request that changes something a user or operator would notice adds a line under **Unreleased** in `CHANGELOG.md` (the pull request template reminds you). Use the headings **Added**, **Changed**, **Deprecated**, **Removed**, **Fixed** and **Security**.

## Cutting a release

1. **Prepare it in a pull request** (nothing is pushed to `main` directly):
   - set `VERSION` and the `version` in `web/package.json` to the new number;
   - in `CHANGELOG.md` move the **Unreleased** entries into a new `## [X.Y.Z] - YYYY-MM-DD` section, leave an empty `## [Unreleased]` above it and update the two comparison links at the bottom;
   - run `make check-version` and `make test`.

   Merge the pull request when `CI` is green.
2. **Tag the merge commit** and push the tag:

   ```bash
   git switch main && git pull
   git tag -a vX.Y.Z -m "CryptoPay X.Y.Z"
   git push origin vX.Y.Z
   ```
3. **The Release workflow does the rest** ([`release.yml`](../.github/workflows/release.yml)):
   1. checks that the tag is `v$VERSION`, that the changelog has the section and that the commit is on `main`;
   2. runs the whole CI suite again on the tagged commit;
   3. builds the runtime images and pushes them to GitHub Container Registry;
   4. creates the GitHub release, with the changelog section as its notes.

   The release is created last, so a failed run never leaves a release without images. Fix the cause and re-run the failed jobs.

A hotfix follows the same steps from `main`. A pre-release (`0.2.0-rc.1`) is published as a GitHub pre-release, and its images get no `latest` or `0.2` tag.

## Docker images

| Image | Tags |
|---|---|
| `ghcr.io/ganymedenil/cryptopay-backend` | `0.1.0`, `0.1`, `latest` |
| `ghcr.io/ganymedenil/cryptopay-web` | `0.1.0`, `0.1`, `latest` |

The images are built for `linux/amd64`. The first time a package is published, GitHub creates it as **private**; make it public under the package's settings on GitHub if everyone should be able to pull it. The backend migrates the database when it starts, so read the changelog before upgrading and back up the database first.
