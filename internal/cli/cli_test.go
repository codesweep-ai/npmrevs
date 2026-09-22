package cli

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	ggcrregistry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/codesweep-ai/npmrevs"
	"github.com/codesweep-ai/npmrevs/internal/images"
	"github.com/codesweep-ai/npmrevs/internal/npmpkg"
	"github.com/codesweep-ai/npmrevs/internal/testpkg"
)

// result is one run of the command tree.
type result struct {
	code           int
	stdout, stderr string
}

func cs(t *testing.T, env map[string]string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, &out, &errb, func(k string) string { return env[k] })
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

func TestVersionAndManual(t *testing.T) {
	r := cs(t, nil, "version")
	if r.code != exitOK || !regexp.MustCompile(`^cs-npmrevs \S+ \(\w+/\w+, go`).MatchString(r.stdout) {
		t.Fatalf("%+v", r)
	}
	r = cs(t, nil, "manual")
	if r.code != exitOK || r.stdout != npmrevs.ManualMD {
		t.Fatalf("manual printed something other than MANUAL.md: %+v", r.code)
	}
}

func TestUsageMistakesExitTwo(t *testing.T) {
	cases := [][]string{
		{"no-such-command"},
		{"version", "extra"},
		{"serve", "--images-scope", "@acme"},
		{"serve", "--images", "ghcr.io"},
		{"serve", "--strict", "acme"},
		{"serve", "--upstream", "ftp://x"},
		{"image", "build", "x.tgz", "--format", "zip"},
		{"image", "build", "x.tgz", "--revision", "HEAD"},
		{"image", "inspect", "no-such-file-and-not-a-ref"},
		{"fetch", "unscoped"},
		{"fetch", "@a/b@v1"},
		{"fetch", "@a/b", "--latest", "-1"},
		{"lockfile", "rewrite", "--to", "not a url"},
		{"-v", "-q", "version"},
		// A parent spelled right with a subcommand spelled wrong, which a
		// script must not read as a pass.
		{"lockfile", "chekc"},
		{"image", "biuld", "x.tgz"},
		{"image"},
		{"serve", "--images", "https://ghcr.io", "--images-scope", "@acme"},
		// A missing relative path is missing, not a reference to look up.
		{"image", "inspect", "build/out:1.tar"},
	}
	for _, args := range cases {
		if r := cs(t, nil, args...); r.code != exitBadUsage {
			t.Errorf("%v: exit %d, want %d (%s)", args, r.code, exitBadUsage, r.stderr)
		}
	}
}

func TestImageBuildInspectExtract(t *testing.T) {
	dir := t.TempDir()
	tgz := testpkg.Write(t, dir, testpkg.Manifest("@acme/tool", "1.0.0"), map[string]string{"a.js": "1"})
	want, _ := os.ReadFile(tgz)

	r := cs(t, nil, "image", "build", tgz)
	if r.code != exitOK || strings.TrimSpace(r.stdout) != "ghcr.io/acme/npm/tool:1.0.0" {
		t.Fatalf("%+v", r)
	}
	archive := strings.TrimSuffix(tgz, ".tgz") + ".image.tar"
	if _, err := os.Stat(archive); err != nil {
		t.Fatal(err)
	}
	docker := filepath.Join(dir, "docker.tar")
	if r := cs(t, nil, "image", "build", tgz, "-o", docker, "--format", "docker", "--registry", "registry.example.org"); r.code != exitOK ||
		strings.TrimSpace(r.stdout) != "registry.example.org/acme/npm/tool:1.0.0" {
		t.Fatalf("%+v", r)
	}

	for _, target := range []string{tgz, archive, docker} {
		r := cs(t, nil, "image", "inspect", target, "--json")
		var got []inspected
		if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || r.code != exitOK || len(got) != 1 {
			t.Fatalf("%s: %+v %v", target, r, err)
		}
		if got[0].Name != "@acme/tool" || got[0].Image != "ghcr.io/acme/npm/tool:1.0.0" {
			t.Fatalf("%s: %+v", target, got[0])
		}
	}
	if r := cs(t, nil, "image", "inspect", tgz); !strings.Contains(r.stdout, "integrity") {
		t.Fatalf("%+v", r)
	}

	data := filepath.Join(dir, "data")
	r = cs(t, nil, "extract", archive, "--data", data)
	if r.code != exitOK {
		t.Fatalf("%+v", r)
	}
	got, err := os.ReadFile(strings.TrimSpace(r.stdout))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("extracted %d bytes, err %v", len(got), err)
	}
	// With no --data, the environment names the directory.
	envData := filepath.Join(dir, "env-data")
	if r := cs(t, map[string]string{"CS_NPMREVS_DATA": envData}, "extract", docker); r.code != exitOK ||
		!strings.HasPrefix(r.stdout, envData) {
		t.Fatalf("%+v", r)
	}
	if r := cs(t, nil, "extract", tgz, "--data", data); r.code != exitBadUsage {
		t.Fatalf("extracting a tarball: %+v", r)
	}
	if r := cs(t, nil, "image", "build", filepath.Join(dir, "missing.tgz")); r.code != exitFailed {
		t.Fatalf("a missing file: %+v", r)
	}
}

