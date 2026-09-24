# cs-npmrevs — build/test/release.
# `make build` produces bin/cs-npmrevs via goreleaser (single host target,
# version-stamped, CGO_ENABLED=0). Falls back to plain `go build` if goreleaser
# is absent. See .goreleaser.yaml.

GORELEASER ?= goreleaser
CS_LINT    ?= go tool cs-lint
# The linters the gates shell out to, all pinned and all built from the module
# cache, so a fresh checkout runs `make check` with nothing installed by hand.
# cs-lint, cs-ledger, deadcode and actionlint are `tool` directives in go.mod and
# run with `go tool`. golangci-lint is one in go.golangci.mod, which says at its
# head why it needs a module file of its own.
GOLANGCI   := bin/tools/golangci-lint
BIN        := bin/cs-npmrevs
PKG        := ./cmd/cs-npmrevs
PREFIX     ?= $(HOME)/.local
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS    := -s -w
# Tracked files where git knows them, every .go file where it does not. A
# fresh clone before its first commit has nothing tracked, and an empty list
# makes `gofmt -l` read stdin and hang rather than check anything.
GO_FILES   := $(shell git ls-files '*.go' 2>/dev/null | grep . || find . -name '*.go' -not -path './bin/*' -not -path './dist/*')

# What $(BIN) is made of. It is a real target rather than a phony one, so make
# skips the build when the binary is already newer than every input, which is
# what stops `make install` from repeating the `make build` that just ran.
#
# `find` rather than $(GO_FILES): a source file that is new and not yet added to
# the index is still an input. $(GIT_DIR)/HEAD is one because the version is the
# VCS stamp Go embeds, so a commit changes the binary even when no source did.
# The embedded files are listed because //go:embed makes them compile-time
# inputs; add to the list when a new one is embedded.
GIT_DIR    := $(shell git rev-parse --git-dir 2>/dev/null)
EMBED_DEPS := MANUAL.md
# //go:embed inputs deliberately left out of $(EMBED_DEPS). Nothing belongs here
# yet; `make embed-check` allows exactly this list and nothing else.
EMBED_EXEMPT :=
BUILD_DEPS := $(shell find . \( -name bin -o -name dist -o -name node_modules -o -name .git \) -prune -o -name '*.go' -print) \
              go.mod go.sum .goreleaser.yaml Makefile $(EMBED_DEPS) $(wildcard $(GIT_DIR)/HEAD)

# Coverage is measured on every `make test` rather than in a mode of its own,
# because a number nobody measures is a number that only falls.
#
# The suite writes Go binary coverage data into $(COVERDIR) and merges it with
# `go tool covdata`, rather than writing a text profile with -coverprofile. With
# -coverpkg every package's test binary emits a block set for every other
# package, and merging text profiles counts each block once per binary. The
# binary format merges by union, which is the number that means something.
#
# -test.gocoverdir must be absolute: `go test` runs each package's test binary
# with that package's directory as its working directory, so a relative path
# would scatter the data one directory per package.
#
# internal/testpkg builds fixtures for the tests and ships nothing, so it is not
# counted against the code it tests.
COVERDIR   ?= .coverage
COVER_ABS  := $(abspath $(COVERDIR))
COVERPKG    = $(shell go list ./... | grep -v '/internal/testpkg' | paste -sd, -)
COVERFLAGS  = -covermode=atomic -coverpkg=$(COVERPKG)
# The floor the suite has to clear. Raised when a tier lands, never lowered to
# make a run green.
COVER_MIN  ?= 75

.PHONY: help tidy-check embed-check build build-go install uninstall test test-race coverage coverage-check ci \
        vet fmt fmt-check check lint deadcode actionlint prose refs oss surface ledger \
        snapshot release release-check clean npm-build npm-snapshot npm-pack npm-local npm-publish images-snapshot

.DEFAULT_GOAL := help

