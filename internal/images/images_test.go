package images_test

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	ggcrregistry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/codesweep-ai/npmrevs/internal/images"
	"github.com/codesweep-ai/npmrevs/internal/npmpkg"
	"github.com/codesweep-ai/npmrevs/internal/testpkg"
)

func pkgOf(t *testing.T, manifest map[string]any) (*npmpkg.Package, []byte) {
	t.Helper()
	data := testpkg.Tarball(t, manifest, map[string]string{"index.js": "1"})
	p, err := npmpkg.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return p, data
}

func TestRefNamesTheRepositoryAfterTheScope(t *testing.T) {
	ref, err := images.Ref("ghcr.io", "@codesweep-ai/lint-linux-x64", "0.0.0-20260918041056-8bdc0656c767")
	if err != nil || ref != "ghcr.io/codesweep-ai/npm/lint-linux-x64:0.0.0-20260918041056-8bdc0656c767" {
		t.Fatalf("%q %v", ref, err)
	}
	if _, err := images.Ref("ghcr.io", "unscoped", "1.0.0"); err == nil {
		t.Error("an unscoped name got a repository")
	}
	if _, err := images.Ref("ghcr.io", "@a/b", "1.0.0+build.1"); err == nil {
		t.Error("a version with build metadata became a tag")
	}
}

// The same tarball has to give the same image on every machine, or a second
// push of an unchanged build would read as a different image.
func TestBuildIsReproducible(t *testing.T) {
	p, data := pkgOf(t, testpkg.Manifest("@acme/tool", "1.0.0"))
	a, err := images.Build(p, data)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	b, err := images.Build(p, data)
	if err != nil {
		t.Fatal(err)
	}
	da, _ := a.Digest()
	db, _ := b.Digest()
	if da != db {
		t.Fatalf("digests differ: %s %s", da, db)
	}
}

func TestBuildCarriesTheMetadataTwice(t *testing.T) {
	m := testpkg.Manifest("@acme/tool", "1.0.0", "repository", map[string]string{"type": "git", "url": "git+https://github.com/acme/tool.git"})
	p, data := pkgOf(t, m)
	img, err := images.Build(p, data)
	if err != nil {
		t.Fatal(err)
	}
	man, _ := img.Manifest()
	cf, _ := img.ConfigFile()
	for _, keys := range []map[string]string{man.Annotations, cf.Config.Labels} {
		if keys[images.KeyIntegrity] != p.Integrity || keys[images.KeyName] != "@acme/tool" {
			t.Fatalf("keys %v", keys)
		}
		if keys["org.opencontainers.image.source"] != "https://github.com/acme/tool" {
			t.Errorf("source %q", keys["org.opencontainers.image.source"])
		}
	}
	if cf.OS != "linux" || cf.Architecture != "amd64" || len(cf.Config.Cmd) != 1 {
		t.Errorf("config %+v", cf)
	}
	md, err := images.ReadMetadata(img)
	if err != nil || md.Version != "1.0.0" || md.Shasum != p.Shasum || len(md.Entry) == 0 {
		t.Fatalf("metadata %+v %v", md, err)
	}
	assertHolds(t, img, data)
}

// assertHolds extracts img's tarballs and checks it holds exactly data.
func assertHolds(t *testing.T, img v1.Image, data []byte) {
	t.Helper()
	dir := t.TempDir()
	var names []string
	err := images.ExtractTarballs(img, dir, func(p *npmpkg.Package) (string, error) {
		name := npmpkg.Filename(p.Name, p.Version)
		names = append(names, name)
		return name, nil
	})
	if err != nil || len(names) != 1 {
		t.Fatalf("tarballs %v %v", names, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, names[0]))
	if err != nil || string(got) != string(data) {
		t.Fatalf("extracted %d bytes, want %d: %v", len(got), len(data), err)
	}
	info, _ := os.Stat(filepath.Join(dir, names[0]))
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", info.Mode())
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Errorf("temporary files left behind: %v", left)
	}
}

