package npmpkg_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/npmrevs/internal/npmpkg"
	"github.com/codesweep-ai/npmrevs/internal/testpkg"
)

func TestParseReadsTheManifestAndHashesTheWholeFile(t *testing.T) {
	data := testpkg.Tarball(t, testpkg.Manifest("@acme/tool", "1.2.3", "license", "MIT"),
		map[string]string{"index.js": "module.exports = 1\n"})
	p, err := npmpkg.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "@acme/tool" || p.Version != "1.2.3" {
		t.Fatalf("read %s@%s", p.Name, p.Version)
	}
	s512 := sha512.Sum512(data)
	if want := "sha512-" + base64.StdEncoding.EncodeToString(s512[:]); p.Integrity != want {
		t.Errorf("integrity %s, want %s", p.Integrity, want)
	}
	s1 := sha1.Sum(data)
	if want := hex.EncodeToString(s1[:]); p.Shasum != want {
		t.Errorf("shasum %s, want %s", p.Shasum, want)
	}
	if p.FileCount != 2 || p.Size != int64(len(data)) {
		t.Errorf("fileCount %d size %d", p.FileCount, p.Size)
	}
	if string(p.Manifest["license"]) != `"MIT"` {
		t.Errorf("manifest lost a field: %s", p.Manifest["license"])
	}
}

// npm strips the first path component whatever it is called, and so must the
// registry, or a tarball packed by another tool would read as having no
// package.json.
func TestParseAcceptsAnyTopDirectory(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := `{"name":"odd","version":"0.1.0"}`
	_ = tw.WriteHeader(&tar.Header{Name: "node/package.json", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gz.Close()
	p, err := npmpkg.Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "odd" {
		t.Fatalf("name %q", p.Name)
	}
}

func TestParseRefusesWhatIsNotAPackage(t *testing.T) {
	cases := map[string][]byte{
		"not gzip":        []byte("plain text"),
		"no package.json": testpkg.Tarball(t, testpkg.Manifest("x", "1.0.0"), nil)[:10],
		"bad version":     testpkg.Tarball(t, testpkg.Manifest("x", "v1"), nil),
		"bad name":        testpkg.Tarball(t, testpkg.Manifest("Upper", "1.0.0"), nil),
		"no name":         testpkg.Tarball(t, map[string]any{"version": "1.0.0"}, nil),
	}
	for name, data := range cases {
		if _, err := npmpkg.Parse(data); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestReadRecordsThePathAndTime(t *testing.T) {
	dir := t.TempDir()
	path := testpkg.Write(t, dir, testpkg.Manifest("a", "1.0.0"), nil)
	p, err := npmpkg.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != path || p.ModTime.IsZero() {
		t.Fatalf("path %q time %v", p.Path, p.ModTime)
	}
	if _, err := npmpkg.Read(filepath.Join(dir, "missing.tgz")); err == nil {
		t.Fatal("read a missing file")
	}
	bad := filepath.Join(dir, "bad.tgz")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := npmpkg.Read(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("error %v does not name the file", err)
	}
}

func TestHasInstallScript(t *testing.T) {
	cases := []struct {
		name    string
		scripts map[string]string
		files   map[string]string
		want    bool
	}{
		{"none", nil, nil, false},
		{"test only", map[string]string{"test": "x"}, nil, false},
		{"postinstall", map[string]string{"postinstall": "x"}, nil, true},
		{"preinstall", map[string]string{"preinstall": "x"}, nil, true},
		{"binding.gyp", nil, map[string]string{"binding.gyp": "{}"}, true},
	}
	for _, c := range cases {
		m := testpkg.Manifest("a", "1.0.0")
		if c.scripts != nil {
			m["scripts"] = c.scripts
		}
		p, err := npmpkg.Parse(testpkg.Tarball(t, m, c.files))
		if err != nil {
			t.Fatal(err)
		}
		if got := p.HasInstallScript(); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEntryIsWhatAPackumentLists(t *testing.T) {
	m := testpkg.Manifest("@acme/tool", "1.0.0", "bin", "cli.js", "os", []string{"linux"})
	p, err := npmpkg.Parse(testpkg.Tarball(t, m, map[string]string{"npm-shrinkwrap.json": "{}"}))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p.Entry("http://127.0.0.1:4873/"))
	if err != nil {
		t.Fatal(err)
	}
	var e struct {
		ID            string            `json:"_id"`
		Bin           map[string]string `json:"bin"`
		OS            []string          `json:"os"`
		HasShrinkwrap bool              `json:"_hasShrinkwrap"`
		Dist          struct {
			Tarball, Integrity, Shasum string
			FileCount                  int
		} `json:"dist"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	if e.ID != "@acme/tool@1.0.0" || e.Bin["tool"] != "cli.js" || len(e.OS) != 1 || !e.HasShrinkwrap {
		t.Fatalf("entry %s", raw)
	}
	if e.Dist.Tarball != "http://127.0.0.1:4873/@acme/tool/-/tool-1.0.0.tgz" || e.Dist.Integrity != p.Integrity || e.Dist.FileCount != 2 {
		t.Fatalf("dist %+v", e.Dist)
	}
	noBase, _ := json.Marshal(p.Entry(""))
	if strings.Contains(string(noBase), "tarball") {
		t.Fatalf("an entry with no base still names a tarball: %s", noBase)
	}
}

func TestNames(t *testing.T) {
	if got := npmpkg.Filename("@acme/tool", "1.0.0"); got != "acme-tool-1.0.0.tgz" {
		t.Errorf("Filename %s", got)
	}
	if got := npmpkg.Filename("tool", "1.0.0"); got != "tool-1.0.0.tgz" {
		t.Errorf("Filename %s", got)
	}
	if got := npmpkg.TarballPath("@acme/tool", "1.0.0"); got != "@acme/tool/-/tool-1.0.0.tgz" {
		t.Errorf("TarballPath %s", got)
	}
	if npmpkg.Scope("@acme/tool") != "@acme" || npmpkg.Scope("tool") != "" || npmpkg.Bare("@acme/tool") != "tool" {
		t.Error("Scope or Bare")
	}
	for v, want := range map[string]bool{"1.0.0": true, "0.0.0-20260918041056-8bdc0656c767": true, "v1.0.0": false, "1.0": false, "": false} {
		if npmpkg.ValidVersion(v) != want {
			t.Errorf("ValidVersion(%q) != %v", v, want)
		}
	}
}
