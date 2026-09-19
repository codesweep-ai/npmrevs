# Contributing to cs-npmrevs

Bug reports and pull requests are welcome. These rules apply to humans and
coding agents alike. If you are an agent working in this repository, read this
file before you change anything and follow it.

For a security issue, use GitHub's private vulnerability reporting on this
repository's Security tab, rather than opening a public issue.

## Submitting a change

File a bug or an idea as a GitHub issue on this repository. For a fix that
stands on its own, a pull request on its own is enough. For anything that
changes what the registry answers, or the images it builds, open an issue
first, so the design gets settled before you write it.

1. Fork the repository, and create a branch off `main`.
2. Make the change, with its test.
3. Run `make ci`, which is every gate CI runs.
4. Open a pull request against `main`, and say what the change does and why.

Expect comments rather than silence, and expect a small change to move
quickly. A reviewer asks whether the change keeps the design rules below,
whether a test fails without it, and where a reader would find it documented.

By opening a pull request you agree that your contribution ships under the
[Apache 2.0 licence](LICENSE) this project is released under.

## Before you push

One command:

```bash
make ci
```

That is every gate the CI workflow has, on this machine and in the order the
workflow takes them, so a green run here is a green run there. `make check` is
the faster subset to keep beside you while you work, and `make ci` is the one
that has to pass.

No linter needs installing. The ones it shells out to are pinned Go tools,
built from the module cache the first time you run them: `golangci-lint`,
`deadcode`, `actionlint`, `cs-lint` and `cs-ledger`. `make repin` moves the
`cs-` pins to the branch tip, and `make versions` prints the version of each.

Two programs are still expected on the PATH. `goreleaser` validates the release
manifest, and `make build` falls back to `go build` where it is absent. `npm`
drives the test that installs through the server, which skips where npm is not
installed.

This repository keeps a **ledger** of open issues in `ledger/`. Read
[`ledger/AGENTS.md`](ledger/AGENTS.md) before you start work, and follow it as
you go. A commit that touches `ledger/` needs `cs-ledger render && cs-ledger
check` to pass first, and `make ledger` runs the check half.

## Design rules

Your change has to keep these. Each one names the test or the review that holds
it.

**Resolution matches npmjs.com.** A packument is what npmjs.com would answer
had every local version been published there. No tag is moved to a local build.
The registry tests hold the dist-tags, and the npm test holds the install.

**Nothing is precomputed.** Every packument is built on request from the files
and the upstream. A cache has a lifetime a reviewer can read beside it.

**Integrity comes from the bytes served.** A tarball is hashed as it is read,
and never modified. The npm test fails if the two disagree.

**Fail loudly, never partially.** An upstream that does not answer is a `503`,
never a version list with half the versions missing. The registry tests assert
it.

**No test reaches the network.** Every upstream and registry a test uses
listens on loopback. A review refuses a test that needs the internet.

## Tests

Ship a change with a test that fails without it. Write the test, watch it fail,
then change the code. A test that passes before the change tested nothing.

Tests sit beside the code as `*_test.go`. `internal/testpkg` builds the npm
tarballs they need, and `httptest` stands in for an upstream. For images, the
tests run go-containerregistry's registry in-process.

Test the contract, not the implementation: the document the registry answers,
the exit status, and the text a reader acts on. Say why a case matters in a
comment when it is not obvious.

