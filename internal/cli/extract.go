package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/spf13/cobra"

	"github.com/codesweep-ai/npmrevs/internal/images"
	"github.com/codesweep-ai/npmrevs/internal/npmpkg"
	"github.com/codesweep-ai/npmrevs/internal/paths"
)

type extractOptions struct {
	data string
}

func extractCmd(a *app) *cobra.Command {
	o := &extractOptions{}
	cmd := &cobra.Command{
		Use:   "extract ARCHIVE|REFERENCE",
		Short: "Copy the npm tarballs out of an image",
		Long: "Copy every npm tarball an image holds into the data directory, and print the\n" +
			"path of each. The image is an archive file (an OCI archive, a docker save\n" +
			"archive, or an OCI layout directory) or a reference to pull it from.\n" +
			"A tarball is checked against the integrity the image declares, when it\n" +
			"declares one, and written into place only once it is whole.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.extract(cmd.Context(), args[0], o)
		},
	}
	cmd.Flags().StringVar(&o.data, "data", "", "the directory to write into (default $CS_NPMREVS_DATA, else the XDG data directory)")
	return cmd
}

func (a *app) dataDir(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	return paths.Data(a.getenv)
}

func (a *app) extract(ctx context.Context, target string, o *extractOptions) error {
	kind, err := classifyTarget(target)
	if err != nil {
		return err
	}
	if kind == targetTarball {
		return fmt.Errorf("%s is an npm tarball already; copy it into the data directory as it is", target)
	}
	dir, err := a.dataDir(o.data)
	if err != nil {
		return failed(err)
	}
	imgs, done, err := a.openImages(ctx, target, kind)
	if err != nil {
		return err
	}
	defer done()
	n := 0
	for _, img := range imgs {
		written, err := extractImage(img, dir, nil)
		if err != nil {
			return failed(fmt.Errorf("%s: %w", target, err))
		}
		for _, p := range written {
			fmt.Fprintln(a.stdout, p)
		}
		n += len(written)
	}
	if n == 0 {
		return notFound(fmt.Errorf("%s holds no npm tarball", target))
	}
	return nil
}

// extractImage writes the tarballs of one image into dir, each under the name
// npm pack gives it, and returns their paths.
//
// An image that declares its npm facts is trusted for exactly the tarball those
// facts describe: one whose integrity, name and version match is written, and
// nothing else is. An image that declares nothing, such as one made before
// images declared anything, has every tarball written, and two that claim one
// version with different bytes stop the run. want, when set, narrows what is
// written to one name and version.
func extractImage(img v1.Image, dir string, want *spec) ([]string, error) {
	md, err := images.ReadMetadata(img)
	if err != nil && !errors.Is(err, images.ErrNoMetadata) {
		return nil, err
	}
	var written []string
	seen := map[string]string{}
	err = images.ExtractTarballs(img, dir, func(p *npmpkg.Package) (string, error) {
		if md != nil && (p.Integrity != md.Integrity || p.Name != md.Name || p.Version != md.Version) {
			return "", nil
		}
		if want != nil && (p.Name != want.name || p.Version != want.version) {
			return "", nil
		}
		id := p.Name + "@" + p.Version
		if prev, ok := seen[id]; ok {
			if prev != p.Integrity {
				return "", fmt.Errorf("the image holds %s twice, with different bytes", id)
			}
			return "", nil
		}
		seen[id] = p.Integrity
		file := npmpkg.Filename(p.Name, p.Version)
		written = append(written, filepath.Join(dir, file))
		return file, nil
	})
	if err != nil {
		return nil, err
	}
	switch {
	case len(written) > 0:
		return written, nil
	case want != nil:
		return nil, fmt.Errorf("it holds no tarball of %s@%s", want.name, want.version)
	case md != nil:
		return nil, fmt.Errorf("it holds no tarball that matches the %s@%s it declares", md.Name, md.Version)
	}
	return nil, nil
}

type fetchOptions struct {
	data     string
	registry string
	latest   int
	noDeps   bool
}

