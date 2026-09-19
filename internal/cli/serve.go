package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/codesweep-ai/npmrevs/internal/datadir"
	"github.com/codesweep-ai/npmrevs/internal/images"
	"github.com/codesweep-ai/npmrevs/internal/paths"
	"github.com/codesweep-ai/npmrevs/internal/registry"
)

// DefaultListen is where serve listens unless told otherwise: npm's
// conventional local registry port, on loopback only.
const DefaultListen = "127.0.0.1:4873"

// DefaultUpstream is the registry every package without a local version comes
// from.
const DefaultUpstream = "https://registry.npmjs.org"

type serveOptions struct {
	data        []string
	listen      string
	printPort   bool
	upstream    string
	images      string
	imagesScope []string
	strict      []string
	cache       string
}

func serveCmd(a *app) *cobra.Command {
	o := &serveOptions{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the registry",
		Long: "Serve the npm registry read API on --listen. A package with a tarball in a\n" +
			"data directory, or an image in the --images registry, is served from there,\n" +
			"merged with its upstream versions. Every other package passes through from\n" +
			"--upstream. Point npm at it with registry=http://<listen>/ in an npmrc.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.serve(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&o.data, "data", nil, "a directory of .tgz files to serve; repeatable (default $CS_NPMREVS_DATA, else the XDG data directory)")
	f.StringVar(&o.listen, "listen", DefaultListen, "the address to listen on; port 0 picks a free one")
	f.BoolVar(&o.printPort, "print-port", false, "print the port on stdout, as one line, once listening")
	f.StringVar(&o.upstream, "upstream", DefaultUpstream, "the registry every other package comes from")
	f.StringVar(&o.images, "images", "", "a container registry holding per-version images, such as ghcr.io")
	f.StringArrayVar(&o.imagesScope, "images-scope", nil, "a scope, @name, whose packages are looked up in --images; repeatable")
	f.StringArrayVar(&o.strict, "strict", nil, "a scope, @name, served from local versions only; repeatable")
	f.StringVar(&o.cache, "cache", "", "where tarballs read out of images are kept (default $CS_NPMREVS_CACHE, else the XDG cache directory)")
	return cmd
}

func (a *app) serve(ctx context.Context, o *serveOptions) error {
	log := a.logger()
	up, err := url.Parse(o.upstream)
	if err != nil || (up.Scheme != "http" && up.Scheme != "https") || up.Host == "" || up.RawQuery != "" {
		return fmt.Errorf("--upstream %q is not an http or https registry URL", o.upstream)
	}
	for _, s := range append(append([]string{}, o.imagesScope...), o.strict...) {
		if !strings.HasPrefix(s, "@") || strings.Contains(s, "/") || len(s) < 2 {
			return fmt.Errorf("%q is not a scope: write it as @name", s)
		}
	}
	if len(o.imagesScope) > 0 && o.images == "" {
		return errors.New("--images-scope names scopes to look up in --images, and --images is not set")
	}
	if strings.Contains(o.images, "://") || strings.Contains(o.images, "/") {
		return fmt.Errorf("--images %q is a registry host, such as ghcr.io, not a URL", o.images)
	}
	if o.images != "" && len(o.imagesScope) == 0 {
		return errors.New("--images needs at least one --images-scope: only the scopes named are looked up there")
	}

	dirs := o.data
	if len(dirs) == 0 {
		d, err := paths.Data(a.getenv)
		if err != nil {
			return failed(err)
		}
		dirs = []string{d}
	}
	idx, err := datadir.Open(dirs, log)
	if err != nil {
		return failed(err)
	}

	cfg := registry.Config{
		Index:       idx,
		Upstream:    up,
		Strict:      o.strict,
		Version:     buildVersion(),
		UpstreamTTL: time.Minute,
		Log:         log,
	}
	if o.images != "" {
		cache := o.cache
		if cache == "" {
			if cache, err = paths.Cache(a.getenv); err != nil {
				return failed(err)
			}
		}
		cfg.Images = &images.Source{
			Client:   &images.Client{Getenv: a.getenv, UserAgent: "cs-npmrevs/" + buildVersion()},
			Registry: o.images,
			Scopes:   o.imagesScope,
			CacheDir: cache,
			TagsTTL:  time.Minute,
		}
	}

	ln, err := net.Listen("tcp", o.listen)
	if err != nil {
		return failed(err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return failed(fmt.Errorf("listening on %s gave no TCP address", o.listen))
	}
	if o.printPort {
		fmt.Fprintln(a.stdout, addr.Port)
	}
	names, versions := idx.Count()
	log.Info("serving", "url", "http://"+ln.Addr().String()+"/", "data", strings.Join(idx.Dirs(), ","),
		"packages", names, "versions", versions, "upstream", up.String())

	srv := &http.Server{Handler: registry.New(cfg), ReadHeaderTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return failed(err)
	case <-ctx.Done():
	}
	// A second Ctrl-C during the wait below ends the process at once.
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return failed(err)
	}
	log.Info("stopped")
	return nil
}
