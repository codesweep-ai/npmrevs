package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/spf13/cobra"

	"github.com/codesweep-ai/npmrevs/internal/images"
	"github.com/codesweep-ai/npmrevs/internal/npmpkg"
)

func imageCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Build an image from a tarball, or say what an image holds",
		Long: "An image carries one version of one npm package: a single layer with the\n" +
			"tarball at its root, and the facts a registry needs about it as annotations\n" +
			"and labels.",
		Args: cobra.NoArgs,
		RunE: needsSubcommand,
	}
	cmd.AddCommand(imageBuildCmd(a), imageInspectCmd(a))
	return cmd
}

// needsSubcommand is the action of a command that only groups others. Without
// one, cobra prints the help and exits 0 for a subcommand spelled wrongly, and
// a script that mistyped a check would read it as passing.
func needsSubcommand(cmd *cobra.Command, _ []string) error {
	var names []string
	for _, c := range cmd.Commands() {
		if c.IsAvailableCommand() {
			names = append(names, c.Name())
		}
	}
	return fmt.Errorf("%s needs a subcommand: %s", cmd.CommandPath(), strings.Join(names, ", "))
}

type buildOptions struct {
	output   string
	registry string
	format   string
}

func imageBuildCmd(a *app) *cobra.Command {
	o := &buildOptions{}
	cmd := &cobra.Command{
		Use:   "build FILE.tgz",
		Short: "Build the image of one npm tarball, as an archive file",
		Long: "Build the image that carries one npm tarball, and write it as an archive\n" +
			"file. It prints the image reference the archive is named for, which is where\n" +
			"a push should send it. Nothing is pushed, and no container engine is used.\n" +
			"The same tarball always gives the same image, byte for byte.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return a.imageBuild(args[0], o)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&o.output, "output", "o", "", "the archive to write (default: FILE with .tgz replaced by .image.tar)")
	f.StringVar(&o.registry, "registry", images.DefaultRegistry, "the registry the image reference names")
	f.StringVar(&o.format, "format", string(images.OCI), "the archive format: oci, or docker")
	return cmd
}

func (a *app) imageBuild(file string, o *buildOptions) error {
	format, err := images.ParseFormat(o.format)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return failed(err)
	}
	pkg, err := npmpkg.Parse(data)
	if err != nil {
		return failed(fmt.Errorf("%s: %w", file, err))
	}
	ref, err := images.Ref(o.registry, pkg.Name, pkg.Version)
	if err != nil {
		return failed(err)
	}
	img, err := images.Build(pkg, data)
	if err != nil {
		return failed(err)
	}
	out := o.output
	if out == "" {
		out = strings.TrimSuffix(file, ".tgz") + ".image.tar"
	}
	if err := images.WriteArchive(out, img, ref, format); err != nil {
		return failed(err)
	}
	fmt.Fprintln(a.stdout, ref)
	a.logger().Info("wrote the image", "archive", out, "package", pkg.Name+"@"+pkg.Version)
	return nil
}

type inspectOptions struct {
	registry string
	json     bool
}

func imageInspectCmd(a *app) *cobra.Command {
	o := &inspectOptions{}
	cmd := &cobra.Command{
		Use:   "inspect FILE.tgz|ARCHIVE|REFERENCE",
		Short: "Print the package an npm tarball or an image holds",
		Long: "Print the name, version, integrity and shasum of an npm tarball, of each\n" +
			"image in an archive, or of an image in a registry, with the image reference\n" +
			"each one is published under. An image is read from its manifest and config\n" +
			"only; no layer is downloaded.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.imageInspect(cmd.Context(), args[0], o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.registry, "registry", images.DefaultRegistry, "the registry the printed image reference names")
	f.BoolVar(&o.json, "json", false, "print a JSON array, one object per package")
	return cmd
}

// inspected is one package as `image inspect` reports it.
type inspected struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Integrity string `json:"integrity"`
	Shasum    string `json:"shasum"`
	Image     string `json:"image,omitempty"`
}

