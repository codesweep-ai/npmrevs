package lockfile_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/npmrevs/internal/lockfile"
)

// A version 3 lockfile as npm writes it: two spaces, keys in npm's order, and a
// URL with an ampersand that must not come back escaped.
const v3 = `{
  "name": "t",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {
      "name": "t"
    },
    "node_modules/@acme/tool": {
      "version": "1.1.0",
      "resolved": "http://127.0.0.1:4875/@acme/tool/-/tool-1.1.0.tgz",
      "integrity": "sha512-local"
    },
    "node_modules/other/node_modules/@acme/tool": {
      "version": "1.1.0",
      "resolved": "http://127.0.0.1:4875/@acme/tool/-/tool-1.1.0.tgz",
      "integrity": "sha512-local"
    },
    "node_modules/left-pad": {
      "version": "1.3.0",
      "resolved": "https://registry.npmjs.org/left-pad/-/left-pad-1.3.0.tgz?a=1&b=2",
      "integrity": "sha512-public"
    },
    "node_modules/@acme/private": {
      "version": "0.0.1",
      "resolved": "http://localhost:4875/@acme/private/-/private-0.0.1.tgz",
      "integrity": "sha512-private"
    }
  }
}
`

func TestLocalFindsEveryLoopbackEntry(t *testing.T) {
	entries, err := lockfile.Local([]byte(v3))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries %+v", entries)
	}
	if entries[0].Name != "@acme/private" || entries[1].Name != "@acme/tool" || entries[2].Key != "node_modules/other/node_modules/@acme/tool" {
		t.Fatalf("entries %+v", entries)
	}
}

func TestLocalReadsAVersionOneLockfile(t *testing.T) {
	v1 := `{"lockfileVersion":1,"dependencies":{"@acme/tool":{"version":"1.1.0","resolved":"http://[::1]:4875/@acme/tool/-/tool-1.1.0.tgz",
	"dependencies":{"inner":{"version":"2.0.0","resolved":"http://localhost:1/inner/-/inner-2.0.0.tgz"}}}}}`
	entries, err := lockfile.Local([]byte(v1))
	if err != nil || len(entries) != 2 || entries[1].Key != "node_modules/@acme/tool/node_modules/inner" {
		t.Fatalf("%+v %v", entries, err)
	}
	if _, err := lockfile.Local([]byte("not json")); err == nil {
		t.Fatal("parsed garbage")
	}
}

func TestIsLoopback(t *testing.T) {
	for u, want := range map[string]bool{
		"http://127.0.0.1:4875/x": true, "http://localhost/x": true, "http://[::1]:1/x": true,
		"http://127.1.2.3/x": true, "https://registry.npmjs.org/x": false, "file:../x": false, "": false,
	} {
		if lockfile.IsLoopback(u) != want {
			t.Errorf("IsLoopback(%q) != %v", u, want)
		}
	}
}

// target is a fake public registry holding @acme/tool 1.1.0 with bytes of its
// own, and nothing else.
func target(t *testing.T) *lockfile.Resolver {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/@acme%2Ftool" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"versions":{"1.1.0":{"dist":{"tarball":"https://registry.example.org/@acme/tool/-/tool-1.1.0.tgz","integrity":"sha512-published"}}}}`)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return &lockfile.Resolver{Registry: u, Client: srv.Client()}
}

func TestPlanRefusesAVersionTheTargetLacks(t *testing.T) {
	_, err := lockfile.Plan(context.Background(), []byte(v3), target(t))
	var me *lockfile.MissingError
	if !errors.As(err, &me) || len(me.Entries) != 1 || me.Entries[0].Name != "@acme/private" {
		t.Fatalf("err %v", err)
	}
}

func TestApplyChangesOnlyTheURLsAndIntegrities(t *testing.T) {
	without := strings.Replace(v3, `    "node_modules/@acme/private": {
      "version": "0.0.1",
      "resolved": "http://localhost:4875/@acme/private/-/private-0.0.1.tgz",
      "integrity": "sha512-private"
    }`, `    "node_modules/@acme/private": {
      "version": "0.0.1"
    }`, 1)
	changes, err := lockfile.Plan(context.Background(), []byte(without), target(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || !changes[0].IntegrityChanged() {
		t.Fatalf("changes %+v", changes)
	}
	out, err := lockfile.Apply([]byte(without), changes)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(without, "http://127.0.0.1:4875/@acme/tool/-/tool-1.1.0.tgz", "https://registry.example.org/@acme/tool/-/tool-1.1.0.tgz")
	want = strings.ReplaceAll(want, "sha512-local", "sha512-published")
	if string(out) != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
	if left, _ := lockfile.Local(out); len(left) != 0 {
		t.Fatalf("loopback entries left: %+v", left)
	}

	path := filepath.Join(t.TempDir(), "package-lock.json")
	if err := os.WriteFile(path, []byte(without), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lockfile.WriteFile(path, out); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
}

// Two copies of one version locked at different times carry one URL and two
// integrities. Each gets the target's integrity, and an entry that did not
// come through this machine keeps its own even when it shares an old one.
func TestApplyRewritesEachPairAndNothingElse(t *testing.T) {
	lock := `{
  "lockfileVersion": 3,
  "packages": {
    "node_modules/@acme/tool": {
      "version": "1.1.0",
      "resolved": "http://127.0.0.1:4875/@acme/tool/-/tool-1.1.0.tgz",
      "integrity": "sha512-new-build"
    },
    "node_modules/other/node_modules/@acme/tool": {
      "version": "1.1.0",
      "resolved": "http://127.0.0.1:4875/@acme/tool/-/tool-1.1.0.tgz",
      "integrity": "sha512-old-build"
    },
    "node_modules/vendored": {
      "version": "1.1.0",
      "resolved": "file:../acme-tool-1.1.0.tgz",
      "integrity": "sha512-old-build"
    }
  }
}
`
	changes, err := lockfile.Plan(context.Background(), []byte(lock), target(t))
	if err != nil {
		t.Fatal(err)
	}
	out, err := lockfile.Apply([]byte(lock), changes)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "127.0.0.1") || strings.Count(string(out), "sha512-published") != 2 {
		t.Fatalf("got\n%s", out)
	}
	if !strings.Contains(string(out), `"resolved": "file:../acme-tool-1.1.0.tgz",
      "integrity": "sha512-old-build"`) {
		t.Fatalf("an entry that never came through this machine was changed:\n%s", out)
	}
}
