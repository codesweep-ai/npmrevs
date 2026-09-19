// Command cs-npmrevs is a scratch npm registry: it serves local builds of npm
// packages and passes everything else through from npmjs.com.
package main

import (
	"os"

	// Root certificates compiled in, used only where the system has none: a
	// scratch container or a bare sandbox still reaches npmjs.com and ghcr.io.
	_ "golang.org/x/crypto/x509roots/fallback"

	"github.com/codesweep-ai/npmrevs/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
