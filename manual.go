// Package npmrevs holds the documentation the cs-npmrevs binary carries with it. It
// is not this module's entry point: cs-npmrevs is a command-line tool rather than
// a library, and the program is cmd/cs-npmrevs.
//
//	go install github.com/codesweep-ai/npmrevs/cmd/cs-npmrevs@latest
//
// The package sits at the module root only because a //go:embed directive
// cannot reach a parent directory and the file it embeds, MANUAL.md, is there.
// Everything the tool actually does lives under internal/.
package npmrevs

import _ "embed"

// ManualMD is MANUAL.md, embedded at build time so `cs-npmrevs manual` prints the
// command reference from the binary. A machine with the tool has the docs, with
// no checkout and no network.
//
//go:embed MANUAL.md
var ManualMD string
