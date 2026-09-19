// Package cli assembles the cs-npmrevs command tree.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// devVersion marks a binary that carried no release stamp.
const devVersion = "dev"

// Version is stamped at build time by the release build.
var Version = devVersion

// buildVersion reports the release stamp when there is one, and otherwise the
// module version the toolchain recorded, which for a build from a checkout is
// the commit's pseudo-version.
func buildVersion() string {
	if Version != devVersion {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return Version
	}
	return info.Main.Version
}

// Exit statuses. A script needs to tell a failure from a mistake in how it
// called the tool, and "there is no such image" from both.
const (
	exitOK       = 0
	exitFailed   = 1
	exitBadUsage = 2
	exitNotFound = 3
)

// exitError carries an exit status out of a command.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// failed is a command that ran and did not succeed.
func failed(err error) error { return &exitError{code: exitFailed, err: err} }

// notFound is a command that found nothing to act on.
func notFound(err error) error { return &exitError{code: exitNotFound, err: err} }

// app is what every command shares: the streams, the environment and the log.
type app struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	verbose, quiet bool
}

// logger writes to stderr: at debug level under --verbose, and errors only
// under --quiet.
func (a *app) logger() *slog.Logger {
	level := slog.LevelInfo
	switch {
	case a.verbose:
		level = slog.LevelDebug
	case a.quiet:
		level = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(a.stderr, &slog.HandlerOptions{Level: level}))
}

// Execute runs the command tree and returns the process exit status.
func Execute() int {
	return run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.Getenv)
}

// run is Execute with its inputs named, so tests drive the real command tree
// and can stop a server by cancelling ctx.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	a := &app{stdout: stdout, stderr: stderr, getenv: getenv}
	root := newRoot(a)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return exitOK
	}
	fmt.Fprintln(stderr, "cs-npmrevs:", err)
	if ee, ok := errors.AsType[*exitError](err); ok {
		return ee.code
	}
	return exitBadUsage
}

func newRoot(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "cs-npmrevs",
		Short: "Package, resolve and serve each revision's build of an npm package, so a team of AI coding agents can install work in progress",
		Long: "A team of AI coding agents needs to share work in progress. One agent's\n" +
			"unfinished build of a package is often what another installs next. That build\n" +
			"is not a version meant for people, and a version published to npmjs.com can\n" +
			"never be taken back.\n\n" +
			"cs-npmrevs makes every revision of an npm package installable without publishing\n" +
			"it. Each build, of a commit or of work not yet committed, becomes a prerelease\n" +
			"version, shared through a container registry or a directory of tarballs. npm\n" +
			"installs it through cs-npmrevs serve the way it installs a published version,\n" +
			"and every other package comes from npmjs.com.\n\n" +
			"  serve     serve the registry\n" +
			"  image     build an image from a tarball, or say what one holds\n" +
			"  extract   copy the tarballs out of an image\n" +
			"  fetch     copy a package's versions out of their images\n" +
			"  lockfile  find or rewrite lockfile entries that point at this machine",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVarP(&a.verbose, "verbose", "v", false, "log every request and every step")
	root.PersistentFlags().BoolVarP(&a.quiet, "quiet", "q", false, "log errors only")
	root.MarkFlagsMutuallyExclusive("verbose", "quiet")

	root.AddCommand(
		serveCmd(a),
		imageCmd(a),
		extractCmd(a),
		fetchCmd(a),
		lockfileCmd(a),
		manualCmd(a),
		versionCmd(a),
	)
	return root
}

func versionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			fmt.Fprintf(a.stdout, "cs-npmrevs %s (%s/%s, %s)\n",
				buildVersion(), runtime.GOOS, runtime.GOARCH, runtime.Version())
			return nil
		},
	}
}
