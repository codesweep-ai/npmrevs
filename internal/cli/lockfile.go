package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/codesweep-ai/npmrevs/internal/lockfile"
)

func lockfileCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lockfile",
		Short: "Find or rewrite lockfile entries that point at this machine",
		Long: "An install through cs-npmrevs records cs-npmrevs's address in the lockfile for\n" +
			"every package it served. Such a lockfile installs nowhere the server is not\n" +
			"running. `lockfile check` finds those entries, and `lockfile rewrite` points\n" +
			"them at a public registry.",
		Args: cobra.NoArgs,
		RunE: needsSubcommand,
	}
	cmd.AddCommand(lockfileCheckCmd(a), lockfileRewriteCmd(a))
	return cmd
}

func lockfileCheckCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "check [FILE]",
		Short: "List the entries that resolved through a loopback address",
		Long: "List every entry of FILE (default package-lock.json) whose resolved URL is a\n" +
			"loopback address, and exit 1 when there is one. It reads nothing but the file.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path := lockPath(args)
			data, err := os.ReadFile(path)
			if err != nil {
				return failed(err)
			}
			entries, err := lockfile.Local(data)
			if err != nil {
				return failed(fmt.Errorf("%s: %w", path, err))
			}
			for _, e := range entries {
				fmt.Fprintf(a.stdout, "%s\t%s@%s\t%s\n", e.Key, e.Name, e.Version, e.Resolved)
			}
			if len(entries) > 0 {
				return failed(fmt.Errorf("%s: %d entries resolved through this machine; run `cs-npmrevs lockfile rewrite`", path, len(entries)))
			}
			return nil
		},
	}
}

type rewriteOptions struct {
	to     string
	dryRun bool
}

func lockfileRewriteCmd(a *app) *cobra.Command {
	o := &rewriteOptions{}
	cmd := &cobra.Command{
		Use:   "rewrite [FILE]",
		Short: "Point loopback entries at a public registry",
		Long: "Look every loopback entry of FILE (default package-lock.json) up on --to, and\n" +
			"point it at the tarball that registry serves, with that tarball's integrity.\n" +
			"A version the registry does not hold stops the rewrite, and nothing is\n" +
			"written. Only the changed URLs and integrities move; the rest of the file is\n" +
			"left as npm wrote it.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.lockfileRewrite(cmd, lockPath(args), o)
		},
	}
	cmd.Flags().StringVar(&o.to, "to", DefaultUpstream, "the registry to point the entries at")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "print what would change, and write nothing")
	return cmd
}

func lockPath(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return "package-lock.json"
}

func (a *app) lockfileRewrite(cmd *cobra.Command, path string, o *rewriteOptions) error {
	to, err := url.Parse(o.to)
	if err != nil || (to.Scheme != "http" && to.Scheme != "https") || to.Host == "" {
		return fmt.Errorf("--to %q is not an http or https registry URL", o.to)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return failed(err)
	}
	r := &lockfile.Resolver{Registry: to, Client: &http.Client{Timeout: 2 * time.Minute}, UserAgent: "cs-npmrevs/" + buildVersion()}
	changes, err := lockfile.Plan(cmd.Context(), data, r)
	if missing, ok := errors.AsType[*lockfile.MissingError](err); ok {
		for _, e := range missing.Entries {
			fmt.Fprintf(a.stdout, "missing\t%s@%s\t%s\n", e.Name, e.Version, e.Key)
		}
		return failed(fmt.Errorf("%s: %w, so nothing was rewritten", path, err))
	}
	if err != nil {
		return failed(err)
	}
	for _, c := range changes {
		note := ""
		if c.IntegrityChanged() {
			note = "\t(integrity changed: the local build is not the published one)"
		}
		fmt.Fprintf(a.stdout, "%s@%s\t%s -> %s%s\n", c.Name, c.Version, c.Resolved, c.NewResolved, note)
	}
	if o.dryRun || len(changes) == 0 {
		return nil
	}
	out, err := lockfile.Apply(data, changes)
	if err != nil {
		return failed(err)
	}
	if err := lockfile.WriteFile(path, out); err != nil {
		return failed(err)
	}
	return nil
}
