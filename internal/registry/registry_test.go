package registry_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codesweep-ai/npmrevs/internal/datadir"
	"github.com/codesweep-ai/npmrevs/internal/registry"
	"github.com/codesweep-ai/npmrevs/internal/testpkg"
)

// upstream is a fake public registry: one packument per name, and a count of
// the requests it answered.
type upstream struct {
	*httptest.Server
	docs  map[string]string
	hits  atomic.Int32
	down  bool
	lastA atomic.Value
}

func newUpstream(t *testing.T, docs map[string]string) *upstream {
	u := &upstream{docs: docs}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		u.lastA.Store(r.Header.Get("Accept"))
		if u.down {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"audited":true}`)
			return
		}
		name, err := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), "/"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		doc, ok := u.docs[name]
		if !ok {
			http.Error(w, `{"error":"Not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"abc"`)
		_, _ = io.WriteString(w, doc)
	}))
	t.Cleanup(u.Close)
	return u
}

func serve(t *testing.T, up *upstream, dirs []string, strict ...string) *httptest.Server {
	t.Helper()
	idx, err := datadir.Open(dirs, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(up.URL)
	srv := httptest.NewServer(registry.New(registry.Config{
		Index: idx, Upstream: u, Strict: strict, Version: "test", UpstreamTTL: time.Minute,
	}))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, u string, header ...string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, http.NoBody)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

type packument struct {
	Name     string                     `json:"name"`
	DistTags map[string]string          `json:"dist-tags"`
	Versions map[string]json.RawMessage `json:"versions"`
	Time     map[string]string          `json:"time"`
	Modified string                     `json:"modified"`
}

const publishedTool = `{"name":"@acme/tool","dist-tags":{"latest":"1.0.0","beta":"2.0.0-beta.1"},
"versions":{
 "1.0.0":{"name":"@acme/tool","version":"1.0.0","dist":{"tarball":"https://registry.npmjs.org/@acme/tool/-/tool-1.0.0.tgz","integrity":"sha512-published"}},
 "1.1.0":{"name":"@acme/tool","version":"1.1.0","dist":{"tarball":"https://registry.npmjs.org/@acme/tool/-/tool-1.1.0.tgz","integrity":"sha512-published"}},
 "2.0.0-beta.1":{"name":"@acme/tool","version":"2.0.0-beta.1","dist":{"tarball":"x","integrity":"y"}}},
"time":{"created":"2020-01-01T00:00:00.000Z","modified":"2020-01-02T00:00:00.000Z","1.0.0":"2020-01-01T00:00:00.000Z"}}`

func TestAPackageWithNoLocalVersionPassesThroughUntouched(t *testing.T) {
	up := newUpstream(t, map[string]string{"left-pad": `{"name":"left-pad","versions":{}}`})
	srv := serve(t, up, []string{t.TempDir()})
	resp, body := get(t, srv.URL+"/left-pad", "Accept", "application/vnd.npm.install-v1+json")
	if resp.StatusCode != http.StatusOK || string(body) != `{"name":"left-pad","versions":{}}` {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("ETag") != `"abc"` || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("headers %v", resp.Header)
	}
	if a, _ := up.lastA.Load().(string); a != "application/vnd.npm.install-v1+json" {
		t.Errorf("the client's Accept did not reach the upstream: %q", a)
	}
	if !strings.HasPrefix(resp.Header.Get("Server"), "cs-npmrevs/") {
		t.Errorf("server header %q", resp.Header.Get("Server"))
	}
	resp, _ = get(t, srv.URL+"/nobody-published-this")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("an absent package answered %d", resp.StatusCode)
	}
}

func TestLocalVersionsAreMergedAndWinOnTheSameNumber(t *testing.T) {
	up := newUpstream(t, map[string]string{"@acme/tool": publishedTool})
	dir := t.TempDir()
	testpkg.Write(t, dir, testpkg.Manifest("@acme/tool", "1.1.0"), map[string]string{"local.js": "1"})
	testpkg.Write(t, dir, testpkg.Manifest("@acme/tool", "1.2.0-dev.1"), nil)
	srv := serve(t, up, []string{dir})

	// Scoped names arrive escaped, as npm sends them, and unescaped.
	for _, path := range []string{"/@acme%2ftool", "/@acme%2Ftool", "/@acme/tool"} {
		resp, body := get(t, srv.URL+path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
		var p packument
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatal(err)
		}
		if len(p.Versions) != 4 {
			t.Fatalf("%s: versions %v", path, p.Versions)
		}
		if !strings.Contains(string(p.Versions["1.1.0"]), srv.URL+"/@acme/tool/-/tool-1.1.0.tgz") {
			t.Errorf("the local 1.1.0 did not replace the published one: %s", p.Versions["1.1.0"])
		}
		if !strings.Contains(string(p.Versions["1.0.0"]), "registry.npmjs.org") {
			t.Errorf("a published version lost its URL: %s", p.Versions["1.0.0"])
		}
		// latest is the highest release across both, and other tags pass through.
		if p.DistTags["latest"] != "1.1.0" || p.DistTags["beta"] != "2.0.0-beta.1" {
			t.Errorf("dist-tags %v", p.DistTags)
		}
		if p.Time["1.1.0"] == "" || p.Time["1.0.0"] != "2020-01-01T00:00:00.000Z" || p.Time["modified"] <= "2020" {
			t.Errorf("time %v", p.Time)
		}
	}
	if n := up.hits.Load(); n != 1 {
		t.Errorf("the upstream was asked %d times; the merge should reuse its answer", n)
	}
}