// An OCI archive keeps the annotations; a docker archive keeps only the
// labels. Either way the metadata reads back.
func TestArchivesRoundTrip(t *testing.T) {
	p, data := pkgOf(t, testpkg.Manifest("@acme/tool", "2.0.0"))
	img, err := images.Build(p, data)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, f := range []images.Format{images.OCI, images.Docker} {
		path := filepath.Join(dir, string(f)+".tar")
		if err := images.WriteArchive(path, img, "ghcr.io/acme/npm/tool:2.0.0", f); err != nil {
			t.Fatal(err)
		}
		ar, err := images.OpenArchive(path)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if len(ar.Images) != 1 {
			t.Fatalf("%s: %d images", f, len(ar.Images))
		}
		md, err := images.ReadMetadata(ar.Images[0])
		if err != nil || md.Integrity != p.Integrity {
			t.Fatalf("%s: %+v %v", f, md, err)
		}
		man, _ := ar.Images[0].Manifest()
		if keeps := man.Annotations[images.KeyIntegrity] != ""; keeps != (f == images.OCI) {
			t.Errorf("%s: manifest annotations kept = %v", f, keeps)
		}
		assertHolds(t, ar.Images[0], data)
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o644 {
			t.Errorf("%s: archive mode %v", f, info.Mode())
		}
		ar.Close()
	}

	// The OCI archive is itself reproducible.
	again := filepath.Join(dir, "again.tar")
	if err := images.WriteArchive(again, img, "ghcr.io/acme/npm/tool:2.0.0", images.OCI); err != nil {
		t.Fatal(err)
	}
	x, _ := os.ReadFile(filepath.Join(dir, "oci.tar"))
	y, _ := os.ReadFile(again)
	if string(x) != string(y) {
		t.Fatal("two OCI archives of one image differ")
	}
}