## help: list available targets (this menu)
help:
	@echo "cs-npmrevs make targets:"
	@grep -E '^## [a-z][a-z0-9-]*: ' $(MAKEFILE_LIST) | sed -E 's/^## ([^:]+): (.*)/  \1|\2/' | column -t -s '|'
	@echo ""
	@echo "  PREFIX=$(PREFIX) (install location; override with make install PREFIX=/usr/local)"

## build: host binary at bin/cs-npmrevs via goreleaser (single target)
##
## A phony alias for $(BIN), so the work sits on a file target and make can skip
## it. `make build install`, and an `install` after a build, then copy what is
## already there instead of building the same binary a second time.
##
## --skip=before, because .goreleaser.yaml's before hooks are `go mod tidy`,
## `go vet ./...` and `go test ./...`: release gates that `make check` runs in
## its own right. `make snapshot` and `make release` still run them.
build: $(BIN)

$(BIN): $(BUILD_DEPS)
	@mkdir -p $(dir $@)
	@if command -v $(GORELEASER) >/dev/null 2>&1; then \
		VERSION='$(VERSION)' $(GORELEASER) build --single-target --snapshot --clean --skip=before --output $@; \
	else \
		echo "goreleaser not found; using go build (run 'make build-go' explicitly to force)"; \
		$(MAKE) build-go; \
	fi

## build-go: host binary at bin/cs-npmrevs via plain go build
build-go:
	@mkdir -p $(dir $(BIN))
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) $(PKG)

## versions: what this build is made of: this repo's binary, every pinned tool,
## the Go toolchain, and whether a workspace is overriding the go.mod pins. It
## runs from source and depends on nothing, because reporting a version must not
## trigger a build. -buildvcs=true because `go run` leaves out the VCS stamp by
## default, and that stamp is the version.
.PHONY: versions
versions:
	@if out="$$(go run -buildvcs=true -ldflags '$(LDFLAGS)' $(PKG) version 2>&1)"; then \
		printf '%-14s %-42s %s\n' '$(notdir $(BIN))' "$$(printf '%s\n' "$$out" | awk 'NR==1{print $$2}')" 'this repo'; \
	else \
		printf '%-14s %s\n' '$(notdir $(BIN))' "FAILED: $$(printf '%s\n' "$$out" | head -1)"; \
	fi
	@ver='{{with .Module}}{{if .Replace}}{{.Replace.Path}}{{else if .Version}}{{.Version}}{{else}}{{.Dir}}{{end}}{{end}}'; \
	for t in $$(go list tool 2>/dev/null); do \
		v="$$(go list -f "$$ver" $$t 2>/dev/null)"; \
		printf '%-14s %s\n' "$$(basename $$t)" "$${v:-FAILED}"; \
	done; \
	for t in $$(GOWORK=off go list -modfile=go.golangci.mod tool 2>/dev/null); do \
		v="$$(GOWORK=off go list -modfile=go.golangci.mod -f "$$ver" $$t 2>/dev/null)"; \
		printf '%-14s %s\n' "$$(basename $$t)" "$${v:-FAILED}"; \
	done
	@printf '%-14s %s\n' 'go' "$$(go env GOVERSION)"
	@w="$$(go env GOWORK)"; \
	case "$$w" in \
		''|off) printf '%-14s %s\n' 'workspace' 'off: versions above are go.mod pins' ;; \
		*)      printf '%-14s %s\n' 'workspace' "$$w: local checkouts override the go.mod pins" ;; \
	esac

