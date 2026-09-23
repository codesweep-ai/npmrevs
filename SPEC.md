# The cs-npmrevs specification

A team of AI coding agents needs to share work in progress that is not yet
meant for people. cs-npmrevs makes every revision of an npm package installable
without publishing it. Each build of a commit, or of work not yet committed,
becomes a prerelease version that npm installs beside the published ones. The
builds come from a directory of tarballs or from container images, one version
per image, and every other package passes through from an upstream registry. An
install through it resolves the way an install from npmjs.com does, with the
revisions added.

This document states what the tool guarantees and how it is built. Requirements
use RFC 2119 keywords in bold: **MUST** is an obligation, **SHOULD** is a strong
preference, and **MAY** is a genuine option. Prose in *italics* explains a
decision and obliges nothing. Read the vocabulary first: the rest of the
document stops explaining itself after it.

## 1. Purpose

A team of AI coding agents needs to share work in progress. One agent's
unfinished build of a package is often what another installs next. That build
is not a version meant for people, and a version published to npmjs.com can
never be taken back.

cs-npmrevs makes every revision of an npm package installable without publishing
it. Go gets that for free: a module is source, and its repository is the
registry, so any commit installs. An npm package is build output, often a
wrapper and a package per platform, so a commit installs only once its build is
stored where npm can reach it. cs-npmrevs keeps each build, of a commit or of
work not yet committed, as a prerelease version.

The team shares those versions through a container registry, one version per
image, or keeps them in a directory on one machine. cs-npmrevs serves them to npm
beside the published versions, from the registry itself (§4.5), once `fetch` has
copied them out (§6), or from the directory. Only the versions meant for people
go to npmjs.com.

A registry is what lets such a build be installed the way its users will
install it. Installing a tarball by path skips the resolution that picks a
version, reads its dependencies and chooses a platform package, which is most
of what can go wrong.

### 1.1 Goals

1. An install through cs-npmrevs resolves as it would against the upstream, with
   the local versions added to what the upstream holds.
2. A local version takes the place of a published one with the same number, so
   a commit's build can be tried under the number it will be published as.
3. Nothing is precomputed. Every document is built on request from the files and
   the upstream, so nothing goes stale.
4. A build travels between agents and machines in a container registry, one
   version per image, and is read back with no container engine.
5. It runs from one static binary with no runtime to install.

### 1.2 Non-goals

1. cs-npmrevs accepts no publish. The server answers the read half of the registry
   API, and a package reaches it as a tarball in a data directory or as a
   per-version image.
2. There is no authentication. The server listens on loopback and serves
   whoever reaches it.
3. It answers plain HTTP, and leaves TLS to whatever stands in front of it.
4. It does not mirror the upstream. Upstream tarballs are redirected, never
   stored.
5. It has no search, no web interface and no user accounts.

### 1.3 Other registries

This section is informative, and obliges nothing. It places cs-npmrevs among
registries and services a reader may already know, as each stood in September
2026.

| | cs-npmrevs | Verdaccio 6.10 | pnpr 0.1 alpha | pkg.pr.new |
|---|---|---|---|---|
| A local version arrives | as a tarball in a directory, or an image (R1, R23) | by `npm publish` | by `npm publish` | from a GitHub Actions run, per commit or pull request |
| Local and upstream versions of one name | merged, and a local version replaces a published one with its number (R8, R9) | merged through an uplink, which refuses a version the uplink holds | never mixed: each name has one source | never mixed: a build installs by URL, outside any registry |
| `latest` | the highest release among both (R14) | the uplink's, when it is the same version or newer | the source's own | untouched: a URL names one build |
| Upstream tarballs | redirected, never stored (R19, R20) | stored | stored | not involved: npm fetches them from npmjs.com |
| Accounts and access control | none | yes | yes | its GitHub App |
| Runs as | one static binary | a Node.js server | one binary | a hosted service |
| Licence | Apache-2.0 | MIT | PolyForm Shield, source-available | MIT |

