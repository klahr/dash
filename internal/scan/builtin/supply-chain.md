---
name: supply-chain
title: CI/CD and supply chain
kind: supply_chain
scope: since_last_scan
# Suggested: Thursdays at 06:00, looking at what changed since the last run
# (skipped when nothing did). Built-in schedules are off until turned on.
schedule: 0 6 * * 4
# Only repositories with CI configuration, container files or package manifests.
requires_files: .github/workflows/*.{yml,yaml}, action.{yml,yaml}, .gitlab-ci.yml, .circleci/config.yml, azure-pipelines.yml, bitbucket-pipelines.yml, Jenkinsfile, .buildkite/*.{yml,yaml}, Dockerfile, *.Dockerfile, Dockerfile.*, Containerfile, docker-compose*.{yml,yaml}, compose*.{yml,yaml}, package.json, go.mod, requirements*.txt, pyproject.toml, Pipfile, setup.py, Cargo.toml, Gemfile, composer.json, pom.xml, build.gradle, build.gradle.kts, .npmrc, .yarnrc.yml, pip.conf
---
Review how the project in {{path}} is built, shipped and pulled in: its CI
configuration, its container files and its package manifests. Report ways
an attacker could get code into the build or the release, or read its
secrets.

Look at the CI configuration (.github/workflows, action.yml of local
actions, .gitlab-ci.yml, .circleci, Jenkinsfile, azure-pipelines.yml,
bitbucket-pipelines.yml, .buildkite), Dockerfiles and Containerfiles with
the compose files and .dockerignore next to them, and the package manifests,
lock files and registry config (.npmrc, .yarnrc.yml, pip.conf, …). When only
some files changed, start from those, and read the files they work with (the
workflow a changed script runs in, the Dockerfile that copies a changed
manifest). If none of the changed files is one of these and none affects
them, answer with no findings.

Known vulnerabilities in dependencies, outdated or abandoned packages, and
licences are not part of this scan: the dependency audit (`dependencies`)
covers them. Secrets written into files or git history, and application
code that logs them, are the secrets scan's (`secrets`): here, only report
how the CI and image builds handle secrets.

## CI: GitHub Actions and other pipelines

- Third-party actions pinned to a tag or branch (`uses: owner/action@v4`,
  `@main`) instead of a full 40-character commit SHA. A tag can be moved to
  other code. Actions from the same repository (`./.github/actions/x`) and
  GitHub's own `actions/*` and `github/*` are lower risk: report them only
  as severity "low", and only together, as one finding per workflow file.
  Docker actions (`docker://image:tag`) without a digest count too.
- `pull_request_target` or `workflow_run` workflows that check out the pull
  request's code (`ref: ${{ github.event.pull_request.head.sha }}`,
  `head.ref`, `refs/pull/…/merge`, or the artifacts of the triggering run)
  and then run it: a build, tests, `npm install` (install scripts), a
  Makefile, a script from the checkout. These run with the base
  repository's secrets and a write token. A workflow that only labels,
  comments or reads metadata without running the checked-out code is fine.
- Script injection: `${{ … }}` expressions with text an attacker controls
  inside `run:` (or `script:` of actions/github-script): `github.event.*`
  titles, bodies, comments, branch names (`head_ref`,
  `github.event.pull_request.head.ref`), commit messages, review and issue
  text, and `inputs.*` of workflows others can trigger. The fix moves the
  value to `env:` and uses `"$VAR"` in the script. Ids, numbers, SHAs and
  `github.repository` are safe.
- Token permissions: no `permissions:` key in the workflow or job, so
  `GITHUB_TOKEN` gets the repository's default (often write to everything),
  or `permissions: write-all`, or write scopes a job never uses. Name the
  scopes the jobs need, judging from the steps (checkout: `contents: read`;
  publishing a release: `contents: write`; commenting: `pull-requests:
  write`; OIDC: `id-token: write`).
- Secrets exposed: secrets given to jobs that run on forks' pull requests
  (`pull_request_target`, `secrets: inherit` into such jobs), secrets
  echoed or written to logs, files that get uploaded as artifacts or caches,
  or `set -x` around them; `ACTIONS_ALLOW_UNSECURE_COMMANDS`.
- Self-hosted runners (`runs-on: self-hosted` or custom labels) on a public
  repository in jobs that pull requests from forks can start. Only report it
  when the repository looks public (a public remote, an OSS licence, a
  README for outside contributors) and say how you judged that.
- The same problems in other CI systems: unpinned orbs, images and
  includes, untrusted merge-request variables in scripts, protected
  variables used in jobs that run for forks.

## Containers

- No `USER` (or `USER root`) in the final stage of an image that runs a
  service. Leave out build-only stages, images whose entrypoint must be
  root and says so, and dev containers.
- Base images with `:latest` or no tag, or a tag only (`node:20`) where the
  project pins other things by digest; the fix pins `image:tag@sha256:…`.
  Report a floating tag on its own as severity "low".
- Secrets in build arguments (`ARG`/`--build-arg` named like tokens,
  passwords, keys), in `ENV`, or copied into a layer and deleted later
  (still in the image). The fix uses `RUN --mount=type=secret`.
- `curl … | sh`, `wget -O- … | bash` and similar: remote scripts run
  without a checksum or signature check. Same in CI steps.
- `ADD https://…` (no checksum; use `ADD --checksum=sha256:…` or `curl`
  plus a check) and `ADD` of archives from outside the repository.
- No `.dockerignore` next to a Dockerfile whose build context is the
  repository and which uses `COPY . …`: `.git`, `.env` files and local
  credentials end up in the image. Only report it when such files could be
  in the context.

## Packages

- A manifest with no lock file committed (package.json without
  package-lock.json, yarn.lock or pnpm-lock.yaml; pyproject.toml or Pipfile
  of an application without its lock; Cargo.toml of a binary without
  Cargo.lock; Gemfile without Gemfile.lock; go.mod without go.sum when it
  has dependencies), or a lock file in .gitignore. Libraries that by
  convention don't commit one (a Rust or Ruby library) are not findings.
  CI that installs without the lock (`npm install` instead of `npm ci`,
  `pip install` of unpinned requirements, `--no-frozen-lockfile`) is.
- Dependencies from git URLs, tarball URLs or branches without a fixed
  commit (`github:owner/repo`, `git+https://…#main`, `replace` to a fork
  without a version), and unpinned ranges where the ecosystem expects a lock
  and there is none (`*`, `latest`, `>=` with no upper bound in
  requirements.txt).
- Install scripts: `preinstall`, `install` and `postinstall` in the
  project's own package.json that download or run remote code, and
  dependencies in the lock file marked `hasInstallScript` that are new in
  the changes and not well known. Don't report widely used packages that
  build native code (esbuild, sharp, bcrypt, …).
- Likely typosquats: a dependency whose name is one or two characters from
  a popular package, or the popular name with a different scope or
  separator (`reqeusts`, `python-dateutil` vs `dateutil`, `@type/node`), and
  that is not itself well known. Say which package it resembles and how
  you judged it. When in doubt, leave it out.
- Registries or indexes over plain HTTP (`registry=http://…`,
  `--index-url http://…`, `GOPROXY` with http, `GOSUMDB=off`, or
  `GONOSUMDB`/`GOINSECURE` covering public modules, `strict-ssl=false`), and private package names that could be taken on the
  public registry (dependency confusion: an unscoped name with an extra
  index URL instead of a scoped registry).

## Rules

Only report what you can see in the files: quote the line. Leave out test
fixtures, examples and docs that show a bad pattern on purpose, vendored
and generated files, and anything under node_modules or vendor.

Severity: use the shared severity scale. For this scan:

- "critical": an outsider can run code with the repository's secrets or
  write token today (a `pull_request_target` workflow that runs the pull
  request's code, script injection from a pull request or issue title).
- "high": secrets exposed to forks or logs, a write-all token in a workflow
  that runs untrusted input, a self-hosted runner open to forks, `curl | sh`
  in a release build, a likely typosquat, a registry over HTTP.
- "medium": third-party actions on tags, missing `permissions:`, secrets in
  image layers or build args, no lock file, dependencies from git branches,
  root in a service image.
- "low": first-party actions on tags, floating image tags, a missing
  .dockerignore, install scripts that look harmless.

Use type "vuln" for findings an attacker can use and "issue" for hardening
(pinning, lock files, permissions not yet abused). Each finding names the
file and line, and its fix is the change to make, ready to paste: the
`uses: owner/action@<sha> # v4.1.1` line (give the SHA only if you know it
for that tag; else say which tag to resolve and how: `git ls-remote
https://github.com/owner/action refs/tags/v4.1.1`), the `permissions:`
block, the `env:` plus `"$VAR"` rewrite, the `USER` line, the pinned image,
the lock file command. Fingerprint
"<check>:<file>:<subject>", for example
"unpinned-action:.github/workflows/ci.yml:actions/setup-go",
"script-injection:.github/workflows/triage.yml:issue.title",
"root-user:Dockerfile", "no-lockfile:web/package.json",
"typosquat:package.json:reqeusts".

Do not change any files.
