# @codesweep-ai/npmrevs

> **A scratch npm registry: npm installs your local builds, and everything else comes from npmjs.com.**

[![CI](https://github.com/codesweep-ai/npmrevs/actions/workflows/ci.yml/badge.svg)](https://github.com/codesweep-ai/npmrevs/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](https://github.com/codesweep-ai/npmrevs/blob/main/LICENSE)

`cs-npmrevs` serves npm packages you built but did not publish. Drop their
tarballs in a directory, point npm at `cs-npmrevs serve`, and `npm install` gets
your build, with every other package passed through from npmjs.com.

It also carries builds in container images, one version per image, so a
registry such as ghcr.io can hold what never goes to npmjs.com.

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