// registryWith pushes the image of each manifest to an in-process registry and
// returns its host.
func registryWith(t *testing.T, manifests ...map[string]any) string {
	t.Helper()
	srv := httptest.NewServer(ggcrregistry.New())
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	for _, m := range manifests {
		data := testpkg.Tarball(t, m, nil)
		p, err := npmpkg.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		img, err := images.Build(p, data)
		if err != nil {
			t.Fatal(err)
		}
		ref, _ := images.Ref(host, p.Name, p.Version)
		r, _ := name.ParseReference(ref)
		if err := remote.Write(r, img); err != nil {
			t.Fatal(err)
		}
	}
	return host
}

func TestFetchFollowsExactDependenciesInTheScope(t *testing.T) {
	host := registryWith(t,
		testpkg.Manifest("@acme/tool", "1.0.0", "optionalDependencies", map[string]string{"@acme/tool-linux-x64": "1.0.0", "left-pad": "1.3.0"}),
		testpkg.Manifest("@acme/tool", "1.1.0", "optionalDependencies", map[string]string{"@acme/tool-linux-x64": "1.1.0"}),
		testpkg.Manifest("@acme/tool-linux-x64", "1.0.0"),
		testpkg.Manifest("@acme/tool-linux-x64", "1.1.0"),
	)
	data := t.TempDir()
	r := cs(t, nil, "fetch", "@acme/tool@1.0.0", "--registry", host, "--data", data)
	if r.code != exitOK || strings.Count(r.stdout, "\n") != 2 {
		t.Fatalf("%+v", r)
	}
	for _, f := range []string{"acme-tool-1.0.0.tgz", "acme-tool-linux-x64-1.0.0.tgz"} {
		if _, err := os.Stat(filepath.Join(data, f)); err != nil {
			t.Fatal(err)
		}
	}

	only := t.TempDir()
	if r := cs(t, nil, "fetch", "@acme/tool", "--registry", host, "--data", only, "--no-deps"); r.code != exitOK || strings.Count(r.stdout, "\n") != 2 {
		t.Fatalf("every version, no deps: %+v", r)
	}
	newest := t.TempDir()
	if r := cs(t, nil, "fetch", "@acme/tool", "--registry", host, "--data", newest, "--latest", "1"); r.code != exitOK ||
		!strings.Contains(r.stdout, "acme-tool-1.1.0.tgz") || strings.Contains(r.stdout, "acme-tool-1.0.0.tgz") {
		t.Fatalf("latest 1: %+v", r)
	}

	if r := cs(t, nil, "fetch", "@acme/absent", "--registry", host, "--data", data); r.code != exitNotFound {
		t.Fatalf("an absent package: %+v", r)
	}
	if r := cs(t, nil, "fetch", "@acme/tool@9.9.9", "--registry", host, "--data", data); r.code != exitNotFound {
		t.Fatalf("an absent version: %+v", r)
	}
	if r := cs(t, nil, "image", "inspect", host+"/acme/npm/tool:9.9.9"); r.code != exitNotFound {
		t.Fatalf("inspecting an absent image: %+v", r)
	}
	if r := cs(t, nil, "extract", host+"/acme/npm/tool:1.1.0", "--data", t.TempDir()); r.code != exitOK {
		t.Fatalf("extracting from a registry: %+v", r)
	}
	// Inspected in a registry, an image reports the reference it was read from,
	// not the one --registry would name.
	if r := cs(t, nil, "image", "inspect", host+"/acme/npm/tool:1.1.0", "--json"); r.code != exitOK ||
		!strings.Contains(r.stdout, `"image": "`+host+`/acme/npm/tool:1.1.0"`) {
		t.Fatalf("inspecting from a registry: %+v", r)
	}
}

const loopbackLock = `{
  "lockfileVersion": 3,
  "packages": {
    "": {},
    "node_modules/@acme/tool": {
      "version": "1.0.0",
      "resolved": "http://127.0.0.1:4875/@acme/tool/-/tool-1.0.0.tgz",
      "integrity": "sha512-local"
    }
  }
}
`

