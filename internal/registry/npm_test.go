package registry_test

import (
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/codesweep-ai/npmrevs/internal/testpkg"
)

// TestNPMInstallsThroughTheRegistry drives the real npm client, which is the
// only check that the documents this server writes are the ones npm acts on.
//
// The upstream stands in for npmjs.com and names registry.npmjs.org in its
// tarball URLs, exactly as the real one does. npm rewrites that host to the
// registry it was given, so the tarball request arrives here, and the redirect
// sends it on. Everything listens on loopback: the test needs no network.
func TestNPMInstallsThroughTheRegistry(t *testing.T) {
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm is not on the PATH")
	}
	if testing.Short() {
		t.Skip("drives npm")
	}

	dep := testpkg.Tarball(t, testpkg.Manifest("dep", "1.2.0"), map[string]string{"index.js": "module.exports = 'dep'\n"})
	sum := sha512.Sum512(dep)
	depIntegrity := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
	// Set when the tarball is served here. The packument names npmjs.com, so
	// the only way a request reaches this server is through the redirect.
	var redirected atomic.Bool
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/dep":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"name":"dep","dist-tags":{"latest":"1.2.0"},"versions":{"1.2.0":{"name":"dep","version":"1.2.0",
				"dist":{"tarball":"https://registry.npmjs.org/dep/-/dep-1.2.0.tgz","integrity":%q}}}}`, depIntegrity)
		case "/dep/-/dep-1.2.0.tgz":
			redirected.Store(true)
			_, _ = w.Write(dep)
		default:
			http.NotFound(w, r)
		}
	}))
	defer public.Close()

	platform := map[string]string{"linux": "linux", "darwin": "darwin"}[runtime.GOOS]
	if platform == "" {
		t.Skip("no npm platform name for " + runtime.GOOS)
	}
	data := t.TempDir()
	testpkg.Write(t, data, testpkg.Manifest("@acme/app", "1.0.0",
		"dependencies", map[string]string{"dep": "^1.0.0", "@acme/lib": "1.0.0"},
		"optionalDependencies", map[string]string{"@acme/app-here": "1.0.0", "@acme/app-elsewhere": "1.0.0"},
	), map[string]string{"index.js": "module.exports = 'app'\n"})
	testpkg.Write(t, data, testpkg.Manifest("@acme/lib", "1.0.0"), nil)
	testpkg.Write(t, data, testpkg.Manifest("@acme/app-here", "1.0.0", "os", []string{platform}), nil)
	testpkg.Write(t, data, testpkg.Manifest("@acme/app-elsewhere", "1.0.0", "os", []string{"aix"}), nil)

	up := &upstream{Server: public}
	srv := serve(t, up, []string{data})

	work := t.TempDir()
	npmrc := filepath.Join(work, "npmrc")
	writeFile(t, npmrc, "registry="+srv.URL+"/\nupdate-notifier=false\naudit=false\nfund=false\ncache="+filepath.Join(work, "cache")+"\n")
	proj := filepath.Join(work, "proj")
	writeFile(t, filepath.Join(proj, "package.json"), `{"name":"t","private":true}`)

	cmd := exec.Command(npm, "install", "--no-progress", "@acme/app@1.0.0")
	cmd.Dir = proj
	cmd.Env = append(filteredEnv(), "NPM_CONFIG_USERCONFIG="+npmrc)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("npm install: %v\n%s", err, out)
	}

	for _, p := range []string{"@acme/app", "@acme/lib", "@acme/app-here", "dep"} {
		if _, err := os.Stat(filepath.Join(proj, "node_modules", p, "package.json")); err != nil {
			t.Errorf("%s was not installed: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(proj, "node_modules", "@acme", "app-elsewhere")); err == nil {
		t.Error("a package for another platform was installed")
	}

	var lock struct {
		Packages map[string]struct {
			Resolved string `json:"resolved"`
		} `json:"packages"`
	}
	b, err := os.ReadFile(filepath.Join(proj, "package-lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &lock); err != nil {
		t.Fatal(err)
	}
	if got := lock.Packages["node_modules/@acme/app"].Resolved; !strings.HasPrefix(got, srv.URL+"/") {
		t.Errorf("the local package resolved to %q", got)
	}
	if !redirected.Load() {
		t.Error("the upstream tarball did not come through the redirect")
	}
	// npm records the URL the packument named, not the host it rewrote it to,
	// so a lockfile carries this server's address only for local versions.
	if got := lock.Packages["node_modules/dep"].Resolved; got != "https://registry.npmjs.org/dep/-/dep-1.2.0.tgz" {
		t.Errorf("the upstream package resolved to %q", got)
	}
}

// filteredEnv is this process's environment without npm settings that would
// point the child at another registry or another user config.
func filteredEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(strings.ToLower(k), "npm_config_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
