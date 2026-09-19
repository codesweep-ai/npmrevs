// Package npmpkg reads an npm package tarball: the manifest it carries, and the
// facts a registry records about the file itself.
//
// A registry never builds these facts from anything but the bytes it serves.
// The integrity npm checks is computed here from the same bytes, so a packument
// built from a Package cannot disagree with the tarball it points at.
package npmpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

// maxManifest bounds how much of a package.json is read. A real one is a few
// kilobytes; the bound only keeps a hostile tarball from exhausting memory.
const maxManifest = 8 << 20

// Package is one npm package tarball and what a registry says about it.
type Package struct {
	// Name and Version are read from the tarball's package.json, never from
	// the file name.
	Name    string
	Version string

	// Manifest is package.json as published, field by field.
	Manifest map[string]json.RawMessage

	// Integrity is "sha512-" and the base64 digest of the whole file; Shasum is
	// its sha1 in hex. npm checks the first, and older clients the second.
	Integrity string
	Shasum    string

	FileCount     int
	UnpackedSize  int64
	HasShrinkwrap bool
	HasGyp        bool

	// Size and ModTime describe the file; ModTime is what the packument
	// reports as the version's publish time.
	Size    int64
	ModTime time.Time

	// Path is where the tarball was read from, when it came from a file.
	Path string
}

// Read parses the tarball at path, following a symbolic link to the file it
// names. The file is read twice rather than held in memory: once to hash it,
// once to unpack it.
func Read(path string) (*Package, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	p, err := parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p.Path = path
	p.ModTime = info.ModTime()
	return p, nil
}

// Parse reads a tarball held in memory.
func Parse(data []byte) (*Package, error) {
	return parse(bytes.NewReader(data))
}

// parse hashes everything r holds, then rewinds it and unpacks it.
func parse(r io.ReadSeeker) (*Package, error) {
	s512, s1 := sha512.New(), sha1.New() // sha1 because the shasum field is defined as one
	size, err := io.Copy(io.MultiWriter(s512, s1), r)
	if err != nil {
		return nil, err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	p := &Package{
		Integrity: "sha512-" + base64.StdEncoding.EncodeToString(s512.Sum(nil)),
		Shasum:    hex.EncodeToString(s1.Sum(nil)),
		Size:      size,
	}

	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("not a gzip file: %w", err)
	}
	tr := tar.NewReader(gz)
	var raw []byte
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("not a tar archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		// npm drops the first path component whatever it is called, so a
		// tarball packed as "package/" and one packed as "node/" install alike.
		_, rel, ok := strings.Cut(strings.TrimPrefix(h.Name, "./"), "/")
		if !ok {
			continue
		}
		p.FileCount++
		p.UnpackedSize += h.Size
		switch rel {
		case "package.json":
			if raw, err = io.ReadAll(io.LimitReader(tr, maxManifest)); err != nil {
				return nil, fmt.Errorf("reading package.json: %w", err)
			}
		case "npm-shrinkwrap.json":
			p.HasShrinkwrap = true
		case "binding.gyp":
			p.HasGyp = true
		}
	}
	if raw == nil {
		return nil, errors.New("no package.json in the tarball")
	}
	if err := json.Unmarshal(raw, &p.Manifest); err != nil {
		return nil, fmt.Errorf("package.json: %w", err)
	}
	var id struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil, fmt.Errorf("package.json: %w", err)
	}
	if err := ValidName(id.Name); err != nil {
		return nil, err
	}
	if !ValidVersion(id.Version) {
		return nil, fmt.Errorf("package.json: %q is not a semver version", id.Version)
	}
	p.Name, p.Version = id.Name, id.Version
	return p, nil
}

// namePattern is npm's rule for a new package name: lower case, URL-safe, and
// at most one scope.
var namePattern = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)