**pnpr** is pnpm's registry, in Rust. It also serves Cargo and Python packages,
and resolves a project's dependency graph on the server. Each package name has a
single source, so it never serves your build of a package beside npmjs.com's
versions of it. That closes dependency confusion, where a public package with
the name of a private one is installed in its place. cs-npmrevs merges the two
sources on purpose, and a strict scope (R21) is how a scope opts out.

**Verdaccio** is a registry to publish to, with an uplink that proxies
npmjs.com. It refuses a version the uplink already holds, and the uplink's
dist-tags take the place of yours. A refused publish still leaves its tarball
behind, so later installs of that version fail their integrity check. It keeps
the uplink's packument for two minutes by default, and with no uplink, a scope
sent to it loses every version npmjs.com holds.

**pkg.pr.new** is the closest in purpose: a hosted service that makes the build
of every commit and pull request installable by URL, without publishing to
npmjs.com. Builds come only from GitHub Actions, and a URL install bypasses
ranges and dist-tags. cs-npmrevs serves revisions from CI, a sandbox or one
machine, through a registry the team controls.

**Nexus and Artifactory** merge a hosted npm repository with a proxy of
npmjs.com in a group or virtual repository. Both are servers to operate, with
storage and accounts of their own.

**GitHub Packages** hosts npm packages too, but its npm registry asks for a
token to install even a public one.

### 1.4 Packages in container registries

This section is informative too. Several ecosystems keep their packages in OCI
registries already. Homebrew has served its bottles, the packages it installs
prebuilt, from ghcr.io since 2021. Helm has pushed charts to OCI registries
natively since version 3.8. The conda-forge channel is mirrored to ghcr.io, and
pixi installs from that mirror. Flux and Crossplane also use OCI registries for
their packages.

Other package managers could do the same, npm among them. An OCI registry
already provides authentication, access control, replication and blob storage
behind a CDN, run by teams whose job that is. Its storage is addressed by
digest, so a file pushed twice is stored once, and a manifest that names the
digests verifies its blobs wherever they came from. Signatures and SBOMs attach
to an artifact through the referrers API, the same way in every ecosystem.
Mirrors such as Harbor and Zot need no protocol of their own.

OCI does not bring everything a package manager needs. It knows nothing of
dependencies, version ranges or platform rules. Fetching a manifest and then a
blob costs round trips, which an install of hundreds of packages feels. So a
registry can keep its ecosystem's own metadata API and put OCI behind it, and a
client never learns what storage the registry uses. cs-npmrevs has that shape: npm
reads packuments and tarballs, and never an image. A packument is built from
image manifests and configs alone (R26), and a layer is downloaded only when npm
asks for its tarball.

cs-npmrevs writes each build as a container image, with the OCI image media types
for its manifest, config and layer. It does not write an artifact with a media
type of its own, the kind the ORAS command-line tool pushes. Registries support
those unevenly: some refuse a config media type they do not know, and some lack
the fields OCI 1.1 added for artifacts. Every registry that holds container
images accepts an image. So does every tool that copies images, such as skopeo,
crane and podman, whether into a registry, an OCI layout or an archive. Docker
takes one only when it names Docker's own operating system (R41). With no
cs-npmrevs at hand, `podman create` and `podman cp` still get the tarball out
(R41). The cost is an image that names a platform and a command it has no use
for, and that a registry lists among its container images.

## 2. Vocabulary

| Term | Meaning |
|---|---|
| **packument** | The JSON document a registry answers for a package name: every version, the dist-tags, and when each version was published. npm reads it to resolve a version. |
| **upstream** | The registry every package without a local version comes from. It is npmjs.com unless `--upstream` names another. |
| **revision** | One build of a package, from a commit or from work not yet committed, identified by the prerelease version it carries. cs-npmrevs serves each revision as a local version. |
| **local version** | A version cs-npmrevs serves from a file it holds rather than from the upstream: a tarball in a data directory, or one read out of a per-version image. |
| **data directory** | A directory of npm tarballs that `serve` reads and `extract` and `fetch` write. |
| **dist-tag** | A name such as `latest` or `dev` that a packument points at one version. |
| **integrity** | The sha512 digest of a tarball, written `sha512-` and base64, which npm checks every download against. |
| **per-version image** | A container image that holds one version of one package, and the npm facts about it. |
| **archive** | An image written to a file: an OCI archive, or a docker archive as `docker save` writes it. |
| **strict scope** | A scope served from local versions only, so a package missing from the data directories is an error rather than a quiet resolution to the published one. |
| **pass-through** | An answer that is the upstream's response, byte for byte. |

