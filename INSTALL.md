# Installing cs-npmrevs

`cs-npmrevs` is a single static binary. It needs no runtime and no container
engine: you put one file on the path and run it. It keeps its files in one data
directory and one cache, both under your home directory unless you move them.

Once it runs, [`MANUAL.md`](MANUAL.md) has the full surface and
[`README.md`](README.md) shows what it is for.

**No version has been tagged yet, so there is nothing on the releases page
today.** Until there is, install with the Go toolchain or from a clone.

## 1. Get it

Four routes. Take the first one that fits.

### With the Go toolchain

The shortest route, and the one that keeps the tool current:

```bash
go install github.com/codesweep-ai/npmrevs/cmd/cs-npmrevs@latest
```

This needs **Go 1.27 or newer**. The binary lands in `$(go env GOPATH)/bin`,
which is `~/go/bin` unless you have moved it. Put that directory on your `PATH`
if it is not already there:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

A project that wants the same version for everybody pins it in its own
`go.mod` instead, which records the version where a reviewer sees it change:

```bash
go get -tool github.com/codesweep-ai/npmrevs/cmd/cs-npmrevs@latest
go tool cs-npmrevs version
```

### With npm

Take this route in a project that already has a `package.json`. No Go toolchain
is involved: the binary is packaged for npm and installs like any other dev
dependency.

```bash
npm install --save-dev @codesweep-ai/npmrevs
npx cs-npmrevs version
```

What installs is a wrapper over four packages, one per platform. Each declares
the operating system and architecture it holds a binary for. npm installs the
one this machine can run and skips the other three.

A release takes the `latest` tag. Every commit on `main` that passes CI also
goes out under the `dev` tag, versioned by the commit it came from:

```bash
npm install --save-dev @codesweep-ai/npmrevs@dev
```

### From a release archive

Take this route on a machine with no Go toolchain. Replace `VERSION` with the
release you want, and `linux_amd64` with your platform:

```bash
VERSION=0.1.0
curl -fsSLO https://github.com/codesweep-ai/npmrevs/releases/download/v${VERSION}/cs-npmrevs_${VERSION}_linux_amd64.tar.gz
curl -fsSLO https://github.com/codesweep-ai/npmrevs/releases/download/v${VERSION}/checksums.txt
sha256sum --check --ignore-missing checksums.txt
tar -xzf cs-npmrevs_${VERSION}_linux_amd64.tar.gz
install -m 0755 cs-npmrevs ~/.local/bin/cs-npmrevs
```

The checksum file is signed. To verify the signature as well, with
[cosign](https://docs.sigstore.dev/cosign/system_config/installation/)
installed:

```bash
curl -fsSLO https://github.com/codesweep-ai/npmrevs/releases/download/v${VERSION}/checksums.txt.sig
curl -fsSLO https://github.com/codesweep-ai/npmrevs/releases/download/v${VERSION}/checksums.txt.pem
cosign verify-blob checksums.txt \
  --signature checksums.txt.sig \
  --certificate checksums.txt.pem \
  --certificate-identity-regexp 'https://github\.com/codesweep-ai/npmrevs/\.github/workflows/release\.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Each archive also ships a software bill of materials as `<archive>.sbom.json`.

### From a clone

For working on cs-npmrevs itself, or for a platform the releases do not cover:

```bash
git clone https://github.com/codesweep-ai/npmrevs
cd npmrevs
make install            # builds, then copies into ~/.local/bin
```

`make install` puts the binary in `$(PREFIX)/bin`, where `PREFIX` defaults to
`$(HOME)/.local`. Override it for a system-wide install:

```bash
sudo make install PREFIX=/usr/local
```

`make uninstall` removes it again, with the same `PREFIX`.

## 2. Host prerequisites

The binary itself needs nothing. What you do around it may:

| Program | What needs it |
|---|---|
| `npm` | installing through `serve`, and packing a tarball with `npm pack` |
| `podman` | pushing an archive `image build` wrote to a registry; cs-npmrevs itself never needs a container engine |

Install them on Fedora or RHEL:

```bash
sudo dnf install -y nodejs podman
```

On Debian or Ubuntu, run:

```bash
sudo apt-get install -y nodejs npm podman
```

On macOS, podman runs its containers in a virtual machine, so start one too:

```bash
brew install node podman
podman machine init && podman machine start
```

## 3. First-run setup

There is none. `serve` creates its data directory the first time it runs, and
the cache the first time it reads an image.

A private package on ghcr.io needs a token that can read it. Put it in
`GH_TOKEN` before `fetch`, `extract` or `serve --images` reach for it.

## 4. Verify the installation

Check that the binary runs and reports its version:

```console
$ cs-npmrevs version
cs-npmrevs …
```

The elision stands for the version and the platform, which depend on how the
binary was built.

Then start it on a free port, and ask it what it serves:

```bash
cs-npmrevs serve --listen 127.0.0.1:0 --print-port &
```

The first line it prints is the port. Use it in place of `PORT`:

```bash
curl -s http://127.0.0.1:PORT/-/npmrevs
kill %1
```

## Shell completion

`cs-npmrevs completion` writes a completion script for `bash`, `zsh`, `fish` or
`powershell`. Load it from your shell's startup file:

```bash
cs-npmrevs completion bash > ~/.local/share/bash-completion/completions/cs-npmrevs
```

```bash
cs-npmrevs completion zsh > "${fpath[1]}/_cs-npmrevs"
```

## Running under a container

cs-npmrevs needs no container engine, and runs inside a container as it runs
anywhere else. The binary carries root certificates of its own, so a `FROM
scratch` image reaches npmjs.com and ghcr.io with nothing added. Inside a
container it has to listen on every interface, so the port can be published:

```bash
cs-npmrevs serve --listen 0.0.0.0:4873 --data /data
```

## State and cache locations

| What | Where |
|---|---|
| The data directory | `$CS_NPMREVS_DATA`, else `$XDG_DATA_HOME/cs-npmrevs/data`, else `~/.local/share/cs-npmrevs/data` |
| The cache | `$CS_NPMREVS_CACHE`, else `$XDG_CACHE_HOME/cs-npmrevs`, else `~/.cache/cs-npmrevs` |

Both are safe to delete while no `serve` is running.

## Upgrading

With the Go toolchain, re-run the install command, which replaces the binary in
place. With npm, move the version in `package.json`. From a release archive,
fetch the new one and replace the old binary. From a clone, run
`git pull && make install`.

## Removing it

```bash
make uninstall                     # a clone install
rm ~/go/bin/cs-npmrevs                # a `go install` install
rm -rf ~/.local/share/cs-npmrevs ~/.cache/cs-npmrevs
```
