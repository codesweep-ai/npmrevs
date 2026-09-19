// Package images carries npm package tarballs in container images: one image
// per package version, holding that version's tarball and nothing else.
//
// An image is data, not a program. It exists so a registry that serves
// container images, such as ghcr.io, can hold npm builds nobody wants on
// npmjs.com, with the retention, access control and garbage collection that
// registry already has. The image carries the npm facts a packument needs as
// manifest annotations, where a registry client reads them in one request, and
// again as config labels, which survive `docker save` and `podman save`.
package images

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/codesweep-ai/npmrevs/internal/npmpkg"
)

// The keys an image carries its npm facts under, as annotations and as labels.
const (
	KeyName      = "ai.codesweep.npm.name"
	KeyVersion   = "ai.codesweep.npm.version"
	KeyIntegrity = "ai.codesweep.npm.integrity"
	KeyShasum    = "ai.codesweep.npm.shasum"
	KeyManifest  = "ai.codesweep.npm.manifest"
)

// Epoch is the time every image and every file in it carries. A layer's digest
// covers the file's modification time, so a fixed one is what makes the same
// tarball produce the same image on every machine that builds it.
var Epoch = time.Date(1985, 10, 26, 8, 15, 0, 0, time.UTC)

// maxTarball bounds a tarball read out of a layer, so a hostile image cannot
// exhaust memory. npmjs.com's own limit is far below it.
const maxTarball = 512 << 20

// DefaultRegistry is where images are published and fetched unless a command
// is told otherwise.
const DefaultRegistry = "ghcr.io"

// tagPattern is what a container tag may be: no `+`, and at most 128
// characters. A version with build metadata cannot be one.
var tagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// Repository is the image repository that holds a scoped package's versions:
// "@scope/name" in registry is "<registry>/scope/npm/name".
func Repository(registry, name string) (string, error) {
	scope := npmpkg.Scope(name)
	if scope == "" {
		return "", fmt.Errorf("%q has no scope, and an image repository is named after the scope", name)
	}
	if err := npmpkg.ValidName(name); err != nil {
		return "", err
	}
	return strings.TrimSuffix(registry, "/") + "/" + strings.TrimPrefix(scope, "@") + "/npm/" + npmpkg.Bare(name), nil
}

// Ref is the image reference that holds name@version.
func Ref(registry, name, version string) (string, error) {
	repo, err := Repository(registry, name)
	if err != nil {
		return "", err
	}
	if !tagPattern.MatchString(version) {
		return "", fmt.Errorf("%s@%s: a container tag cannot hold that version", name, version)
	}
	return repo + ":" + version, nil
}

// Metadata is what an image says about the tarball it holds.
type Metadata struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Integrity string `json:"integrity"`
	Shasum    string `json:"shasum"`
	// Entry is the version object a packument lists, with no dist.tarball: the
	// registry that serves the tarball decides where it lives.
	Entry json.RawMessage `json:"entry,omitempty"`
}

// A BuildOption adjusts the image Build makes.
type BuildOption func(*buildConfig)

type buildConfig struct {
	revision string
}

// WithRevision records rev, the commit the tarball was built from, as the
// image's org.opencontainers.image.revision. It takes the place of the gitHead
// the package.json may name.
func WithRevision(rev string) BuildOption {
	return func(c *buildConfig) { c.revision = rev }
}

// revisionPattern is what names a commit: a hex object name of 7 to 64 digits.
var revisionPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// ValidRevision reports whether rev can name a commit.
func ValidRevision(rev string) error {
	if !revisionPattern.MatchString(rev) {
		return fmt.Errorf("%q does not name a commit: give its hex object name, 7 to 64 digits", rev)
	}
	return nil
}

// Build returns the image that carries one tarball.
func Build(pkg *npmpkg.Package, data []byte, opts ...BuildOption) (v1.Image, error) {
	var cfg buildConfig
	for _, o := range opts {
		o(&cfg)
	}
	layer, err := layerFor(npmpkg.Filename(pkg.Name, pkg.Version), data)
	if err != nil {
		return nil, err
	}
	img := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, types.OCIConfigJSON)
	if img, err = mutate.AppendLayers(img, layer); err != nil {
		return nil, err
	}

	keys, err := keysFor(pkg, cfg.revision)
	if err != nil {
		return nil, err
	}
	cf, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	cf = cf.DeepCopy()
	cf.OS, cf.Architecture = platformOf(pkg)
	cf.Created = v1.Time{Time: Epoch}
	// Nothing ever runs this image. A command is set because `podman create`
	// refuses an image without one, and creating a container is how a client
	// without cs-npmrevs copies the tarball out.
	cf.Config.Cmd = []string{"/noop"}
	cf.Config.Labels = keys
	if img, err = mutate.ConfigFile(img, cf); err != nil {
		return nil, err
	}
	withAnnotations, ok := mutate.Annotations(img, keys).(v1.Image)
	if !ok {
		return nil, errors.New("annotating the image produced something that is not an image")
	}
	return withAnnotations, nil
}