## 3. Interfaces

### 3.1 Command line

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

[`MANUAL.md`](MANUAL.md) documents every flag.

### 3.2 Registry API

| Route | Answer |
|---|---|
| `GET /-/ping` | `200` with `{}`, once the server is ready |
| `GET /-/npmrevs` | the status document of §9.2 |
| `GET /{name}`, `GET /@{scope}/{name}` | the packument |
| `GET /{name}/-/{file}`, `GET /@{scope}/{name}/-/{file}` | a tarball, a `404` in a strict scope, or a redirect to the upstream's |
| `POST /-/npm/v1/security/...` | forwarded to the upstream, for `npm audit` |
| any write | `405` |
| anything else | `404` |

A scoped name arrives with its slash escaped, `/@scope%2fname`, and unescaped.
Both are the same package. A name from before npm required lower case, such as
`JSONStream`, is routed like any other, because the upstream still serves it.

### 3.3 Images

The image of `@scope/name` at version `V` is `<registry>/scope/npm/name:V`. It
is a container image with the OCI image media types, for the reasons in §1.4.
It holds one layer, and the layer holds one file: the tarball, at the root,
named as `npm pack` names it. The npm facts are in the manifest annotations and
again in the config labels, under the keys of §9.1.

## 4. Serving

### 4.1 Local versions

**R1.** A data directory's tarballs **MUST** be indexed by the name and version
each one's `package.json` declares, never by the file name.

**R2.** The integrity and shasum a packument states for a local version **MUST**
be computed from the bytes the server serves for it. *Computed from anything
else, the two could disagree, and npm refuses a tarball whose integrity is not
the one it was promised.*

**R3.** A tarball **MUST NOT** be modified between being read and being served.

**R4.** Two files that declare the same name and version with different bytes
**MUST** stop the server at startup. After startup they **MUST** make that name
answer `500`, with an error naming both files, until one is removed. *Serving
either one would let two machines install different code under one version.*

**R5.** Two identical copies of one version **MUST** be served as one version.

**R6.** A tarball added to, rewritten in or removed from a data directory **MUST**
be served, reread or dropped within one second, without a restart.

### 4.2 The packument

**R7.** A package with no local version **MUST** be answered with the upstream's
response: its status, its body and its content type.

**R8.** A package with a local version **MUST** be answered with the upstream's
packument, with every local version written into its `versions`.

**R9.** A local version **MUST** take the place of an upstream version with the
same number. *A local build of a commit carries the version its published
counterpart will carry, and trying it under that version is the point.*

**R10.** A local version's `dist.tarball` **MUST** point at this server, at the
address the request's `Host` header names. *The client reaches the server by
that address, whatever address the server listens on.*

**R11.** The client's `Accept` header **MUST** be passed to the upstream, so the
abbreviated packument an install asks for and the full one `npm view` asks for
each keep their shape.

**R12.** A package the upstream answers `404` for **MUST** be served from its
local versions alone.

**R13.** When the upstream fails for a package with a local version, by not
answering or by answering a `5xx`, the server **MUST** answer `503`. It
**MUST NOT** answer with the local versions alone. *A partial version list
resolves a range against half the versions, and the install that follows looks
plausible and is wrong.*

**R14.** The `latest` dist-tag **MUST** point at the highest version that is not a
prerelease, among local and upstream versions alike. With no such version it
**MUST** be the upstream's `latest`, and with neither there **MUST** be none.
*This is what npmjs.com would answer had every local version been published
there, so resolution through cs-npmrevs matches resolution in production.*

