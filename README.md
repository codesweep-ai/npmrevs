# npmrevs

> **Package, resolve and serve each revision's build of an npm package, so a team of AI coding agents can install work in progress.**

[![CI](https://github.com/codesweep-ai/npmrevs/actions/workflows/ci.yml/badge.svg)](https://github.com/codesweep-ai/npmrevs/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
![Registry](https://img.shields.io/badge/npm-registry-informational)
![Platforms](https://img.shields.io/badge/platform-Linux%20%C2%B7%20macOS-lightgrey)

A team of AI coding agents needs to share work in progress. One agent's
unfinished build of a package is often what another installs next. That build
is not a version meant for people, and a version published to npmjs.com can
never be taken back.

npmrevs makes every revision of an npm package installable without publishing
it, the way Go makes every commit of a module installable. Each build, of a
commit or of work not yet committed, becomes a prerelease version. The team
shares those versions through a container registry such as ghcr.io, or keeps
them in a directory on one machine.

`cs-npmrevs serve` puts those revisions beside the versions on npmjs.com, so npm
installs a revision as it installs a release, and everything else still comes
from npmjs.com. A revision takes the place of a published version with the same
number, so a commit's build installs under the version it will be published as.

```
                     npm install @acme/tool@1.1.0-dev.3
                                  │
                                  ▼
       data directory ──► cs-npmrevs serve ◄── ghcr.io/acme/npm/tool:<version>
       (your .tgz files)          │               (--images)
                                  │ every other package, and the rest
                                  ▼ of each one's versions
                            registry.npmjs.org
```

## Quickstart

```bash
go install github.com/codesweep-ai/npmrevs/cmd/cs-npmrevs@latest
# or, in a project with a package.json:
#   npm install --save-dev @codesweep-ai/npmrevs

# ./my-package is yours: any directory with a package.json
mkdir -p ~/npmrevs-data
npm pack ./my-package --pack-destination ~/npmrevs-data
cs-npmrevs serve --data ~/npmrevs-data &
printf 'registry=http://127.0.0.1:4873/\n' > ~/npmrevs.npmrc
NPM_CONFIG_USERCONFIG=~/npmrevs.npmrc npm install my-package@1.0.0
```

[INSTALL.md](INSTALL.md) has every other way to get the binary.

## Your build, under its own version

A package with no local version is npmjs.com's answer, byte for byte, and its
tarballs download straight from npmjs.com. A package with one gets a packument
built on request: the published versions, with each local version written in.

The `latest` tag points at the highest version that is not a prerelease, local
or published, which is what npmjs.com would answer had yours been published
there. Every other tag is npmjs.com's. Ask for a dev build by its exact version.

`--strict @acme` serves that scope from local versions only, so a package you
forgot to build is an error that names it.

## Share revisions through a container registry

A container registry such as ghcr.io can hold every revision of a package, one
image per version, where the whole team reaches it and npmjs.com never sees it.

1. Pack the build, and turn its tarball into an image. `cs-npmrevs image build`
   needs nothing else, and prints the name to push the image under:

   ```bash
   npm pack ./tool                                         # acme-tool-1.1.0-dev.3.tgz
   ref=$(cs-npmrevs image build acme-tool-1.1.0-dev.3.tgz)   # ghcr.io/acme/npm/tool:1.1.0-dev.3
   ```

2. Push it with podman, logged in to the registry. A CI build usually does this
   for every commit.

   ```bash
   podman load -i acme-tool-1.1.0-dev.3.image.tar && podman push "$ref"
   ```

3. Install it from the registry. `serve --images` finds every version pushed for
   the scope, and npm installs one by its exact version:

   ```bash
   cs-npmrevs serve --images ghcr.io --images-scope @acme &
   NPM_CONFIG_USERCONFIG=~/npmrevs.npmrc npm install @acme/tool@1.1.0-dev.3
   ```

`cs-npmrevs fetch @acme/tool --latest 5` copies revisions into a directory
instead, with the platform packages a wrapper needs. Each revision is a plain
container image, so any registry that holds images holds it.

## Beside other registries

There are other npm registries besides npmjs.com.
[pnpr](https://pnpm.io/pnpr/) comes from the pnpm team and hosts the packages
you publish to it. [Verdaccio](https://verdaccio.org) runs a registry on your
own machine, and can pass the packages it does not have through to npmjs.com.

Neither serves your unpublished builds of a package beside the versions
npmjs.com has of it. With pnpr, each package name has a single home. Verdaccio
turns down a version npmjs.com already has, and prefers npmjs.com's tags to
yours.

[pkg.pr.new](https://pkg.pr.new) does make every commit installable, but it is
a hosted service that runs from GitHub Actions, with nothing to run on your own
machine.

cs-npmrevs serves your builds and npmjs.com's versions of the same package
together, from a directory or a container registry, wherever you are working.
[SPEC.md](SPEC.md#13-other-registries) compares them in detail.

## Lockfiles

npm records the tarball URL of each package it installs, so a lockfile made
through `serve` names this machine for every local version. `cs-npmrevs lockfile
check` finds those entries, and `cs-npmrevs lockfile rewrite` points them at
npmjs.com, integrity included.

## Docs

- [INSTALL.md](INSTALL.md) · how to get the tool, and the setup it needs once
- [MANUAL.md](MANUAL.md) · the full surface: commands, options, the registry API, exit codes
- [SPEC.md](SPEC.md) · what the behaviour must be, and what is left open
- [CONTRIBUTING.md](CONTRIBUTING.md) · conventions, and the rituals a diff does not show
- [AGENTS.md](AGENTS.md) · where an agent looks first

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md). It applies to coding agents as well as
to people.

## License

[Apache-2.0](LICENSE).
