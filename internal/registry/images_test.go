package registry_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	ggcrregistry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/codesweep-ai/npmrevs/internal/datadir"
	"github.com/codesweep-ai/npmrevs/internal/images"
	"github.com/codesweep-ai/npmrevs/internal/npmpkg"
	"github.com/codesweep-ai/npmrevs/internal/registry"
	"github.com/codesweep-ai/npmrevs/internal/testpkg"
)

// pushImage pushes the image of manifest to host, changed by edit when given.
func pushImage(t *testing.T, host string, manifest map[string]any, edit func(v1.Image) v1.Image) {
	t.Helper()
	data := testpkg.Tarball(t, manifest, nil)
	p, err := npmpkg.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	img, err := images.Build(p, data)
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		img = edit(img)
	}
	ref, _ := images.Ref(host, p.Name, p.Version)
	r, _ := name.ParseReference(ref)
	if err := remote.Write(r, img); err != nil {
		t.Fatal(err)
	}
}

// An image version is served from the registry it lives in, and one whose
// version object is broken is left out rather than bringing the server down.
func TestImagesAreServedAndABrokenOneIsLeftOut(t *testing.T) {
	reg := httptest.NewServer(ggcrregistry.New())
	defer reg.Close()
	host := strings.TrimPrefix(reg.URL, "http://")
	pushImage(t, host, testpkg.Manifest("@acme/tool", "1.0.0"), nil)
	pushImage(t, host, testpkg.Manifest("@acme/tool", "1.1.0"), func(img v1.Image) v1.Image {
		broken, _ := mutate.Annotations(img, map[string]string{images.KeyManifest: "null"}).(v1.Image)
		return broken
	})

	up := newUpstream(t, nil)
	idx, err := datadir.Open([]string{t.TempDir()}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(up.URL)
	srv := httptest.NewServer(registry.New(registry.Config{
		Index: idx, Upstream: u, Version: "test", UpstreamTTL: time.Minute,
		Images: &images.Source{Client: &images.Client{}, Registry: host, Scopes: []string{"@acme"}, CacheDir: t.TempDir(), TagsTTL: time.Minute},
	}))
	defer srv.Close()

	resp, body := get(t, srv.URL+"/@acme%2ftool")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"1.0.0"`) || strings.Contains(string(body), `"1.1.0"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	resp, got := get(t, srv.URL+"/@acme/tool/-/tool-1.0.0.tgz")
	if resp.StatusCode != http.StatusOK || string(got) != string(testpkg.Tarball(t, testpkg.Manifest("@acme/tool", "1.0.0"), nil)) {
		t.Fatalf("tarball %d", resp.StatusCode)
	}
}

// A data directory's version takes the place of an image's (R24), and says so
// when its bytes are other than the image's.
func TestADataDirectoryVersionWithOtherBytesThanItsImageIsWarnedOf(t *testing.T) {
	reg := httptest.NewServer(ggcrregistry.New())
	defer reg.Close()
	host := strings.TrimPrefix(reg.URL, "http://")
	pushImage(t, host, testpkg.Manifest("@acme/tool", "1.0.0"), nil)
	dir := t.TempDir()
	testpkg.Write(t, dir, testpkg.Manifest("@acme/tool", "1.0.0"), map[string]string{"local.js": "1"})

	up := newUpstream(t, nil)
	idx, err := datadir.Open([]string{dir}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	u, _ := url.Parse(up.URL)
	srv := httptest.NewServer(registry.New(registry.Config{
		Index: idx, Upstream: u, Version: "test", UpstreamTTL: time.Minute,
		Images: &images.Source{Client: &images.Client{}, Registry: host, Scopes: []string{"@acme"}, CacheDir: t.TempDir(), TagsTTL: time.Minute},
		Log:    slog.New(slog.NewTextHandler(logs, nil)),
	}))
	defer srv.Close()

	if resp, body := get(t, srv.URL+"/@acme%2ftool"); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if !strings.Contains(logs.String(), "a local version takes the place of a published one with other bytes") ||
		!strings.Contains(logs.String(), `published="its image"`) {
		t.Fatalf("no warning naming the image:\n%s", logs)
	}
}