## repin: pin each codesweep-ai tool to the last commit its CI built, and report
##
## Each project lists the commits its CI built and passed in `built` in the
## ci-status.json it publishes (codesweep-ai/dashboards SPEC.md), newest first,
## so a pin never lands on a commit CI failed, is still building, or never built
## because it changed only the ledger. curl reads it from the project's Pages
## site, which no API rate limit applies to. A tool whose project's file cannot
## be read or lists no build keeps its pin, and says so. Uses GOPROXY=direct so
## each commit is read from its repository, whatever the module proxy holds. Uses
## GOWORK=off so this edits the recorded pins even while a workspace is serving
## local checkouts.
.PHONY: repin
repin:
	@tools="$$(go list tool 2>/dev/null | grep codesweep-ai || true)"; \
	if [ -z "$$tools" ]; then \
		echo "no codesweep-ai tools declared yet; add the first with:" >&2; \
		echo "  GOPROXY=direct go get -tool github.com/codesweep-ai/lint/cmd/cs-lint@main" >&2; \
		exit 1; \
	fi; \
	pins=""; \
	for t in $$tools; do \
		owner=$$(echo "$$t" | cut -d/ -f2); repo=$$(echo "$$t" | cut -d/ -f3); \
		built=$$(curl -fsSL "https://$$owner.github.io/$$repo/ci-status.json" 2>/dev/null | \
			sed -n '/^ "built": \[$$/,/^ \]/s/^ *"commit": *"\([0-9a-f]\{40\}\)".*/\1/p' | head -1); \
		if [ -n "$$built" ]; then echo "$$repo: $$(echo "$$built" | cut -c1-7), the last commit its CI built"; \
			pins="$$pins $$t@$$built"; \
		else echo "$$repo: held, as its status file lists no build"; fi; \
	done; \
	if [ -n "$$pins" ]; then GOWORK=off GOPROXY=direct go get -tool $$pins; fi
	@GOWORK=off go mod tidy
	@$(MAKE) versions

## install: build and copy the binary into $(PREFIX)/bin, and pack its npm packages for later builds
install: build npm-pack
	@mkdir -p $(PREFIX)/bin
	install -m 0755 $(BIN) $(PREFIX)/bin/cs-npmrevs
	@echo "installed $(PREFIX)/bin/cs-npmrevs"

## uninstall: remove the binary from $(PREFIX)/bin
uninstall:
	rm -f $(PREFIX)/bin/cs-npmrevs

## test: the unit suite, with coverage into $(COVERDIR)
##
## It includes the tier that drives the real npm client through the server,
## which skips where npm is not on the PATH. -short skips it on purpose.
test:
	@rm -rf $(COVER_ABS) && mkdir -p $(COVER_ABS)
	go test $(COVERFLAGS) ./... -args -test.gocoverdir=$(COVER_ABS)

## test-race: the unit suite under the race detector. The server answers many
## requests at once from one index, one cache and one image source.
test-race:
	go test -race ./...

## coverage: the per-function coverage of the last run
coverage:
	@go tool covdata textfmt -i=$(COVER_ABS) -o=$(COVER_ABS)/merged.out
	@go tool cover -func=$(COVER_ABS)/merged.out

## coverage-check: fail when total coverage is under $(COVER_MIN)%
coverage-check:
	@go tool covdata textfmt -i=$(COVER_ABS) -o=$(COVER_ABS)/merged.out
	@total=$$(go tool cover -func=$(COVER_ABS)/merged.out | awk '/^total:/ {gsub(/%/,"",$$NF); print $$NF}'); \
	echo "coverage: $$total% (floor $(COVER_MIN)%)"; \
	awk -v t=$$total -v m=$(COVER_MIN) 'BEGIN { exit (t+0 >= m+0) ? 0 : 1 }' \
		|| { echo "coverage is under the floor" >&2; exit 1; }

## vet: the compiler-adjacent checks
vet:
	go vet ./...

## fmt: format every tracked Go file
fmt:
	gofmt -w $(GO_FILES)

## fmt-check: fail when a tracked Go file is not formatted
fmt-check:
	@out=$$(gofmt -l $(GO_FILES)); \
	if [ -n "$$out" ]; then echo "not gofmt'd:"; echo "$$out"; exit 1; fi