func TestALocalOnlyPackageStandsAlone(t *testing.T) {
	up := newUpstream(t, nil)
	dir := t.TempDir()
	testpkg.Write(t, dir, testpkg.Manifest("@acme/private", "0.0.0-20260918041056-8bdc0656c767"), nil)
	srv := serve(t, up, []string{dir})
	resp, body := get(t, srv.URL+"/@acme%2fprivate")
	var p packument
	if err := json.Unmarshal(body, &p); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	// Only prereleases, and no upstream latest: there is no latest.
	if p.Name != "@acme/private" || len(p.Versions) != 1 || len(p.DistTags) != 0 || p.Modified == "" {
		t.Fatalf("packument %s", body)
	}
}

func TestAnUpstreamThatFailsIsAnErrorNotAPartialAnswer(t *testing.T) {
	up := newUpstream(t, map[string]string{"@acme/tool": publishedTool})
	up.down = true
	dir := t.TempDir()
	testpkg.Write(t, dir, testpkg.Manifest("@acme/tool", "1.1.0"), nil)
	srv := serve(t, up, []string{dir})
	resp, body := get(t, srv.URL+"/@acme%2ftool")
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "upstream") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	up.Close()
	resp, body = get(t, srv.URL+"/left-pad")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("pass-through with the upstream gone: %d %s", resp.StatusCode, body)
	}
}

func TestStrictScopesNeverReachTheUpstream(t *testing.T) {
	up := newUpstream(t, map[string]string{"@acme/tool": publishedTool, "@acme/other": publishedTool})
	dir := t.TempDir()
	testpkg.Write(t, dir, testpkg.Manifest("@acme/tool", "1.1.0"), nil)
	srv := serve(t, up, []string{dir}, "@acme")

	_, body := get(t, srv.URL+"/@acme%2ftool")
	var p packument
	_ = json.Unmarshal(body, &p)
	if len(p.Versions) != 1 {
		t.Fatalf("strict packument %s", body)
	}
	resp, body := get(t, srv.URL+"/@acme%2fother")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "@acme/other") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	resp, _ = get(t, srv.URL+"/@acme/other/-/other-1.0.0.tgz")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("strict tarball %d", resp.StatusCode)
	}
	if n := up.hits.Load(); n != 0 {
		t.Fatalf("the upstream was asked %d times", n)
	}
}

