# npmrevs

> **A scratch npm registry: npm installs your local builds, and everything else comes from npmjs.com.**

[![CI](https://github.com/codesweep-ai/npmrevs/actions/workflows/ci.yml/badge.svg)](https://github.com/codesweep-ai/npmrevs/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
![Registry](https://img.shields.io/badge/npm-registry-informational)
![Platforms](https://img.shields.io/badge/platform-Linux%20%C2%B7%20macOS-lightgrey)

`cs-npmrevs` serves npm packages you built but did not publish. Drop their
tarballs in a directory, point npm at `cs-npmrevs serve`, and `npm install` gets
your build, with every other package passed through from npmjs.com. A local
version takes the place of a published one with the same number, so the build
of a commit installs under the version it will be published as.

It also carries builds in container images, one version of one package per
image, so a registry such as ghcr.io can hold what never goes to npmjs.com.
Verdaccio with an uplink does part of this, and merges the uplink's dist-tags
over your own. cs-npmrevs computes them the way npmjs.com would.

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

## Builds in a container registry

```bash
ref=$(cs-npmrevs image build acme-tool-1.1.0-dev.3.tgz)   # ghcr.io/acme/npm/tool:1.1.0-dev.3
podman load -i acme-tool-1.1.0-dev.3.image.tar && podman push "$ref"

cs-npmrevs fetch @acme/tool --latest 5                      # copy them back out
cs-npmrevs serve --images ghcr.io --images-scope @acme      # or serve them from there
```

`image build` needs the tarball and nothing else: no container engine, and no
earlier image. The same tarball always gives the same image. `fetch` follows a
wrapper's exact dependencies in its scope, so a package split into one per
platform comes back whole.

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