// npmOS and npmCPU name the OCI platform each value of npm's os and cpu fields
// means, as SPEC.md §9.3 lists them. The fields hold Node's process.platform
// and process.arch. A value missing here maps to nothing: sunos, for one, is
// either of two OCI systems, solaris and illumos.
var (
	npmOS = map[string]string{
		"aix": "aix", "android": "android", "darwin": "darwin", "freebsd": "freebsd",
		"linux": "linux", "netbsd": "netbsd", "openbsd": "openbsd", "win32": "windows",
	}
	npmCPU = map[string]string{
		"arm": "arm", "arm64": "arm64", "ia32": "386", "loong64": "loong64", "mips": "mips",
		"mipsel": "mipsle", "ppc64": "ppc64le", "riscv64": "riscv64", "s390x": "s390x", "x64": "amd64",
	}
)

// platformOf is the platform an image of pkg names: the one its os and cpu
// fields name, when each names exactly one value with an OCI name, and
// linux/amd64 otherwise. Nothing chooses an image by it. It is there because an
// image config must name one, and a registry shows it.
func platformOf(pkg *npmpkg.Package) (goos, arch string) {
	goos, okOS := npmOS[onlyValue(pkg.Manifest["os"])]
	arch, okCPU := npmCPU[onlyValue(pkg.Manifest["cpu"])]
	if !okOS || !okCPU {
		return "linux", "amd64"
	}
	// Node's ppc64 is little-endian everywhere but AIX.
	if goos == "aix" && arch == "ppc64le" {
		arch = "ppc64"
	}
	return goos, arch
}

// onlyValue is the one value a package.json os or cpu field names, as a string
// or a list of one, and "" when it names none, several, or one it excludes.
func onlyValue(raw json.RawMessage) string {
	var list []string
	var one string
	if json.Unmarshal(raw, &one) == nil {
		list = []string{one}
	} else if json.Unmarshal(raw, &list) != nil {
		return ""
	}
	if len(list) != 1 || strings.HasPrefix(list[0], "!") {
		return ""
	}
	return list[0]
}

// keysFor is the set of annotations and labels an image of pkg carries.
// revision is the commit the tarball was built from, or "" when the caller
// names none.
func keysFor(pkg *npmpkg.Package, revision string) (map[string]string, error) {
	entry, err := json.Marshal(pkg.Entry(""))
	if err != nil {
		return nil, err
	}
	keys := map[string]string{
		KeyName:      pkg.Name,
		KeyVersion:   pkg.Version,
		KeyIntegrity: pkg.Integrity,
		KeyShasum:    pkg.Shasum,
		KeyManifest:  string(entry),

		"org.opencontainers.image.title":       pkg.Name,
		"org.opencontainers.image.version":     pkg.Version,
		"org.opencontainers.image.description": "An npm package tarball, not a runnable image: " + pkg.Name + "@" + pkg.Version,
	}
	// The source label is what links the package on ghcr.io to its repository,
	// and so what gives it that repository's visibility.
	if src := sourceURL(pkg.Manifest["repository"]); src != "" {
		keys["org.opencontainers.image.source"] = src
	}
	// The commit the tarball was built from: the one the caller names, else the
	// gitHead npm records in package.json, when that names one.
	if revision == "" {
		revision = gitHead(pkg.Manifest["gitHead"])
	}
	if revision != "" {
		keys["org.opencontainers.image.revision"] = revision
	}
	return keys, nil
}

// gitHead is the commit a package.json gitHead field names, or "" when it
// names none.
func gitHead(raw json.RawMessage) string {
	var sha string
	if json.Unmarshal(raw, &sha) != nil || ValidRevision(sha) != nil {
		return ""
	}
	return sha
}

// sourceURL turns package.json's `repository` into the https URL of a GitHub
// repository, or "" when it names something else.
func sourceURL(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var url string
	if json.Unmarshal(raw, &url) != nil {
		var obj struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(raw, &obj) != nil {
			return ""
		}
		url = obj.URL
	}
	url = strings.TrimPrefix(url, "git+")
	url = strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git")
	// An SSH URL names a user before the host, as git@ or ssh://git@.
	url = strings.TrimPrefix(strings.TrimPrefix(url, "ssh://"), "git@")
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "git://github.com/", "github.com/", "github.com:", "github:"} {
		if rest, ok := strings.CutPrefix(url, prefix); ok {
			url = rest
			break
		}
	}
	if strings.Count(url, "/") != 1 || strings.ContainsAny(url, ": ") {
		return ""
	}
	return "https://github.com/" + url
}