func TestTarballsAreServedLocallyOrRedirected(t *testing.T) {
	up := newUpstream(t, nil)
	dir := t.TempDir()
	path := testpkg.Write(t, dir, testpkg.Manifest("@acme/tool", "1.1.0"), nil)
	srv := serve(t, up, []string{dir})

	resp, body := get(t, srv.URL+"/@acme/tool/-/tool-1.1.0.tgz")
	want := testpkg.Tarball(t, testpkg.Manifest("@acme/tool", "1.1.0"), nil)
	if resp.StatusCode != http.StatusOK || string(body) != string(want) {
		t.Fatalf("local tarball %d (%d bytes) from %s", resp.StatusCode, len(body), path)
	}

	resp, _ = get(t, srv.URL+"/@acme/tool/-/tool-1.0.0.tgz")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != up.URL+"/@acme/tool/-/tool-1.0.0.tgz" {
		t.Fatalf("published tarball %d -> %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = get(t, srv.URL+"/left-pad/-/left-pad-1.3.0.tgz")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != up.URL+"/left-pad/-/left-pad-1.3.0.tgz" {
		t.Fatalf("unscoped tarball %d -> %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestWritesAreRefusedByName(t *testing.T) {
	up := newUpstream(t, nil)
	srv := serve(t, up, []string{t.TempDir()})
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		req, _ := http.NewRequest(method, srv.URL+"/@acme%2ftool", strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed || !strings.Contains(string(body), "data directory") {
			t.Errorf("%s: %d %s", method, resp.StatusCode, body)
		}
	}
	resp, _ := get(t, srv.URL+"/a/b/c/d/e")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path %d", resp.StatusCode)
	}
	resp, _ = get(t, srv.URL+"/Not_A_Name")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("invalid name %d", resp.StatusCode)
	}
}

func TestAConflictIsReportedNotServed(t *testing.T) {
	up := newUpstream(t, nil)
	dir := t.TempDir()
	srv := serve(t, up, []string{dir})
	testpkg.Write(t, dir, testpkg.Manifest("x", "1.0.0"), map[string]string{"a": "1"})
	// A second file for the same version, under another name and with other bytes.
	if err := writeAs(t, t.TempDir(), dir+"/copy.tgz", testpkg.Manifest("x", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	resp, body := get(t, srv.URL+"/x")
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "different bytes") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

// writeAs packs manifest in scratch and moves the tarball to dest.
func writeAs(t *testing.T, scratch, dest string, manifest map[string]any) error {
	t.Helper()
	return os.Rename(testpkg.Write(t, scratch, manifest, nil), dest)
}

func TestStatusAndPing(t *testing.T) {
	up := newUpstream(t, nil)
	dir := t.TempDir()
	testpkg.Write(t, dir, testpkg.Manifest("x", "1.0.0"), nil)
	srv := serve(t, up, []string{dir}, "@acme")
	resp, _ := get(t, srv.URL+"/-/ping")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ping %d", resp.StatusCode)
	}
	resp, body := get(t, srv.URL+"/-/npmrevs")
	var st registry.Status
	if err := json.Unmarshal(body, &st); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if st.Server != "cs-npmrevs" || st.Packages != 1 || st.Versions != 1 || len(st.Data) != 1 || st.Strict[0] != "@acme" {
		t.Fatalf("status %+v", st)
	}
}

func TestAuditIsForwarded(t *testing.T) {
	up := newUpstream(t, nil)
	srv := serve(t, up, []string{t.TempDir()})
	resp, err := http.Post(srv.URL+"/-/npm/v1/security/advisories/bulk", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != `{"audited":true}` {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestHighestRelease(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"1.0.0", "1.10.0", "1.9.0", "2.0.0-rc.1"}, "1.10.0"},
		{[]string{"0.0.0-20260918041056-8bdc0656c767"}, ""},
		{[]string{"garbage", "1.0.0"}, "1.0.0"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := registry.HighestRelease(c.in); got != c.want {
			t.Errorf("%v: %q, want %q", c.in, got, c.want)
		}
	}
}

// Names from before npm required lower case are still served by the public
// registry, and a tree that holds one has to install.
func TestLegacyNamesPassThrough(t *testing.T) {
	up := newUpstream(t, map[string]string{"JSONStream": `{"name":"JSONStream","versions":{}}`})
	srv := serve(t, up, []string{t.TempDir()})
	resp, body := get(t, srv.URL+"/JSONStream")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "JSONStream") {
		t.Fatalf("packument %d %s", resp.StatusCode, body)
	}
	resp, _ = get(t, srv.URL+"/JSONStream/-/JSONStream-1.3.5.tgz")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("tarball %d", resp.StatusCode)
	}
	for _, bad := range []string{"/..%2f..%2fetc", "/@%2fx", "/a%20b"} {
		if resp, _ := get(t, srv.URL+bad); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", bad, resp.StatusCode)
		}
	}
}

// A tarball linked into the data directory from where it is built is reread
// when the build replaces it, so the packument never promises the old bytes.
func TestALinkedTarballIsRereadWhenRebuilt(t *testing.T) {
	up := newUpstream(t, nil)
	build, data := t.TempDir(), t.TempDir()
	target := testpkg.Write(t, build, testpkg.Manifest("x", "1.0.0"), map[string]string{"a.js": "1"})
	if err := os.Symlink(target, data+"/x-1.0.0.tgz"); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, up, []string{data})
	integrity := func() string {
		_, body := get(t, srv.URL+"/x")
		var p packument
		_ = json.Unmarshal(body, &p)
		var v struct{ Dist struct{ Integrity string } }
		_ = json.Unmarshal(p.Versions["1.0.0"], &v)
		return v.Dist.Integrity
	}
	before := integrity()
	testpkg.Write(t, build, testpkg.Manifest("x", "1.0.0"), map[string]string{"a.js": "rebuilt"})
	time.Sleep(300 * time.Millisecond)
	after := integrity()
	if after == before {
		t.Fatal("the packument still promises the bytes the link pointed at before the rebuild")
	}
	_, got := get(t, srv.URL+"/x/-/x-1.0.0.tgz")
	if want := testpkg.Tarball(t, testpkg.Manifest("x", "1.0.0"), map[string]string{"a.js": "rebuilt"}); string(got) != string(want) {
		t.Fatal("the served bytes are not the rebuilt tarball")
	}
}

func TestAConflictOnTheTarballRouteIsTheServersOwnError(t *testing.T) {
	up := newUpstream(t, nil)
	dir := t.TempDir()
	srv := serve(t, up, []string{dir})
	testpkg.Write(t, dir, testpkg.Manifest("x", "1.0.0"), map[string]string{"a": "1"})
	if err := writeAs(t, t.TempDir(), dir+"/copy.tgz", testpkg.Manifest("x", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if resp, _ := get(t, srv.URL+"/x/-/x-1.0.0.tgz"); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestStatusCountsFollowTheDirectory(t *testing.T) {
	up := newUpstream(t, nil)
	dir := t.TempDir()
	srv := serve(t, up, []string{dir})
	testpkg.Write(t, dir, testpkg.Manifest("x", "1.0.0"), nil)
	time.Sleep(300 * time.Millisecond)
	_, body := get(t, srv.URL+"/-/npmrevs")
	var st registry.Status
	if err := json.Unmarshal(body, &st); err != nil || st.Versions != 1 || st.PID != os.Getpid() {
		t.Fatalf("status %s", body)
	}
}