Never lower the coverage floor to make a run green. Raise it when a tier lands.
[`SPEC.md`](SPEC.md#114-testing) holds how the suite is organised.

## Commits

**Keep it short.** One idea per commit, and a message a reader takes in at a
glance. If a change will not fit one idea, split it.

**Subject**, always. Under 60 characters, imperative, no trailing period,
completing *"If applied, this commit will …"*. Say what the change does in
plain English. The test: would this subject make sense to someone who has not
read the diff and does not know this codebase? Use no category label:
`fix(serve):`, `bugfix:` and `[docs]` each name a class of change rather than
the change itself, which the diff already shows. The gate fails on one, so
amend before you push.

**Body**, rarely. Most commits need none. Add one only when the subject leaves
a question a reader would otherwise have to open the diff to answer, and then
answer that question. A sentence or two does it. Wrap it at 72 columns.

Leave out how the work was scheduled, how you tested it, and what led you to
it, and stop once the question is answered. The reason a rule exists belongs
beside the rule in [`SPEC.md`](SPEC.md), and the investigation that found it
belongs in the pull request.

```
Redirect upstream tarballs instead of proxying them
```

```
Answer 503 when the upstream fails for a local package

A packument with half the versions resolves a range to something
plausible and wrong, and nothing downstream notices.
```

Keep the `Co-Authored-By:` trailer when an agent wrote the change. Drop any
trailer linking to the agent's session or transcript. Such a link is private to
whoever ran it and dead to everyone else, and it is the one part of a commit
message that cannot be fixed after publication.

## Docs

A user-visible change lands in exactly one document. Every fact lives in one
place, and the others link to it.

| The change | Where it goes |
|---|---|
| What the registry answers, or what an image holds | [`SPEC.md`](SPEC.md) as a numbered requirement |
| A new flag or subcommand | [`MANUAL.md`](MANUAL.md) |
| A new error a user can hit | [`MANUAL.md`](MANUAL.md), under Diagnostics |
| A new prerequisite | [`INSTALL.md`](INSTALL.md) |
| A change to what the project is for | [`README.md`](README.md) |
| A convention a contributor has to follow | this file |

## Publishing to npm

Every release also goes to npm as five packages: four carry the binary, one per
platform goreleaser builds, and the wrapper picks the right one at run time.
Only the wrapper is written by hand, under `npm/npmrevs/`. The other four are
generated from goreleaser's output, and nothing under `npm/dist/` is committed.

```bash
make npm-snapshot   # build every target, package it, and show what would publish
make npm-build      # package whatever dist/ already holds
make npm-local      # serve a dev build from this machine, and print how to install it
make npm-publish    # platform packages first, then the wrapper
```

Keep that order in `npm/publish.sh`. The wrapper depends on packages that must
already exist when it is published. Publish it first, and every install between
the two commands resolves a binary the registry does not have.

Running `npm/publish.sh` again is safe. It skips each package the registry
already has from this commit, and stops on one it has from another commit.

`make npm-local` is how to try a package before publishing it. It packs the
five packages into `npm/.local-registry/data/`, serves them with the cs-npmrevs it
just built, and prints the install command. Run it after every change: a
rebuild of the same commit replaces the last run's tarballs.
`npm/local-registry.sh stop` stops the server.

These variables belong to the packaging rather than to the tool, which is why
[`MANUAL.md`](MANUAL.md) does not carry them:

| Variable | Effect |
|---|---|
| `CS_NPMREVS_BINARY` | The binary the npm wrapper runs, so the packaging can be tried against a local build. |
| `CS_NPMREVS_NPM_VERSION` | The version the generated packages carry. A tagged release supplies its own. |
| `CS_NPMREVS_NPM_TAG` | The channel a prerelease is published to, `next` unless it says otherwise. |
| `CS_NPMREVS_REGISTRY_PORT` | The port `make npm-local` serves on, 4873 unless it says otherwise. |
| `NPMREVS` | The command `npm/local-registry.sh` and `npm/publish-images.sh` run as cs-npmrevs. `make` passes the binary it built, and by hand they run `cs-npmrevs` from the PATH. |
| `REGISTRY` | The registry `npm/publish-images.sh` publishes to, `ghcr.io` unless it says otherwise. |

### Dev builds

The `npm` workflow publishes every commit on main that passes `ci` to the `dev`
channel, cutting no tag and making no release. It runs when `ci` finishes, and
skips a commit that is no longer main's head by then. It stores no credential:
each package names the workflow as a trusted publisher. A trusted publisher can
only be added to a package that exists, so the first publish runs
`npm/publish.sh` from a machine logged in to npm.

Such a build takes its version from the binary, which is the commit's timestamp
and its hash:

```bash
npm install --save-dev @codesweep-ai/npmrevs@dev
```

### Images of the packages

The `publish images` workflow pushes each commit on main that passes `ci` as
five images, one per package, such as `ghcr.io/codesweep-ai/npm/npmrevs:<version>`,
so nobody publishes them by hand. `npm/publish-images.sh` builds each with
`cs-npmrevs image build` and pushes it with podman, skipping a version already
published with the same bytes. The `prune-images` workflow keeps the newest 20
versions of each package.

`make images-snapshot` builds the images of whatever `npm/dist/` holds and pushes
nothing. The script is shared with lint, ledger and ui, so change all four
together.

## Writing

Six principles do most of the work. Read them before you write a document,
and apply them when you edit one:

1. **Introduce a term where you first use it**, in the same sentence, or link
   to the page that defines it. A reader should never meet a word the docs have
   not explained.
2. **State the point first, then qualify it.** Opening with the qualifier makes
   the reader decode the sentence backwards.
3. **Give every sentence a subject and a verb.** Say what the thing is.
4. **A how-to is steps that work.** Put the reasons somewhere else. A reader
   working through one wants commands that run.
5. **Describe what the software does, not how it came to do it.** Leave out
   what the project used to do, what was tried and dropped, and numbers from a
   run somebody did once.
6. **Do not explain a design by contrast with a worse one.** Say what it is and
   what you get, rather than asking the reader to picture a design nobody
   proposed.

The mechanical rules are enforced rather than restated here. `cs-lint prose`
carries them, `make check` runs it over this repository, and `--explain` prints
what each one wants and the guidance behind it:

```bash
go tool cs-lint prose --explain
```

That listing is the authority. Where this section and the linter disagree,
the linter is right. Every knob lives in [`.cs-lint.yaml`](.cs-lint.yaml).

## AI-assisted contributions

An agent wrote most of this repository, and you are welcome to use one. The
standard is the same either way: you are responsible for what you submit.

Point your tool at [`AGENTS.md`](AGENTS.md), which routes it to the documents
that hold the conventions, and check three things before you open the pull
request:

- You understand every line, and can answer a question about it without going
  back to the tool.
- You ran `make ci` and it passed.
- You cut what the tool added to fill space. A model pads a commit body to the
  shape it was shown, and comments that restate the code around them. Both read
  as noise to a maintainer, and both are yours to remove.

Keep the `Co-Authored-By:` trailer, which is how the work is disclosed. An
unattended agent must not open pull requests or comment on this repository.
