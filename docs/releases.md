# Releases

Every `v*` tag pushed to GitHub builds Dash for Linux and macOS (amd64 and arm64) and attaches
the archives to a [GitHub release](https://github.com/klahr/dash/releases), together with a
`checksums.txt`. Each archive holds `dash`, `dash-tui` and `dash-mcp`, plus the license, the
README, `.env.example` and `deploy/dash.service`. The builds are static (CGO off), so they run on
a machine without Go.

## Cut a release

From an up-to-date `main` whose CI is green:

```sh
git tag -a v1.2.0 -m "v1.2.0"
git push origin v1.2.0
```

The [Release workflow](../.github/workflows/release.yml) runs the tests, then GoReleaser
([`.goreleaser.yaml`](../.goreleaser.yaml)) builds and publishes the release, with a changelog
from the commits since the previous tag. A tag with a suffix, such as `v1.2.0-rc.1`, is marked
as a pre-release.

To try the build locally without publishing anything (output in `dist/`):

```sh
goreleaser check
goreleaser release --snapshot --clean
```

## Versions

The version is stamped into the binaries at link time:

```sh
dash version        # or dash -version; dash-tui -version, dash-mcp -version
```

A release prints its tag, e.g. `dash v1.2.0`; a build from source without the linker flag prints
`dash dev`. Dash also logs the version at startup and returns it as `release` from
`/api/version`.