func TestLockfileCheckAndRewrite(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "package-lock.json")
	if err := os.WriteFile(lock, []byte(loopbackLock), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := cs(t, nil, "lockfile", "check", lock); r.code != exitFailed || !strings.Contains(r.stdout, "@acme/tool@1.0.0") {
		t.Fatalf("%+v", r)
	}

	hasIt := true
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hasIt {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"versions":{"1.0.0":{"dist":{"tarball":"https://registry.example.org/t.tgz","integrity":"sha512-local"}}}}`)
	}))
	defer target.Close()

	if r := cs(t, nil, "lockfile", "rewrite", lock, "--to", target.URL, "--dry-run"); r.code != exitOK {
		t.Fatalf("%+v", r)
	}
	if b, _ := os.ReadFile(lock); string(b) != loopbackLock {
		t.Fatal("a dry run wrote the file")
	}
	hasIt = false
	if r := cs(t, nil, "lockfile", "rewrite", lock, "--to", target.URL); r.code != exitFailed || !strings.Contains(r.stdout, "missing") {
		t.Fatalf("%+v", r)
	}
	hasIt = true
	if r := cs(t, nil, "lockfile", "rewrite", lock, "--to", target.URL); r.code != exitOK {
		t.Fatalf("%+v", r)
	}
	if r := cs(t, nil, "lockfile", "check", lock); r.code != exitOK {
		t.Fatalf("after the rewrite: %+v", r)
	}
	if r := cs(t, nil, "lockfile", "check", filepath.Join(dir, "missing.json")); r.code != exitFailed {
		t.Fatalf("a missing lockfile: %+v", r)
	}
}

func TestServeListensAndStops(t *testing.T) {
	data := t.TempDir()
	testpkg.Write(t, data, testpkg.Manifest("@acme/tool", "1.0.0"), nil)
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var errb bytes.Buffer
	go func() {
		done <- run(ctx, []string{"serve", "--data", data, "--listen", "127.0.0.1:0", "--print-port", "--strict", "@acme"},
			pw, &errb, func(string) string { return "" })
		_ = pw.Close()
	}()
	port, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	base := "http://127.0.0.1:" + strings.TrimSpace(port)
	resp, err := http.Get(base + "/@acme%2ftool")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), base+"/@acme/tool/-/tool-1.0.0.tgz") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
	if !strings.Contains(errb.String(), "serving") || !strings.Contains(errb.String(), "stopped") {
		t.Fatalf("log %s", errb.String())
	}
}

func TestServeRefusesATakenPort(t *testing.T) {
	busy := httptest.NewServer(http.NotFoundHandler())
	defer busy.Close()
	addr := strings.TrimPrefix(busy.URL, "http://")
	if r := cs(t, nil, "serve", "--data", t.TempDir(), "--listen", addr); r.code != exitFailed {
		t.Fatalf("%+v", r)
	}
}

// An image tagged as one package and holding another is refused, so a
// mis-tagged or hostile image cannot put a package under a name it was not
// fetched as.
func TestFetchRefusesAnImageOfAnotherPackage(t *testing.T) {
	srv := httptest.NewServer(ggcrregistry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	data := testpkg.Tarball(t, testpkg.Manifest("lodash", "4.17.21"), nil)
	p, _ := npmpkg.Parse(data)
	img, err := images.Build(p, data)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := name.ParseReference(host + "/acme/npm/tool:1.0.0")
	if err := remote.Write(r, img); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if r := cs(t, nil, "fetch", "@acme/tool@1.0.0", "--registry", host, "--data", dir); r.code != exitFailed ||
		!strings.Contains(r.stderr, "no tarball of @acme/tool@1.0.0") {
		t.Fatalf("%+v", r)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("written anyway: %v", left)
	}
}

// An image that declares its tarball has that tarball extracted and nothing
// else, however many more its layers carry.
func TestExtractWritesOnlyTheDeclaredTarball(t *testing.T) {
	declared := testpkg.Tarball(t, testpkg.Manifest("@acme/tool", "1.0.0"), map[string]string{"a": "declared"})
	other := testpkg.Tarball(t, testpkg.Manifest("@acme/tool", "1.0.0"), map[string]string{"a": "other"})
	p, _ := npmpkg.Parse(declared)
	img, err := images.Build(p, declared)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "acme-tool-1.0.0.tgz", Mode: 0o644, Size: int64(len(other)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(other)
	_ = tw.Close()
	b := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil })
	if err != nil {
		t.Fatal(err)
	}
	if img, err = mutate.AppendLayers(img, layer); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "two.tar")
	if err := images.WriteArchive(archive, img, "ghcr.io/acme/npm/tool:1.0.0", images.OCI); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	r := cs(t, nil, "extract", archive, "--data", dir)
	if r.code != exitOK || strings.Count(r.stdout, "\n") != 1 {
		t.Fatalf("%+v", r)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "acme-tool-1.0.0.tgz"))
	if !bytes.Equal(got, declared) {
		t.Fatal("the undeclared tarball was written")
	}
}

// A compressed file that is not an npm tarball is refused by name, rather than
// read as one or looked up as a reference.
func TestACompressedArchiveIsNotATarball(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.tar.gz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte("not a tar"))
	_ = gz.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := cs(t, nil, "extract", path); r.code != exitBadUsage || !strings.Contains(r.stderr, "decompress") {
		t.Fatalf("%+v", r)
	}
}