func (a *app) imageInspect(ctx context.Context, target string, o *inspectOptions) error {
	var found []inspected
	kind, err := classifyTarget(target)
	switch {
	case err != nil:
		return err
	case kind == targetTarball:
		pkg, err := npmpkg.Read(target)
		if err != nil {
			return failed(err)
		}
		found = append(found, inspected{Name: pkg.Name, Version: pkg.Version, Integrity: pkg.Integrity, Shasum: pkg.Shasum})
	default:
		imgs, done, err := a.openImages(ctx, target, kind)
		if err != nil {
			return err
		}
		defer done()
		for _, img := range imgs {
			md, err := images.ReadMetadata(img)
			if err != nil {
				return failed(fmt.Errorf("%s: %w", target, err))
			}
			found = append(found, inspected{Name: md.Name, Version: md.Version, Integrity: md.Integrity, Shasum: md.Shasum})
		}
	}
	for i := range found {
		// An image read from a registry is published where it was read from.
		if kind == targetReference {
			found[i].Image = target
			continue
		}
		if ref, err := images.Ref(o.registry, found[i].Name, found[i].Version); err == nil {
			found[i].Image = ref
		}
	}
	return printInspected(a.stdout, found, o.json)
}

func printInspected(w io.Writer, found []inspected, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(found)
	}
	for i, p := range found {
		if i > 0 {
			fmt.Fprintln(w)
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "name\t%s\n", p.Name)
		fmt.Fprintf(tw, "version\t%s\n", p.Version)
		fmt.Fprintf(tw, "integrity\t%s\n", p.Integrity)
		fmt.Fprintf(tw, "shasum\t%s\n", p.Shasum)
		if p.Image != "" {
			fmt.Fprintf(tw, "image\t%s\n", p.Image)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return nil
}

type targetKind int

const (
	targetTarball targetKind = iota
	targetArchive
	targetReference
)

// classifyTarget tells a file on disk from an image reference. A path that
// exists is a file. Anything else has to read as a reference that names its
// registry: a first element with a dot or a port in it, or localhost. A
// relative path that is missing is then reported as missing rather than looked
// up on a registry.
func classifyTarget(target string) (targetKind, error) {
	if info, err := os.Stat(target); err == nil {
		if info.IsDir() || !isGzip(target) {
			return targetArchive, nil
		}
		if _, err := npmpkg.Read(target); err != nil {
			return 0, fmt.Errorf("%s is compressed and is not an npm tarball; decompress an image archive before reading it", target)
		}
		return targetTarball, nil
	}
	host, rest, ok := strings.Cut(target, "/")
	if ok && rest != "" && !filepath.IsAbs(target) && (strings.ContainsAny(host, ".:") || host == "localhost") &&
		(strings.Contains(rest, ":") || strings.Contains(rest, "@")) {
		return targetReference, nil
	}
	return 0, fmt.Errorf("%s: no such file, and not an image reference such as ghcr.io/owner/npm/name:1.0.0", target)
}

func isGzip(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	magic := make([]byte, 2)
	_, err = io.ReadFull(f, magic)
	return err == nil && magic[0] == 0x1f && magic[1] == 0x8b
}

// openImages returns the images an archive holds, or the one a reference
// names, with the function that releases them.
func (a *app) openImages(ctx context.Context, target string, kind targetKind) ([]v1.Image, func(), error) {
	if kind == targetArchive {
		ar, err := images.OpenArchive(target)
		if err != nil {
			return nil, nil, failed(err)
		}
		return ar.Images, ar.Close, nil
	}
	c := &images.Client{Getenv: a.getenv, UserAgent: "cs-npmrevs/" + buildVersion()}
	img, err := c.Image(ctx, target)
	if errors.Is(err, images.ErrNotFound) {
		return nil, nil, notFound(err)
	}
	if err != nil {
		return nil, nil, failed(err)
	}
	return []v1.Image{img}, func() {}, nil
}