## tidy-check: go.mod and go.sum are what `go mod tidy` would write
##
## It says what moved instead of quietly absorbing it, and it puts the originals
## back before failing, so a red gate leaves the tree as it found it. GOWORK=off,
## so a workspace serving local checkouts cannot make an untidy go.mod look tidy.
tidy-check:
	@t="$$(mktemp -d)"; cp go.mod go.sum "$$t/"; \
	GOWORK=off go mod tidy || { cp "$$t/go.mod" go.mod; cp "$$t/go.sum" go.sum; rm -rf "$$t"; exit 1; }; \
	if cmp -s go.mod "$$t/go.mod" && cmp -s go.sum "$$t/go.sum"; then \
		rm -rf "$$t"; echo "tidy: go.mod and go.sum are what \`go mod tidy\` writes"; \
	else \
		echo "go.mod/go.sum are not tidy; \`go mod tidy\` would apply:" >&2; \
		diff -u "$$t/go.mod" go.mod >&2; diff -u "$$t/go.sum" go.sum >&2; \
		cp "$$t/go.mod" go.mod; cp "$$t/go.sum" go.sum; rm -rf "$$t"; \
		exit 1; \
	fi

## embed-check: every //go:embed input is a prerequisite of the binary
##
## $(EMBED_DEPS) is written by hand, and an embed added without a line there
## leaves make holding a binary it calls current while the bytes inside it have
## moved. `go list` resolves the patterns itself, so this compares against what
## the toolchain actually embeds.
embed-check:
	@deps="$$(mktemp)"; embeds="$$(mktemp)"; raw="$$(mktemp)"; \
	printf '%s\n' $(patsubst ./%,%,$(BUILD_DEPS)) $(EMBED_EXEMPT) | LC_ALL=C sort -u >"$$deps"; \
	if ! go list -f '{{range .EmbedFiles}}{{$$.Dir}}/{{.}}{{"\n"}}{{end}}' ./... >"$$raw"; then \
		rm -f "$$deps" "$$embeds" "$$raw"; \
		echo "embed-check: go list failed, so the embed set is unknown" >&2; exit 1; \
	fi; \
	grep -v '/node_modules/' "$$raw" | sed "s|^$$PWD/||" | grep . | LC_ALL=C sort -u >"$$embeds"; \
	missing="$$(LC_ALL=C comm -23 "$$embeds" "$$deps")"; n="$$(wc -l <"$$embeds")"; \
	rm -f "$$deps" "$$embeds" "$$raw"; \
	if [ -n "$$missing" ]; then \
		echo "//go:embed reads these, and no prerequisite of $(BIN) covers them:" >&2; \
		printf '  %s\n' $$missing >&2; \
		echo "add each to EMBED_DEPS, or a change to one will not rebuild the binary" >&2; \
		exit 1; \
	fi; \
	echo "embed: all $$n //go:embed inputs are prerequisites of $(notdir $(BIN))"

# Built rather than run with `go tool`, because -modfile is refused in workspace
# mode. The build is the only step that reads go.golangci.mod, so only the build
# turns the workspace off; the linter then runs with it back on.
$(GOLANGCI): go.golangci.mod
	@mkdir -p $(@D)
	@GOWORK=off go build -modfile=go.golangci.mod -o $@ \
		github.com/golangci/golangci-lint/v2/cmd/golangci-lint

## lint: the Go rules from .golangci.yml (see that file for what is on and why)
lint: $(GOLANGCI)
	$(GOLANGCI) run

## deadcode: functions no entry point reaches, which `unused` cannot see
deadcode:
	@out=$$(go tool deadcode -test ./...); \
	if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

## actionlint: the workflow files, which the forge validates only by refusing to
## run them
actionlint:
	go tool actionlint

## prose: check how this repository's documents are written
prose:
	$(CS_LINT) prose

## refs: check that everything the documents point at is there
refs:
	$(CS_LINT) refs

## oss: the rules this repo has to satisfy as a published project
oss:
	$(CS_LINT) oss

## surface: check the docs against the binary, the code and the build
surface: build
	$(CS_LINT) surface

# The four targets above are one shared tool: github.com/codesweep-ai/lint,
# pinned in go.mod and run with `go tool`, so the gates use the version this
# repo records. `make repin` moves that pin. Its knobs for this repo live in
# .cs-lint.yaml, and `cs-lint <linter> --explain` says what each rule wants.

