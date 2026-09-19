# @codesweep-ai/npmrevs

> **Package, resolve and serve each revision's build of an npm package, so a team of AI coding agents can install work in progress.**

[![CI](https://github.com/codesweep-ai/npmrevs/actions/workflows/ci.yml/badge.svg)](https://github.com/codesweep-ai/npmrevs/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](https://github.com/codesweep-ai/npmrevs/blob/main/LICENSE)

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

The tool is written in Go, and packaged here for npm projects.

## Quickstart

```bash
npm install --save-dev @codesweep-ai/npmrevs

# ./my-package is yours: any directory with a package.json
mkdir -p ./data
npm pack ./my-package --pack-destination ./data
npx cs-npmrevs serve --data ./data &
printf 'registry=http://127.0.0.1:4873/\n' > .npmrevs.npmrc
NPM_CONFIG_USERCONFIG=.npmrevs.npmrc npm install my-package@1.0.0
```

## Docs

The documentation lives in the [codesweep-ai/npmrevs](https://github.com/codesweep-ai/npmrevs)
GitHub repository, and none of it ships in this package.

- [INSTALL.md](https://github.com/codesweep-ai/npmrevs/blob/main/INSTALL.md) · how to get the tool, and the setup it needs once
- [MANUAL.md](https://github.com/codesweep-ai/npmrevs/blob/main/MANUAL.md) · the full surface: commands, options, the registry API, exit codes
- [SPEC.md](https://github.com/codesweep-ai/npmrevs/blob/main/SPEC.md) · what the behaviour must be, and what is left open
- [CONTRIBUTING.md](https://github.com/codesweep-ai/npmrevs/blob/main/CONTRIBUTING.md) · conventions, and the rituals a diff does not show

## License

[Apache-2.0](https://github.com/codesweep-ai/npmrevs/blob/main/LICENSE).
