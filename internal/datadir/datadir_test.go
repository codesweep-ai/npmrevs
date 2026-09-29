package datadir_test

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codesweep-ai/npmrevs/internal/datadir"
	"github.com/codesweep-ai/npmrevs/internal/testpkg"
)

var quiet = slog.New(slog.DiscardHandler)

func TestOpenIndexesByTheNameInsideEachFile(t *testing.T) {
	dir := t.TempDir()
	path := testpkg.Write(t, dir, testpkg.Manifest("@acme/tool", "1.0.0"), nil)
	// The file name says nothing: the manifest inside it decides.
	if err := os.Rename(path, filepath.Join(dir, "anything.tgz")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := datadir.Open([]string{dir}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := idx.Versions("@acme/tool")
	if err != nil || vs["1.0.0"] == nil {
		t.Fatalf("versions %v, err %v", vs, err)
	}
	if names, versions := idx.Count(); names != 1 || versions != 1 {
		t.Fatalf("count %d %d", names, versions)
	}
}

func TestOpenCreatesAMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not", "yet")
	idx, err := datadir.Open([]string{dir}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	if got := idx.Dirs(); len(got) != 1 || got[0] != dir {
		t.Fatalf("dirs %v", got)
	}
}

// Two files claiming one version with different bytes would let two machines
// install different code under one number, so the index refuses both.
func TestConflictingCopiesFailTheOpen(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	testpkg.Write(t, a, testpkg.Manifest("x", "1.0.0"), map[string]string{"a.js": "1"})
	testpkg.Write(t, b, testpkg.Manifest("x", "1.0.0"), map[string]string{"a.js": "2"})
	_, err := datadir.Open([]string{a, b}, quiet)
	var ce *datadir.ConflictError
	if !errors.As(err, &ce) || ce.Name != "x" || len(ce.Paths) != 2 {
		t.Fatalf("err %v", err)
	}
}

// The files are read side by side, so each one's result has to land against
// its own path, and a file that is no package must spoil none of the others.
func TestOpenReadsEveryFileOfManyAtOnce(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for i := range 64 {
		dir := a
		if i%2 == 1 {
			dir = b
		}
		testpkg.Write(t, dir, testpkg.Manifest(fmt.Sprintf("p%d", i%16), fmt.Sprintf("1.0.%d", i)), nil)
	}
	if err := os.WriteFile(filepath.Join(a, "broken.tgz"), []byte("not a tarball"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := datadir.Open([]string{a, b}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if names, versions := idx.Count(); names != 16 || versions != 64 {
		t.Fatalf("count %d %d", names, versions)
	}
	vs, err := idx.Versions("p5")
	if err != nil || len(vs) != 4 || vs["1.0.21"] == nil || vs["1.0.21"].Version != "1.0.21" {
		t.Fatalf("versions %v, err %v", vs, err)
	}
}

func TestIdenticalCopiesAreOneVersion(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	testpkg.Write(t, a, testpkg.Manifest("x", "1.0.0"), nil)
	testpkg.Write(t, b, testpkg.Manifest("x", "1.0.0"), nil)
	idx, err := datadir.Open([]string{a, b}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if _, versions := idx.Count(); versions != 1 {
		t.Fatalf("versions %d", versions)
	}
}

func TestTheIndexFollowsTheDirectory(t *testing.T) {
	dir := t.TempDir()
	idx, err := datadir.Open([]string{dir}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	wait := func() { time.Sleep(300 * time.Millisecond) }

	path := testpkg.Write(t, dir, testpkg.Manifest("x", "1.0.0"), map[string]string{"a.js": "1"})
	wait()
	vs, _ := idx.Versions("x")
	if vs["1.0.0"] == nil {
		t.Fatal("a new file was not picked up")
	}
	first := vs["1.0.0"].Integrity

	// Rewritten in place, which changes neither the name nor the directory.
	testpkg.Write(t, dir, testpkg.Manifest("x", "1.0.0"), map[string]string{"a.js": "22"})
	wait()
	vs, _ = idx.Versions("x")
	if vs["1.0.0"].Integrity == first {
		t.Fatal("a rewritten file kept its old integrity")
	}

	// A second copy that disagrees breaks the package, and removing it heals it.
	other := filepath.Join(dir, "copy.tgz")
	if err := os.WriteFile(other, testpkg.Tarball(t, testpkg.Manifest("x", "1.0.0"), nil), 0o644); err != nil {
		t.Fatal(err)
	}
	wait()
	if _, err := idx.Versions("x"); err == nil {
		t.Fatal("a conflict was served")
	}
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}
	wait()
	if _, err := idx.Versions("x"); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	wait()
	if vs, _ := idx.Versions("x"); len(vs) != 0 {
		t.Fatal("a removed file is still served")
	}
}