**R15.** Every other upstream dist-tag **MUST** pass through unchanged, and the
server **MUST NOT** add one of its own. *npm installs the `latest` version whenever it
satisfies the range asked for, so a tag moved to a local build would make local
builds win ranges that npmjs.com would resolve otherwise. An exact version is
how a local build is asked for.*

**R16.** A full packument's `time` **MUST** record each version from a data
directory at its tarball's modification time. An abbreviated packument's
`modified` **MUST** be the later of the upstream's and the newest of those.
*An image records no time worth reporting: it carries a fixed one, so that it
builds the same everywhere.*

**R17.** A local version's object **MUST** carry its `package.json` fields as
published, with `_id`, `_hasShrinkwrap`, `hasInstallScript` and `dist` added. A
string `bin` **MUST** become a map keyed by the name without its scope.
*`os` and `cpu` are among the fields copied, and npm reads them from the
packument to choose which platform packages to install.*

### 4.3 Tarballs

**R18.** A request for a local version's tarball **MUST** be answered with its
bytes.

**R19.** A request for any other tarball outside a strict scope **MUST** be
redirected with `302` to the same path on the upstream. *npm rewrites a tarball
URL naming registry.npmjs.org to the registry it was given, so the request comes
here. The redirect sends the download straight to the upstream.*

**R20.** The server **MUST NOT** store an upstream tarball.

### 4.4 Strict scopes

**R21.** A package in a strict scope **MUST** be served from its local versions
only, and the upstream **MUST NOT** be asked about it.

**R22.** A package in a strict scope with no local version **MUST** answer `404`,
with an error naming the package and the scope. A tarball in a strict scope that
is not a local version's **MUST** answer `404` too.

### 4.5 Images as a source

**R23.** With `--images`, every tag of a covered package's image repository
**MUST** become a local version when the image declares the package's name and
the tag as its version. A package is covered when its scope is an
`--images-scope`.

**R24.** A version in a data directory **MUST** take the place of the same version
read from an image.

**R25.** A tarball read out of an image **MUST** be checked against the integrity
the image declares before it is kept or served.

**R26.** A packument **MUST** be built from image manifests and configs alone. A
layer **MUST** be downloaded only for a tarball request. *A package keeps twenty
versions, and a packument that pulled every layer would cost a download of all
of them.*

**R27.** When the images registry fails with anything but a refusal or a missing
repository, the packument **MUST** answer `503`.

### 4.6 Other routes and headers

**R28.** `GET /-/ping` **MUST** answer `200` once the server accepts requests.

**R29.** `GET /-/npmrevs` **MUST** answer the status document of §9.2.

**R30.** The audit endpoints under `/-/npm/v1/security/` **MUST** be forwarded to
the upstream.

**R31.** Every write **MUST** be refused with `405`, and an error that says a
package reaches the server as a tarball or an image, never through `npm
publish`. *With cs-npmrevs as the only registry in an npmrc, `npm publish` through
that npmrc then cannot reach npmjs.com.*

**R32.** Every response **MUST** carry a `Server` header naming cs-npmrevs and its
version, so a script can tell cs-npmrevs from another program on the port.

**R33.** Every packument and every local tarball **MUST** carry
`Cache-Control: no-cache`. *npm caches what the header allows, and a tarball
added to a data directory has to reach the next install.*

**R34.** The server **MUST** listen on loopback unless `--listen` names another
address.

## 5. Images

**R35.** An image **MUST** hold exactly one layer, and the layer exactly one
regular file: the tarball, at the root, named as `npm pack` names it.

**R36.** The image of `@scope/name` at version `V` **MUST** be
`<registry>/scope/npm/name:V`. A package without a scope has no image.

**R37.** A version a container tag cannot hold, with build metadata or longer
than 128 characters, **MUST** be refused.

**R38.** The npm facts of §9.1 **MUST** be written both as manifest annotations
and as config labels. *A registry client reads the annotations in one request.
A docker archive drops them and keeps the labels.*