func fetchCmd(a *app) *cobra.Command {
	o := &fetchOptions{}
	cmd := &cobra.Command{
		Use:   "fetch @SCOPE/NAME[@VERSION]...",
		Short: "Copy a package's versions out of their images, into the data directory",
		Long: "Copy one version of a package, or every version the registry holds images\n" +
			"of, into the data directory. Each version comes from its own image,\n" +
			"<registry>/<scope>/npm/<name>:<version>. The packages each version depends\n" +
			"on with an exact version in the same scope come too, such as a wrapper's\n" +
			"platform packages, unless --no-deps is given.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.fetch(cmd.Context(), args, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.data, "data", "", "the directory to write into (default $CS_NPMREVS_DATA, else the XDG data directory)")
	f.StringVar(&o.registry, "registry", images.DefaultRegistry, "the registry the images are in")
	f.IntVar(&o.latest, "latest", 0, "fetch only the newest N versions of a package named without a version (0: all)")
	f.BoolVar(&o.noDeps, "no-deps", false, "fetch only the packages named, not the ones they depend on")
	return cmd
}

// spec is one package named on the fetch command line.
type spec struct{ name, version string }

func parseSpec(arg string) (spec, error) {
	name, version := arg, ""
	if i := strings.LastIndex(arg, "@"); i > 0 {
		name, version = arg[:i], arg[i+1:]
	}
	if npmpkg.Scope(name) == "" || npmpkg.ValidName(name) != nil {
		return spec{}, fmt.Errorf("%q is not a scoped package name such as @scope/name or @scope/name@1.0.0", arg)
	}
	if version != "" && !npmpkg.ValidVersion(version) {
		return spec{}, fmt.Errorf("%q: %q is not a version", arg, version)
	}
	return spec{name: name, version: version}, nil
}

func (a *app) fetch(ctx context.Context, args []string, o *fetchOptions) error {
	if o.latest < 0 {
		return errors.New("--latest takes a count of 0 or more")
	}
	var specs []spec
	for _, arg := range args {
		s, err := parseSpec(arg)
		if err != nil {
			return err
		}
		specs = append(specs, s)
	}
	dir, err := a.dataDir(o.data)
	if err != nil {
		return failed(err)
	}
	c := &images.Client{Getenv: a.getenv, UserAgent: "cs-npmrevs/" + buildVersion()}
	f := &fetcher{a: a, c: c, registry: o.registry, dir: dir, deps: !o.noDeps, done: map[string]bool{}}

	for _, s := range specs {
		versions := []string{s.version}
		if s.version == "" {
			all, err := c.Versions(ctx, o.registry, s.name)
			if errors.Is(err, images.ErrNotFound) || (err == nil && len(all) == 0) {
				return notFound(fmt.Errorf("%s holds no image of %s", o.registry, s.name))
			}
			if err != nil {
				return failed(err)
			}
			if o.latest > 0 && len(all) > o.latest {
				all = all[len(all)-o.latest:]
			}
			versions = all
		}
		for _, v := range versions {
			if err := f.fetch(ctx, s.name, v, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// fetcher copies versions out of their images, and follows each one's exact
// dependencies within its scope.
type fetcher struct {
	a        *app
	c        *images.Client
	registry string
	dir      string
	deps     bool
	done     map[string]bool
}

func (f *fetcher) fetch(ctx context.Context, name, version string, named bool) error {
	key := name + "@" + version
	if f.done[key] {
		return nil
	}
	f.done[key] = true
	ref, err := images.Ref(f.registry, name, version)
	if err != nil {
		return failed(err)
	}
	img, err := f.c.Image(ctx, ref)
	if errors.Is(err, images.ErrNotFound) {
		if named {
			return notFound(err)
		}
		f.a.logger().Warn("a dependency has no image, so it is left to the registry", "package", key)
		return nil
	}
	if err != nil {
		return failed(err)
	}
	written, err := extractImage(img, f.dir, &spec{name: name, version: version})
	if err != nil {
		return failed(fmt.Errorf("%s: %w", ref, err))
	}
	for _, p := range written {
		fmt.Fprintln(f.a.stdout, p)
	}
	if !f.deps {
		return nil
	}
	for _, p := range written {
		pkg, err := npmpkg.Read(p)
		if err != nil {
			return failed(err)
		}
		for _, dep := range scopedExactDeps(pkg) {
			if err := f.fetch(ctx, dep.name, dep.version, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// scopedExactDeps returns the dependencies of pkg, of every kind an install
// resolves, that are in its own scope and pinned to one exact version.
func scopedExactDeps(pkg *npmpkg.Package) []spec {
	scope := npmpkg.Scope(pkg.Name)
	var out []spec
	for _, field := range []string{"dependencies", "optionalDependencies", "peerDependencies"} {
		deps := map[string]string{}
		if raw, ok := pkg.Manifest[field]; ok {
			_ = json.Unmarshal(raw, &deps) // a malformed block depends on nothing
		}
		for name, v := range deps {
			if npmpkg.Scope(name) == scope && npmpkg.ValidVersion(v) {
				out = append(out, spec{name: name, version: v})
			}
		}
	}
	slices.SortFunc(out, func(x, y spec) int { return strings.Compare(x.name+"@"+x.version, y.name+"@"+y.version) })
	return out
}