## ledger: validate the issue records and prove ledger.html is current
ledger:
	go tool cs-ledger check ledger

## check: the one command before pushing
check: fmt-check tidy-check embed-check vet lint deadcode test test-race coverage-check prose refs oss surface

# say prints a heading above each gate, so a long run reads as a list rather
# than as a wall. Bold where a terminal is reading it and plain where a pipe
# is, so `make ci > ci.log` leaves a log somebody can read.
define say
@if [ -t 1 ]; then printf '\n\033[1m==> %s\033[0m\n' "$(1)"; else printf '\n==> %s\n' "$(1)"; fi
endef

## ci: every gate the CI workflow runs, on this machine
##
## One Linux leg of .github/workflows/ci.yml, in the order CI runs it, so a red
## build is something you can see before you push rather than after. What it
## cannot reproduce it names on the way out.
ci:
	$(call say,the gate a contributor runs before pushing)
	@$(MAKE) --no-print-directory check
	$(call say,actionlint)
	@$(MAKE) --no-print-directory actionlint
	$(call say,build)
	@$(MAKE) --no-print-directory build
	$(call say,release manifest)
	@$(MAKE) --no-print-directory release-check
	$(call say,ledger)
	@$(MAKE) --no-print-directory ledger
	@printf '\nci: every gate ran. Not reproduced here: build-test on macOS.\n'

## snapshot: build every release target without publishing
snapshot:
	VERSION='$(VERSION)' $(GORELEASER) release --snapshot --clean --skip=publish,sign,sbom

## release-check: validate the release manifest
release-check:
	$(GORELEASER) check

## release: cut a release from the current tag
release:
	$(GORELEASER) release --clean

# The npm distribution. cs-npmrevs is a Go binary, and a project with no Go
# toolchain has no way to pin it in a file its reviewers read. npm is that file
# for a JavaScript project, so the release binaries are also published as five
# packages: one per platform, plus the wrapper that picks between them.
# npm/build.mjs generates all five from goreleaser's output; nothing under
# npm/dist is committed.
#
# The snapshot version is a placeholder, because npm refuses a version that is
# not semver and goreleaser's snapshot name is a commit description.
NPM_SNAPSHOT_VERSION ?= 0.0.0-snapshot.0

## npm-build: package the binaries in dist/ as npm packages, into npm/dist
npm-build:
	node npm/build.mjs

## npm-snapshot: build every target, package it for npm, and show what would publish
npm-snapshot:
	$(GORELEASER) build --snapshot --clean --skip=before
	@CS_NPMREVS_NPM_VERSION='$(NPM_SNAPSHOT_VERSION)' node npm/build.mjs
	@./npm/publish.sh --dry-run

## npm-pack: package a dev build into cs-npmrevs's data directory, for a later build to install
##
## `make install` runs it too, so every install leaves its packages where a
## later build installing through cs-npmrevs finds them. A machine without
## goreleaser, node or npm skips it, and installs the binary all the same.
npm-pack: build
	@if command -v $(GORELEASER) >/dev/null 2>&1 && command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then \
		./npm/local-registry.sh pack; \
	else \
		echo "npm-pack: SKIP (needs goreleaser, node and npm)"; \
	fi

## npm-local: serve a dev build from this machine, and print how to install it
npm-local: build
	NPMREVS=$(abspath $(BIN)) ./npm/local-registry.sh

## npm-publish: publish npm/dist to the registry (platform packages first)
npm-publish:
	./npm/publish.sh

## images-snapshot: build the image of every package in npm/dist, and push nothing
images-snapshot: build
	NPMREVS=$(abspath $(BIN)) ./npm/publish-images.sh --dry-run npm/dist/npmrevs-*/ npm/dist/npmrevs

## clean: remove build output and coverage data
clean:
	rm -rf bin dist npm/dist $(COVERDIR)