// RoutableName reports whether name can be looked up on a registry. It is
// looser than ValidName: the public registry still serves packages published
// before names had to be lower case, such as JSONStream, and a request for one
// has to reach it.
func RoutableName(name string) bool {
	if name == "" || len(name) > 214 || strings.Contains(name, "..") || strings.ContainsAny(name, "\\ \t\r\n?#%") {
		return false
	}
	scope, bare, scoped := strings.Cut(name, "/")
	if !scoped {
		return !strings.HasPrefix(name, "@") && !strings.HasPrefix(name, ".")
	}
	return strings.HasPrefix(scope, "@") && len(scope) > 1 && bare != "" && !strings.Contains(bare, "/") && !strings.HasPrefix(bare, ".")
}

// ValidName reports whether name is a package name npm would publish.
func ValidName(name string) error {
	if name == "" {
		return errors.New("package.json has no name")
	}
	if len(name) > 214 || !namePattern.MatchString(name) {
		return fmt.Errorf("%q is not a valid npm package name", name)
	}
	return nil
}

// versionPattern is SemVer 2.0 in full: three numbers with no leading zeros, an
// optional prerelease and optional build metadata. npm refuses the shorthand
// "1.0" that Go's module versions accept.
var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)` +
	`(-(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?` +
	`(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// ValidVersion reports whether v is a semver version, as npm requires of every
// published one.
func ValidVersion(v string) bool {
	return versionPattern.MatchString(v)
}

// Scope returns "@scope" for a scoped name, and "" for a bare one.
func Scope(name string) string {
	if scope, _, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(scope, "@") {
		return scope
	}
	return ""
}

// Bare returns the name without its scope.
func Bare(name string) string {
	if _, bare, ok := strings.Cut(name, "/"); ok {
		return bare
	}
	return name
}

// Filename is the name `npm pack` gives the tarball of name@version:
// "@scope/name" becomes "scope-name-<version>.tgz".
func Filename(name, version string) string {
	return strings.TrimPrefix(strings.ReplaceAll(name, "/", "-"), "@") + "-" + version + ".tgz"
}

// TarballPath is the path a registry serves name@version's tarball at, relative
// to its root: "@scope/name/-/name-<version>.tgz".
func TarballPath(name, version string) string {
	return name + "/-/" + Bare(name) + "-" + version + ".tgz"
}

// HasInstallScript reports whether installing the package runs a script: one of
// the three install lifecycle scripts, or a binding.gyp npm builds with
// node-gyp when the package names no install script of its own.
func (p *Package) HasInstallScript() bool {
	var scripts map[string]string
	if raw, ok := p.Manifest["scripts"]; ok {
		_ = json.Unmarshal(raw, &scripts) // a malformed scripts block runs nothing
	}
	for _, s := range []string{"preinstall", "install", "postinstall"} {
		if scripts[s] != "" {
			return true
		}
	}
	return p.HasGyp
}

// Entry is the version object a packument lists for this package, with its
// tarball served from base (for example "http://127.0.0.1:4873").
func (p *Package) Entry(base string) map[string]any {
	e := make(map[string]any, len(p.Manifest)+4)
	for k, v := range p.Manifest {
		e[k] = v
	}
	if bin := normalizeBin(p.Name, p.Manifest["bin"]); bin != nil {
		e["bin"] = bin
	}
	e["_id"] = p.Name + "@" + p.Version
	e["_hasShrinkwrap"] = p.HasShrinkwrap
	if p.HasInstallScript() {
		e["hasInstallScript"] = true
	}
	dist := map[string]any{
		"integrity":    p.Integrity,
		"shasum":       p.Shasum,
		"fileCount":    p.FileCount,
		"unpackedSize": p.UnpackedSize,
	}
	if base != "" {
		dist["tarball"] = strings.TrimSuffix(base, "/") + "/" + TarballPath(p.Name, p.Version)
	}
	e["dist"] = dist
	return e
}

// normalizeBin turns a string `bin` into the map npm publish writes, keyed by
// the bare package name. It returns nil when there is nothing to change.
func normalizeBin(name string, raw json.RawMessage) map[string]string {
	var path string
	if len(raw) == 0 || json.Unmarshal(raw, &path) != nil || path == "" {
		return nil
	}
	return map[string]string{Bare(name): path}
}