// layerFor wraps one file as a layer that holds it at the image root.
func layerFor(filename string, data []byte) (v1.Layer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name:     filename,
		Mode:     0o644,
		Size:     int64(len(data)),
		ModTime:  Epoch,
		Typeflag: tar.TypeReg,
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write(data); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	return tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(b)), nil
	}, tarball.WithMediaType(types.OCILayer))
}

// ReadMetadata returns what an image says about its tarball: from the manifest
// annotations, or from the config labels when the annotations did not survive
// the way the image travelled.
func ReadMetadata(img v1.Image) (*Metadata, error) {
	m, err := img.Manifest()
	if err != nil {
		return nil, err
	}
	if md := metadataFrom(m.Annotations); md != nil {
		return md, nil
	}
	cf, err := img.ConfigFile()
	if err != nil {
		return nil, err
	}
	if md := metadataFrom(cf.Config.Labels); md != nil {
		return md, nil
	}
	return nil, ErrNoMetadata
}

// ErrNoMetadata marks an image that says nothing about an npm tarball. Its
// tarballs can still be read; nothing can be checked against them.
var ErrNoMetadata = errors.New("the image carries no npm metadata")

func metadataFrom(keys map[string]string) *Metadata {
	if keys[KeyName] == "" || keys[KeyVersion] == "" || keys[KeyIntegrity] == "" {
		return nil
	}
	md := &Metadata{
		Name:      keys[KeyName],
		Version:   keys[KeyVersion],
		Integrity: keys[KeyIntegrity],
		Shasum:    keys[KeyShasum],
	}
	// Only an object is a version object; `null` is valid JSON and is not one.
	if e := strings.TrimSpace(keys[KeyManifest]); strings.HasPrefix(e, "{") && json.Valid([]byte(e)) {
		md.Entry = json.RawMessage(e)
	}
	return md
}

// Limits on what one image may unpack, so a hostile image cannot fill memory
// or a disk: no more files than any real package set has, and no more bytes in
// total than a few of the largest packages npmjs.com accepts.
const (
	maxFiles      = 1000
	maxTotalBytes = 4 << 30
)

// ErrStop ends ExtractTarballs early, keeping what was kept so far.
var ErrStop = errors.New("stop")

// ExtractTarballs streams each npm tarball the image holds at its root into
// dir, one file at a time. It reads only regular files named *.tgz at the top
// level, so an image cannot make it write outside dir. choose sees each one
// parsed and returns the file name to keep it under, or "" to drop it. It may
// return ErrStop to end the walk. A kept file is renamed into place whole, so
// a reader of dir never finds half of one.
func ExtractTarballs(img v1.Image, dir string, choose func(*npmpkg.Package) (string, error)) error {
	layers, err := img.Layers()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var files int
	var total int64
	for _, l := range layers {
		rc, err := l.Uncompressed()
		if err != nil {
			return err
		}
		err = func() error {
			defer rc.Close()
			tr := tar.NewReader(rc)
			for {
				h, err := tr.Next()
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return fmt.Errorf("reading a layer: %w", err)
				}
				name := strings.TrimPrefix(strings.TrimPrefix(h.Name, "./"), "/")
				if h.Typeflag != tar.TypeReg || !SafeName(name) {
					continue
				}
				files++
				total += h.Size
				if files > maxFiles {
					return fmt.Errorf("the image holds more than %d tarballs", maxFiles)
				}
				if h.Size > maxTarball || total > maxTotalBytes {
					return fmt.Errorf("%s: the image holds more than it may unpack", name)
				}
				if err := extractOne(tr, h.Size, dir, choose); err != nil {
					return err
				}
			}
		}()
		if errors.Is(err, ErrStop) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// extractOne writes one tarball to a temporary file in dir, parses it, and
// keeps it under the name choose gives, or removes it.
func extractOne(r io.Reader, size int64, dir string, choose func(*npmpkg.Package) (string, error)) error {
	tmp, err := os.CreateTemp(dir, ".cs-npmrevs-*.tgz")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(r, size))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != size {
		return errors.New("a tarball in the image is shorter than its header says")
	}
	pkg, err := npmpkg.Read(tmp.Name())
	if err != nil {
		return err
	}
	keep, err := choose(pkg)
	if keep != "" {
		if cerr := os.Chmod(tmp.Name(), 0o644); cerr != nil {
			return cerr
		}
		if rerr := os.Rename(tmp.Name(), filepath.Join(dir, keep)); rerr != nil {
			return rerr
		}
	}
	return err
}

// SafeName reports whether name is a tarball name that can be written into a
// directory as it is: a single path element ending in .tgz, with no dot files
// and no whiteout markers.
func SafeName(name string) bool {
	return strings.HasSuffix(name, ".tgz") && !strings.ContainsAny(name, `/\`) &&
		!strings.HasPrefix(name, ".") && name != ".tgz"
}