**R39.** Building one tarball twice **MUST** produce the same image digest, and an
OCI archive of it **MUST** be the same bytes. *A re-run of a publish then pushes
nothing new, and a version published twice with different bytes is visible as
a different digest. podman recompresses the layer as it pushes, so a registry
can show another digest than the build's, and the npm integrity the image
carries is what names the tarball.*

**R40.** Building an image **MUST NOT** need a container engine or the network.

**R41.** The image config **MUST** name a platform and a command, so
`podman create` accepts the image and a client without cs-npmrevs can copy the
tarball out. The platform **MUST** be the one the package's `os` and `cpu`
fields name when each names exactly one value that §9.3 maps, and `linux/amd64`
otherwise. *A registry shows the platform on the package's page, so the image
of a platform package names its own. Nothing chooses an image by it: npm
chooses a platform package by its `os` and `cpu` (R17), and podman, given
another platform, warns and goes on. Docker refuses an image that names an
operating system other than its own, so the tarball of a darwin or win32
package comes out with podman or `cs-npmrevs extract` instead.*

**R42.** When `package.json` names a GitHub repository, the image **MUST** carry
`org.opencontainers.image.source` naming it. *ghcr.io links a package to the
repository that label names, and gives it that repository's visibility.* When
`--revision` or the package's `gitHead` names a commit, the image **MUST** carry
`org.opencontainers.image.revision` naming it. *A version names its commit only
by convention, and this key names it in the form every OCI tool reads.*

## 6. Extract and fetch

**R43.** `extract` and `fetch` **MUST** write only regular files at the top of a
layer whose names end in `.tgz`, and only into the data directory. *An image
cannot then write outside the directory it is extracted into.*

**R44.** When an image carries npm facts, only the tarball that matches them in
name, version and integrity **MUST** be written, and an image with no such
tarball **MUST** fail. `fetch` **MUST** write only the tarball of the name and
version it was asked for. *An image tagged as one package and holding another
would otherwise put a package under a name nobody fetched it as.*

**R45.** Each tarball **MUST** be written through a temporary file and a rename,
so a server reading the directory never indexes half of one.

**R46.** `fetch` **MUST** also fetch what each fetched version depends on through
`dependencies`, `optionalDependencies` or `peerDependencies`, when the
dependency is in the same scope and pinned to an exact version. `--no-deps`
turns this off. *A wrapper package is useless
without the platform packages it names, and those live in images of their own.*

**R47.** A dependency with no image **MUST** be left to the registry, with a
warning, rather than failing the fetch.

**R48.** `extract` **MUST** read an image from any of three kinds of file: an OCI
archive, a `docker save` archive, and an OCI image layout directory.

## 7. Lockfiles

**R49.** `lockfile check` **MUST** list every entry whose `resolved` URL is a
loopback address, and **MUST** exit `1` when there is one. When there is none,
it **MUST** print the lockfile's absolute path and how many entries carry a
`resolved` URL, so a pass shows what it checked.

**R50.** `lockfile rewrite` **MUST** look each such entry up on the target
registry, and take the target's tarball URL and integrity for it.

**R51.** A version the target registry does not hold **MUST** stop the rewrite
with nothing written.

**R52.** A rewrite **MUST** change only the `resolved` URLs and integrities it
replaces, and leave every other byte of the file as it was.

## 8. Exit status

**R53.** A command that did what it was asked **MUST** exit `0`.

**R54.** A command that ran and did not succeed **MUST** exit `1`. A `lockfile
check` that finds an entry is one.

**R55.** A command called wrongly, with an unknown verb or flag, a missing
argument or a value that cannot be one, **MUST** exit `2`.

**R56.** A command that found nothing to act on, such as an image, a tag or a
package the registry does not hold, **MUST** exit `3`. *A script tells "not
published yet" from "the push failed" by this code.*

## 9. Data model

### 9.1 The npm facts an image carries

Each key is written as a manifest annotation and as a config label.

