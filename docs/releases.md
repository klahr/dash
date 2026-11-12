# Releases

Every `X.Y.Z` tag (e.g. `1.2.0`, no `v`) pushed to GitHub builds Dash for Linux and macOS (amd64 and arm64) and attaches
the archives to a [GitHub release](https://github.com/klahr/dash/releases), together with a
`checksums.txt`. Each archive holds `dash`, `dash-tui` and `dash-mcp`, plus the license, the
README, `.env.example` and `deploy/dash.service`. The builds are static (CGO off), so they run on
a machine without Go.

The same tag publishes the hub's container image to `ghcr.io/<owner>/dash` (`<owner>` being the
GitHub account or organization the repository belongs to), for linux/amd64 and linux/arm64,
tagged with the version (`1.2.0`) and `latest` (not for pre-releases). It is built
from the release binary with [`deploy/Dockerfile.release`](../deploy/Dockerfile.release), so it
reports the same `dash version`; see [Run the hub with Docker](deploy.md#run-the-hub-with-docker).
The archives also carry the compose setup (`deploy/docker-compose*.yml`,
`deploy/docker.env.example`, `deploy/caddy/`).

## Cut a release

From an up-to-date `main` whose CI is green:

```sh
git tag -a 1.2.0 -m "1.2.0"
git push origin 1.2.0
```

Or from the dashboard: in the Dash project's **History**, click the commit and choose **Tag
release…** ([Tag a release](github.md#tag-a-release)). The workflow runs for tags matching
`[0-9]+.[0-9]+.[0-9]+*`, which is the project's default tag format `X.Y.Z`.

The [Release workflow](../.github/workflows/release.yml) runs the tests, then GoReleaser
([`.goreleaser.yaml`](../.goreleaser.yaml)) builds and publishes the release, with a changelog
from the commits since the previous tag. A tag with a suffix, such as `1.2.0-rc.1`, is marked
as a pre-release.

To try the build locally without publishing anything (output in `dist/`; the images need
`docker buildx`):

```sh
goreleaser check
goreleaser release --snapshot --clean
```

## Versions

The version is stamped into the binaries at link time:

```sh
dash version        # or dash -version; dash-tui -version, dash-mcp -version
```

A release prints its tag, e.g. `dash 1.2.0`; a build from source without the linker flag prints
`dash dev`. Dash also logs the version at startup and returns it as `release` from
`/api/version`.

## Release notes

Changes that need something from you, or that you will notice, when you upgrade. The full list
of changes is in each GitHub release's changelog.

### Next release

- **Runner clones move to `<projects>/<org>/<owner>/`.** A runner's clones of the hub's projects
  now go in a folder for the remote's owner (the GitHub user or organization) inside the
  organization's folder, `<-projects>/<org folder>/<owner>/<repo>`. Clones the runner made
  itself move there on their own, the same way as the move into the organization's folder below.
- **Runner clones move to `<projects>/<org>/`.** A runner now keeps its clones of the hub's
  projects in its organization's folder, `<-projects>/<org folder>/<repo>`, instead of straight in
  `<-projects>/`. Clones the runner made itself move there on their own, on the runner's first
  start after the upgrade or at the hub's next link of the project, worktrees and task branches
  and all (the log says `runner: moved <old> → <new>`). A project with tasks running or merging,
  or a preview or try session open, at that time moves later, once it is idle. Checkouts the
  runner adopted, and old clones under `<-projects>/hub/`, stay where they are. See
  [Where a runner keeps a shared project](several-computers.md#where-a-runner-keeps-a-shared-project).