func TestOpenArchiveRefusesWhatIsNotOne(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.tar")
	f, err := os.Create(plain)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	_ = tw.WriteHeader(&tar.Header{Name: "readme", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.Close()
	_ = f.Close()
	if _, err := images.OpenArchive(plain); err == nil || !strings.Contains(err.Error(), "neither") {
		t.Fatalf("err %v", err)
	}
	if _, err := images.ParseFormat("zip"); err == nil {
		t.Fatal("zip is a format")
	}
}

func TestSafeName(t *testing.T) {
	for n, want := range map[string]bool{"a-1.0.0.tgz": true, "../a.tgz": false, "dir/a.tgz": false, ".wh.a.tgz": false, "a.tar": false, ".tgz": false} {
		if images.SafeName(n) != want {
			t.Errorf("SafeName(%q) != %v", n, want)
		}
	}
}

// push builds pkg's image and pushes it to the registry at host.
func push(t *testing.T, host string, manifest map[string]any) *npmpkg.Package {
	t.Helper()
	p, data := pkgOf(t, manifest)
	img, err := images.Build(p, data)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := images.Ref(host, p.Name, p.Version)
	if err != nil {
		t.Fatal(err)
	}
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(r, img); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClientAndSourceReadAPushedImage(t *testing.T) {
	srv := httptest.NewServer(ggcrregistry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	p1 := push(t, host, testpkg.Manifest("@acme/tool", "1.0.0"))
	push(t, host, testpkg.Manifest("@acme/tool", "1.1.0-dev.1"))

	ctx := context.Background()
	c := &images.Client{}
	vs, err := c.Versions(ctx, host, "@acme/tool")
	if err != nil || len(vs) != 2 || vs[0] != "1.0.0" {
		t.Fatalf("versions %v %v", vs, err)
	}
	if _, err := c.Versions(ctx, host, "@acme/absent"); !errors.Is(err, images.ErrNotFound) {
		t.Fatalf("absent repository: %v", err)
	}
	if _, err := c.Image(ctx, host+"/acme/npm/tool:9.9.9"); !errors.Is(err, images.ErrNotFound) {
		t.Fatalf("absent tag: %v", err)
	}

	src := &images.Source{Client: c, Registry: host, Scopes: []string{"@acme"}, CacheDir: t.TempDir(), TagsTTL: time.Minute}
	if src.Covers("@other/x") || !src.Covers("@acme/tool") {
		t.Fatal("Covers")
	}
	metas, err := src.Versions(ctx, "@acme/tool")
	if err != nil || len(metas) != 2 || metas["1.0.0"].Integrity != p1.Integrity {
		t.Fatalf("metas %v %v", metas, err)
	}
	if metas, err := src.Versions(ctx, "@other/x"); err != nil || metas != nil {
		t.Fatalf("an uncovered scope was looked up: %v %v", metas, err)
	}
	path, err := src.Tarball(ctx, "@acme/tool", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	got, err := npmpkg.Read(path)
	if err != nil || got.Integrity != p1.Integrity {
		t.Fatalf("cached tarball %v %v", got, err)
	}
	// Read again from the cache, with the registry gone.
	srv.Close()
	if again, err := src.Tarball(ctx, "@acme/tool", "1.0.0"); err != nil || again != path {
		t.Fatalf("cache: %q %v", again, err)
	}
}

// ghcr.io links a package to the repository its source label names, so every
// way package.json writes a GitHub repository has to come out the same.
func TestSourceLabelNormalisesGitHubURLs(t *testing.T) {
	for _, repo := range []any{
		"github:acme/tool",
		"git@github.com:acme/tool.git",
		"ssh://git@github.com/acme/tool.git",
		map[string]string{"type": "git", "url": "git+https://github.com/acme/tool.git"},
		"https://github.com/acme/tool/",
	} {
		p, data := pkgOf(t, testpkg.Manifest("@acme/tool", "1.0.0", "repository", repo))
		img, err := images.Build(p, data)
		if err != nil {
			t.Fatal(err)
		}
		man, _ := img.Manifest()
		if got := man.Annotations["org.opencontainers.image.source"]; got != "https://github.com/acme/tool" {
			t.Errorf("%v: source %q", repo, got)
		}
	}
	p, data := pkgOf(t, testpkg.Manifest("@acme/tool", "1.0.0", "repository", "https://gitlab.com/acme/tool"))
	img, _ := images.Build(p, data)
	if man, _ := img.Manifest(); man.Annotations["org.opencontainers.image.source"] != "" {
		t.Error("a repository outside GitHub got a source label")
	}
}

// A version object that is not an object carries no entry, rather than one a
// server would trip over.
func TestMetadataRefusesAManifestThatIsNotAnObject(t *testing.T) {
	p, data := pkgOf(t, testpkg.Manifest("@acme/tool", "1.0.0"))
	img, err := images.Build(p, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"null", "[1]", "not json"} {
		broken, _ := mutate.Annotations(img, map[string]string{images.KeyManifest: bad}).(v1.Image)
		md, err := images.ReadMetadata(broken)
		if err != nil || md.Entry != nil {
			t.Errorf("%s: entry %s, err %v", bad, md.Entry, err)
		}
	}
}

// A layer of a thousand and one small, valid tarballs stops the extraction at
// the limit: the count is what bounds a hostile image, whatever each file holds.
func TestExtractTarballsIsBounded(t *testing.T) {
	var buf strings.Builder
	tw := tar.NewWriter(&buf)
	for i := range 1001 {
		data := testpkg.Tarball(t, testpkg.Manifest("p"+strconv.Itoa(i), "1.0.0"), nil)
		_ = tw.WriteHeader(&tar.Header{Name: "p" + strconv.Itoa(i) + "-1.0.0.tgz", Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(data)
	}
	_ = tw.Close()
	b := buf.String()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(b)), nil })
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	err = images.ExtractTarballs(img, t.TempDir(), func(*npmpkg.Package) (string, error) { seen++; return "", nil })
	if err == nil || !strings.Contains(err.Error(), "more than 1000") || seen != 1000 {
		t.Fatalf("seen %d, err %v", seen, err)
	}
}