| Key | Value |
|---|---|
| `ai.codesweep.npm.name` | the package name |
| `ai.codesweep.npm.version` | the version |
| `ai.codesweep.npm.integrity` | the tarball's integrity |
| `ai.codesweep.npm.shasum` | the tarball's sha1, in hex |
| `ai.codesweep.npm.manifest` | the version object a packument lists, as JSON, with no `dist.tarball` |
| `org.opencontainers.image.title` | the package name |
| `org.opencontainers.image.version` | the version |
| `org.opencontainers.image.description` | a sentence that says the image is a tarball and not a program |
| `org.opencontainers.image.source` | the GitHub repository, when `package.json` names one |
| `org.opencontainers.image.revision` | the commit the tarball was built from, when `--revision` or the `gitHead` in `package.json` names one |

For example, the image of `@acme/tool` 1.0.0 carries:

```json
{
  "ai.codesweep.npm.name": "@acme/tool",
  "ai.codesweep.npm.version": "1.0.0",
  "ai.codesweep.npm.integrity": "sha512-3yGWSbe…",
  "ai.codesweep.npm.shasum": "5f9b0c3a…",
  "ai.codesweep.npm.manifest": "{\"name\":\"@acme/tool\",\"version\":\"1.0.0\",\"_id\":\"@acme/tool@1.0.0\",\"dist\":{\"integrity\":\"sha512-3yGWSbe…\",…}}",
  "org.opencontainers.image.source": "https://github.com/acme/tool"
}
```

### 9.2 The status document

`GET /-/npmrevs` answers:

| Field | Meaning |
|---|---|
| `server` | always `cs-npmrevs` |
| `version` | the cs-npmrevs version |
| `pid` | the server's process, for a script that started it through a launcher |
| `data` | the data directories, as absolute paths |
| `upstream` | the upstream registry |
| `images`, `imagesScopes` | the images registry and its scopes, when `--images` is set |
| `strict` | the strict scopes |
| `packages`, `versions` | how many names and versions the data directories hold |

```json
{
  "server": "cs-npmrevs",
  "version": "v0.1.0",
  "pid": 41733,
  "data": ["/home/ada/.local/share/cs-npmrevs/data"],
  "upstream": "https://registry.npmjs.org",
  "packages": 5,
  "versions": 5
}
```

### 9.3 Platform names

npm's `os` and `cpu` fields hold Node's names for a platform, and an image
config holds OCI's. R41 maps one to the other as this table lists.

| npm | OCI |
|---|---|
| `os`: `aix`, `android`, `darwin`, `freebsd`, `linux`, `netbsd`, `openbsd` | the same name |
| `os`: `win32` | `windows` |
| `cpu`: `arm`, `arm64`, `loong64`, `mips`, `riscv64`, `s390x` | the same name |
| `cpu`: `x64` | `amd64` |
| `cpu`: `ia32` | `386` |
| `cpu`: `mipsel` | `mipsle` |
| `cpu`: `ppc64` | `ppc64le`, or `ppc64` with `os` `aix` |

Any other value maps to nothing. `sunos` is one: OCI names two systems for it,
`solaris` and `illumos`.

## 10. Configuration

Flags win over the environment, and the environment wins over the XDG defaults.
Each directory is the first of its column that is set:

| Order | Data directory | Cache |
|---|---|---|
| 1 | `--data` | `--cache` |
| 2 | `$CS_NPMREVS_DATA` | `$CS_NPMREVS_CACHE` |
| 3 | `$XDG_DATA_HOME/cs-npmrevs/data` | `$XDG_CACHE_HOME/cs-npmrevs` |
| 4 | `~/.local/share/cs-npmrevs/data` | `~/.cache/cs-npmrevs` |

**R57.** Requests to ghcr.io **MUST** carry `$GH_TOKEN`, else `$GITHUB_TOKEN`, when
one is set, and every other registry request **MUST** be anonymous. cs-npmrevs
**MUST NOT** read a Docker or podman credential file or run a credential helper.
*What cs-npmrevs sends is then decided by its own environment and nothing else.*

## 11. Implementation

