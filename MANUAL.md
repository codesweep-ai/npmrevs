# The cs-npmrevs manual

## Name

`cs-npmrevs`: package, resolve and serve each revision's build of an npm package, so a team of AI coding agents can install work in progress.

## Synopsis

```
cs-npmrevs serve [--data DIR]... [--listen ADDR] [--print-port] [--upstream URL]
              [--images REGISTRY --images-scope @SCOPE...] [--strict @SCOPE]... [--cache DIR]
cs-npmrevs image build FILE.tgz [-o FILE] [--registry HOST] [--format oci|docker] [--revision SHA]
cs-npmrevs image inspect FILE.tgz|ARCHIVE|REFERENCE [--registry HOST] [--json]
cs-npmrevs extract ARCHIVE|REFERENCE [--data DIR]
cs-npmrevs fetch @SCOPE/NAME[@VERSION]... [--data DIR] [--registry HOST] [--latest N] [--no-deps]
cs-npmrevs lockfile check [FILE]
cs-npmrevs lockfile rewrite [FILE] [--to URL] [--dry-run]
cs-npmrevs completion bash|zsh|fish|powershell
cs-npmrevs manual
cs-npmrevs version

Global: [-v|--verbose] [-q|--quiet]
```

## Description

A team of AI coding agents needs to share work in progress. One agent's
unfinished build of a package is often what another installs next. That build
is not a version meant for people, and a version published to npmjs.com can
never be taken back.

cs-npmrevs makes every revision of an npm package installable without publishing
it. Each build, of a commit or of work not yet committed, becomes a prerelease
version, and npm installs it through `cs-npmrevs serve` the way it installs a
published version. Every other package passes through from npmjs.com.

A team shares its revisions through a container registry such as ghcr.io, one
version of one package per image. `image build` makes the image, and
`serve --images` installs from the registry, while `fetch` and `extract` copy
the tarballs out instead. On one machine, a **data directory**, which is any
directory of `.tgz` files, serves the same purpose with no container registry.

A version cs-npmrevs holds is a **local version**. It is written into the package's
**packument**, the document npm reads to pick a version, beside the versions the
**upstream** publishes. The upstream is npmjs.com unless you name another. A
local version takes the place of a published one with the same number, so a
build of a commit installs under the version it will be published as.

For what cs-npmrevs guarantees and how it is built, see [SPEC.md](SPEC.md).

## Commands

### serve

```
cs-npmrevs serve --data ./data
```

Serves the registry until it is stopped with Ctrl-C, `SIGINT` or `SIGTERM`. It
waits for requests in flight, then exits 0.

A package with a local version is answered with the upstream's packument and
every local version written into it. Its `latest` **dist-tag**, the name a bare
`npm install` resolves, points at the highest version that is not a
prerelease, local or published. Every other dist-tag is the upstream's. Ask for
a local build by its exact version.

A package with no local version is the upstream's answer, byte for byte. Its
tarballs are redirected to the upstream, so they never pass through cs-npmrevs.

A tarball dropped into a data directory is served within a second, with no
restart, and one removed stops being served. Two files that hold the same name
and version with different bytes stop the server at startup, and after it make
that package answer an error until one is removed.

Point npm at it for every package, not only your own scope:

```ini
registry=http://127.0.0.1:4875/
```

`--strict @scope` serves that scope from local versions only. A package of the
scope you forgot to build is then an error that names it, rather than a quiet
resolution to the published version.

`--images ghcr.io --images-scope @scope` adds the versions that registry holds
images of, for packages in that scope. The packument is built from the image
manifests and configs alone, and a tarball is downloaded only when npm asks for
it. The tarballs are kept in the cache directory.

### image build

```
cs-npmrevs image build acme-tool-1.0.0.tgz
```

Builds the image that carries one npm tarball and writes it as an archive file,
`acme-tool-1.0.0.image.tar` unless `-o` names another. It prints the image
reference the archive is named for, which is where to push it:

```
ghcr.io/acme/npm/tool:1.0.0
```

The image is `<registry>/<scope>/npm/<name>:<version>`, so a package needs a
scope. It holds one layer with the tarball at its root, and the npm facts about
it as manifest annotations and config labels. Nothing is pushed and no container
engine runs. The same tarball always gives the same image, byte for byte.

The image names the platform the package's `os` and `cpu` fields name, such as
`linux/arm64` for a package built for arm64 Linux, as
[SPEC.md](SPEC.md#93-platform-names) lists. A package that names no single
platform, such as a wrapper, gets `linux/amd64`. Nothing runs the image, so the
platform is a label. Docker still refuses an image that names an operating
system other than its own. Copy such a tarball out with `cs-npmrevs extract` or
podman instead.

`--revision SHA` records the commit the tarball was built from as
`org.opencontainers.image.revision`. Without it, the `gitHead` npm writes into
`package.json` is recorded when it names a commit.

`--format oci` writes an OCI archive, which keeps the annotations. Push it with
`podman load -i FILE` and then `podman push REFERENCE`, which keep the
annotations and recompress the layer. `--format docker` writes the archive
`docker save` writes, which `docker load` reads when the image names Linux.

### image inspect

```
cs-npmrevs image inspect ghcr.io/acme/npm/tool:1.0.0
```

Prints the name, version, integrity and shasum of what it is given: an npm
tarball, each image in an archive, or an image in a registry. It also prints the
reference each is published under. An image in a registry is read from its
manifest and config; no layer is downloaded. `--json` prints a JSON array of
objects with the fields `name`, `version`, `integrity`, `shasum` and `image`.

It exits 3 when the registry holds no such image.

### extract

```
cs-npmrevs extract ghcr.io/acme/npm/tool:1.0.0 --data ./data
```

Copies the npm tarballs an image holds into the data directory, and prints the
path of each. The image is an OCI archive, a docker archive, an OCI image layout
directory, or a reference to pull. An image that declares its npm facts has
only the tarball that matches them written. Each is written into place whole.

An image pulled with podman or docker lives in the engine's storage rather than
in a file. Write it to one first with `podman save`.

### fetch

```
cs-npmrevs fetch @acme/tool@1.0.0
cs-npmrevs fetch @acme/tool --latest 5
```

Copies one version of a package, or the versions the registry holds images of,
into the data directory. Only the tarball of the name and version asked for is
written. The packages each version depends on with an exact version in the same
scope come too, such as a wrapper's platform packages. `--no-deps` fetches only
what is named.

It exits 3 when the registry holds no image of a package or version named.

### lockfile check

```
cs-npmrevs lockfile check
```

Lists every entry of `package-lock.json`, or the file named, whose `resolved`
URL is on this machine, and exits 1 when there is one. A lockfile written
through `serve` names the server for each local version, and installs nowhere
the server is not running. Run this before committing one.

When no entry is on this machine, it prints one line. The line names the file by
its absolute path, and counts the entries that carry a `resolved` URL:

```
/home/ada/app/package-lock.json: 0 of 237 entries resolved through this machine
```

A count of 0 means the lockfile names no tarball at all, so the pass checked
nothing. A relative path is read from the working directory, which `go -C`
moves, so read the path the line prints.

### lockfile rewrite

```
cs-npmrevs lockfile rewrite --dry-run
cs-npmrevs lockfile rewrite
```

Looks every entry `lockfile check` would list up on `--to`, and points it at the
tarball that registry serves, with that tarball's integrity. A version the
registry does not hold stops the rewrite, and nothing is written. Only the
changed URLs and integrities move.

The integrity changes when your local build is not byte for byte the published
one, and the rewrite says so for each entry.

### completion

```
cs-npmrevs completion bash|zsh|fish|powershell
```

Writes a shell completion script to stdout. [INSTALL.md](INSTALL.md#shell-completion)
shows where to put it.

### manual

```
cs-npmrevs manual | less
```

Prints this manual, which is compiled into the binary.

### version

```
cs-npmrevs version
```

Prints the version, the platform and the Go version the binary was built with.

## Options

| Option | Applies to | Meaning |
|---|---|---|
| `--data DIR` | serve | A directory of `.tgz` files to serve. Repeatable. Default: see Configuration. |
| `--data DIR` | extract, fetch | The directory to write into. Default: see Configuration. |
| `--listen ADDR` | serve | The address to listen on. Default `127.0.0.1:4875`, clear of Verdaccio's 4873. Port 0 picks a free one. |
| `--print-port` | serve | Print the port on stdout, as one line, once listening. |
| `--upstream URL` | serve | The registry every other package comes from. Default `https://registry.npmjs.org`. |
| `--images REGISTRY` | serve | A container registry of per-version images, such as `ghcr.io`. Needs `--images-scope`. |
| `--images-scope @SCOPE` | serve | A scope whose packages are looked up in `--images`. Repeatable. |
| `--strict @SCOPE` | serve | A scope served from local versions only. Repeatable. |
| `--cache DIR` | serve | Where tarballs read out of images are kept. Default: see Configuration. |
| `-o FILE`, `--output FILE` | image build | The archive to write. Default: the tarball's name, `.tgz` replaced by `.image.tar`. |
| `--registry HOST` | image build, image inspect, fetch | The registry the image references name. Default `ghcr.io`. |
| `--format oci\|docker` | image build | The archive format. Default `oci`. |
| `--revision SHA` | image build | The commit the tarball was built from, recorded as `org.opencontainers.image.revision`. Default: the `gitHead` in `package.json`, when it names one. |
| `--json` | image inspect | Print a JSON array. |
| `--latest N` | fetch | Fetch only the newest N versions of a package named without a version. `0`, the default, fetches all. |
| `--no-deps` | fetch | Fetch only the packages named. |
| `--to URL` | lockfile rewrite | The registry to point the entries at. Default `https://registry.npmjs.org`. |
| `--dry-run` | lockfile rewrite | Print what would change, and write nothing. |
| `-v`, `--verbose` | all | Log every request and every step. |
| `-q`, `--quiet` | all | Log errors only. |

## Registry API

`serve` answers these routes:

| Route | Answer |
|---|---|
| `GET /-/ping` | `200` once the server accepts requests |
| `GET /-/npmrevs` | a JSON status document: the version, the server's pid, the data directories, the upstream, the images registry and its scopes, the strict scopes, and how many packages and versions it holds |
| `GET /<name>` | the packument, with the name's slash escaped (`/@acme%2ftool`) or not |
| `GET /<name>/-/<file>.tgz` | a local tarball, a `404` in a strict scope, or a `302` to the upstream's |
| `POST /-/npm/v1/security/...` | forwarded to the upstream, so `npm audit` works |
| `PUT`, `DELETE` | `405`: a package reaches cs-npmrevs as a tarball or an image, never through `npm publish` |

Every response carries `Server: cs-npmrevs/<version>`.

## Configuration

cs-npmrevs reads no configuration file. Flags win over the environment, and the
environment wins over the defaults. Each directory is the first of its column
that is set:

| Order | Data directory | Cache |
|---|---|---|
| 1 | `--data` | `--cache` |
| 2 | `$CS_NPMREVS_DATA` | `$CS_NPMREVS_CACHE` |
| 3 | `$XDG_DATA_HOME/cs-npmrevs/data` | `$XDG_CACHE_HOME/cs-npmrevs` |
| 4 | `~/.local/share/cs-npmrevs/data` | `~/.cache/cs-npmrevs` |

`serve` creates the data directory when it does not exist.

## Files

| Path | What it is |
|---|---|
| the data directory | The tarballs `serve` serves, and `extract` and `fetch` write, named as `npm pack` names them. |
| `<cache>/tarballs/` | Tarballs read out of images by `serve --images`, each checked against its image's integrity. |
| `FILE.image.tar` | An archive `image build` wrote. |
| `package-lock.json` | What `lockfile check` reads and `lockfile rewrite` edits in place. |

## Environment

| Variable | Effect |
|---|---|
| `CS_NPMREVS_DATA` | The data directory, when `--data` is not given. |
| `CS_NPMREVS_CACHE` | The cache directory, when `--cache` is not given. |
| `XDG_DATA_HOME`, `XDG_CACHE_HOME` | Where the data and cache directories go when neither variable above is set. |
| `GH_TOKEN`, `GITHUB_TOKEN` | A token sent to ghcr.io, `GH_TOKEN` first. Every other registry is asked anonymously, and no Docker or podman credential file is read. |

## Exit status

| Code | Meaning |
|---|---|
| 0 | Done. |
| 1 | The command ran and failed, or `lockfile check` found an entry. |
| 2 | The command was called wrongly: an unknown verb or flag, or a value that cannot be one. |
| 3 | Nothing to act on: the registry holds no such image, tag or package. |

## Diagnostics

Each message is printed after `cs-npmrevs: ` on stderr.

**`--images-scope names scopes to look up in --images, and --images is not set`**
Add `--images` with the registry, or drop `--images-scope`. Exit 2.

**`--images needs at least one --images-scope: only the scopes named are looked up there`**
Name the scopes whose packages the registry holds, such as
`--images-scope @acme`. Exit 2.

**`"acme" is not a scope: write it as @name`**
Scopes are written with their `@`. Exit 2.

**`--upstream "…" is not an http or https registry URL`**
Give the registry's base URL, such as `https://registry.npmjs.org`. Exit 2.

**`--images "https://ghcr.io" is a registry host, such as ghcr.io, not a URL`**
Give the host alone. Exit 2.

**`cs-npmrevs image needs a subcommand: build, inspect`**
The subcommand is missing or misspelled. Exit 2.

**`@acme/tool@1.0.0 is in two files with different bytes: … and …. Remove one.`**
Two tarballs claim one version. Keep the one you mean and delete the other. At
startup this exits 1; while serving, that package answers `500`.

**`listen tcp 127.0.0.1:4875: bind: address already in use`**
Another program has the port. Stop it, or pass another `--listen`. Exit 1.

**`… no such file, and not an image reference such as ghcr.io/owner/npm/name:1.0.0`**
The argument is neither a file nor a reference. Check the path. Exit 2.

**`… is an npm tarball already; copy it into the data directory as it is`**
`extract` reads images. A tarball needs no extracting. Exit 2.

**`… is compressed and is not an npm tarball; decompress an image archive before reading it`**
Archives are read uncompressed. Run `gunzip` on it first. Exit 2.

**`… holds no npm tarball`**
The image has no `.tgz` at the top of any layer, so it is not one cs-npmrevs
built. Exit 3.

**`…: not found`**
The registry holds no such image or tag, or will not show it without a
credential. For a private package on ghcr.io, set `GH_TOKEN`. Exit 3.

**`ghcr.io holds no image of @acme/tool`**
No version of that package was published as an image. Exit 3.

**`…: not a gzip file`, `no package.json in the tarball`, `"…" is not a semver version`**
The file is not an npm package tarball. Build it with `npm pack`. Exit 1.

**`@acme/tool@1.0.0+build.5: a container tag cannot hold that version`**
A container tag cannot carry build metadata. Exit 1.

**`"tool" has no scope, and an image repository is named after the scope`**
Only scoped packages have images. Exit 1.

**`…: it holds no tarball that matches the @acme/tool@1.0.0 it declares`**
The tarball in the image is not the one its npm facts describe, and nothing was
written. Rebuild the image. Exit 1.

**`…: it holds no tarball of @acme/tool@1.0.0`**
`fetch` found an image under that name holding some other package, and wrote
nothing. The image was tagged wrongly. Exit 1.

**`package-lock.json: 3 entries resolved through this machine; run cs-npmrevs lockfile rewrite`**
`lockfile check` found entries that install only while `serve` runs. Exit 1.

**`… the target registry has no @acme/tool@1.0.0-dev.1, so nothing was rewritten`**
A local build the target never published. Drop it from the lockfile, or
publish it first. Exit 1.

These are answered by the server rather than printed:

| Status | Error | What to do |
|---|---|---|
| `503` | `the upstream registry did not answer` | Check the network, or `--upstream`. A package with local versions is never answered with them alone. |
| `503` | `the images registry did not answer` | Check the network, and `GH_TOKEN` for a private package. |
| `404` | `… has no local version, and @scope is served from local versions only (--strict)` | Build the package into a data directory or an image, or drop `--strict`. |
| `404` | `no local tarball …, and @scope is served from local versions only (--strict)` | Build that version into a data directory or an image, or drop `--strict`. |
| `405` | `cs-npmrevs serves the read half of the registry API` | Put the tarball in a data directory, or build it into an image, instead of publishing it. |
| `500` | `… is in two files with different bytes` | Remove one of the two files. |

## Notes for agents

- **Every command is non-interactive.** None prompts or reads stdin.
- **Output to parse.** `image inspect --json` prints a JSON array.
  `GET /-/npmrevs` answers JSON. `serve --print-port` prints the port as the first
  line of stdout. `image build` prints the image reference and nothing else on
  stdout, and `extract` and `fetch` print one written path per line.
- **Readiness.** `GET /-/ping` answers `200` once `serve` accepts requests. Start
  it with `--listen 127.0.0.1:0 --print-port` to take a free port.
- **Stopping.** `serve` runs until `SIGINT` or `SIGTERM`, then exits 0.
- **Network.** `serve` reaches the upstream, and the images registry when
  `--images` is set. `fetch`, `extract` and `image inspect` of a reference reach
  the registry. `lockfile rewrite` reaches `--to`. `image build`, `lockfile
  check` and everything on files reach nothing.
- **Files written.** `extract` and `fetch` write into the data directory,
  `serve --images` into the cache, `image build` the archive, and `lockfile
  rewrite` the lockfile. Nothing else is written, and no file outside those is
  read but the ones named.
- **Exit 3 means absent.** A script can tell "not published yet" from a failure
  by it.

## Examples

Serve a build from a data directory, and install it:

```bash
# ./my-package is yours: any directory with a package.json
mkdir -p ./data
npm pack ./my-package --pack-destination ./data
cs-npmrevs serve --data ./data &
printf 'registry=http://127.0.0.1:4875/\n' > /tmp/npmrc
NPM_CONFIG_USERCONFIG=/tmp/npmrc npm install my-package@1.0.0
```

Publish a tarball as an image, and check it landed:

```bash
ref=$(cs-npmrevs image build acme-tool-1.0.0.tgz)
podman load -i acme-tool-1.0.0.image.tar && podman push "$ref"
cs-npmrevs image inspect "$ref"
```

Fetch the images of a package, then serve them:

```bash
cs-npmrevs fetch @acme/tool --latest 3 --data ./data
cs-npmrevs serve --data ./data
```

Keep a lockfile made through the server out of a commit:

```bash
cs-npmrevs lockfile check || cs-npmrevs lockfile rewrite
```

## See also

- [README.md](README.md): what cs-npmrevs is for.
- [INSTALL.md](INSTALL.md): getting the binary.
- [SPEC.md](SPEC.md): what it guarantees.
- `npm help registry`, `npm help npmrc`: how npm chooses a registry.
- `podman push`: pushing an image to a registry.