### 11.1 Packages

| Package | Holds |
|---|---|
| `cmd/cs-npmrevs` | the entry point, and the root certificates compiled in for a machine with none |
| `npmrevs` (the module root) | `MANUAL.md`, embedded for `cs-npmrevs manual` |
| `internal/cli` | the command tree, the exit statuses and the logging |
| `internal/registry` | the HTTP server: routes, the merge, pass-through, redirects and dist-tags |
| `internal/datadir` | the index of the data directories |
| `internal/npmpkg` | reading a tarball into its manifest, integrity and version object |
| `internal/images` | building, archiving and reading per-version images, and the registry client |
| `internal/lockfile` | finding and rewriting loopback entries |
| `internal/paths` | the data and cache directories |
| `internal/testpkg` | tarballs built for the tests, and nothing that ships |

Dependencies point one way. `cli` imports every other package, and `registry`
imports `datadir`, `images` and `npmpkg`. `npmpkg` imports nothing of this
module.

### 11.2 Key types

- `npmpkg.Package` is one tarball: its manifest, integrity, shasum, file count
  and path. `Entry` turns it into the version object a packument lists.
- `datadir.Index` maps a name to its versions across the data directories, and
  rereads what changed at most every 250 milliseconds.
- `images.Source` is the images registry as a server reads it: a tag list per
  package, trusted for a minute, and one manifest per version, trusted for good.
- `registry.Server` is the handler, holding the index, the source, the upstream
  and a one-minute cache of the upstream packuments it merged.

### 11.3 Lifecycle and concurrency

The server answers each request on its own goroutine. The index, the image
source and the upstream cache each hold one mutex around their maps, and none
is held across a network call. `serve` stops on `SIGINT` or `SIGTERM`, and
waits up to ten seconds for requests in flight.

### 11.4 Testing

Every test runs without the network. An upstream is an `httptest` server, and an
images registry is go-containerregistry's registry served in-process. One test
drives the real npm client through the server, against an upstream that names
registry.npmjs.org in its tarball URLs as npmjs.com does. It skips where npm is
not installed, and CI installs it. `make test` measures coverage, and `make
coverage-check` fails under the floor the Makefile states.

## 12. Conformance

An implementation conforms to this specification when it satisfies R1 through
R57.

## 13. Quality attributes

```
[QA-01] Resolution fidelity: npm installs through cs-npmrevs what the packument promises
  Measured by: the test that drives npm through the server
  Classification: BEHAVIORAL

[QA-02] Reproducible images: one tarball, one image digest, on every machine
  Measured by: the tests that build twice and compare
  Classification: BEHAVIORAL

[QA-03] Freshness: a tarball dropped in a data directory is served within a second
  Measured by: the index tests that change a directory under a running index
  Classification: BEHAVIORAL

[QA-04] Isolation: no test reaches the network
  Measured by: every upstream and registry in the suite listening on loopback
  Classification: STRUCTURAL

[QA-05] Deployment: one static binary, no runtime, no container engine
  Measured by: CGO_ENABLED=0 in the release manifest
  Classification: STRUCTURAL
```

## 14. Hard limits

**Plain HTTP on loopback.** The server has no TLS and no authentication. Put it
behind something that does before letting another machine reach it.

**No latest for prereleases only.** A package whose versions are all
prereleases, with no upstream `latest`, has no `latest`. A bare `npm install`
of it fails, and an exact version installs it.

**Sizes.** A `package.json` is read up to 8 MiB. An image is unpacked up to 1000
tarballs, 512 MiB each and 4 GiB in all, one file at a time.

**Lockfile addresses.** npm records the tarball URL the packument named. A local
version's entry therefore names the server, and only `lockfile rewrite` makes
that lockfile install elsewhere.

## 15. Open questions

1. **Does `podman save --format oci-archive` keep manifest annotations?** When it
   does not, `extract` still verifies against the labels.
2. **Should `serve` keep upstream tarballs for work offline?** It redirects
   them, so a machine that loses the network loses every package it has no
   local version of.
