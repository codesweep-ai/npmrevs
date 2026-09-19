// Package datadir indexes the npm tarballs in one or more data directories, by
// the package name and version each one declares.
//
// The index follows the directories as they change: a tarball dropped in is
// served on the next request, and one removed stops being served. Only a file
// that is new, or whose size or modification time moved, is read again, so a
// directory of a few hundred tarballs costs a directory listing per refresh.
package datadir

import (
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/codesweep-ai/npmrevs/internal/npmpkg"
)

// refreshEvery bounds how often the directories are listed. An install asks
// for hundreds of packuments in a few seconds, and one listing covers them all.
const refreshEvery = 250 * time.Millisecond

// Index is the set of packages held in the data directories.
type Index struct {
	dirs []string
	log  *slog.Logger

	mu       sync.Mutex
	checked  time.Time
	files    map[string]fileState
	packages map[string]map[string]*npmpkg.Package
	broken   map[string]error
}

type fileState struct {
	size    int64
	modTime time.Time
	pkg     *npmpkg.Package
}

// ConflictError reports two files that declare the same name and version with
// different bytes. Serving either would let two machines install different code
// under one version, so neither is served.
type ConflictError struct {
	Name, Version string
	Paths         []string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s@%s is in two files with different bytes: %s. Remove one.",
		e.Name, e.Version, strings.Join(e.Paths, " and "))
}

// Open indexes the directories. A directory that does not exist yet is
// created, so a server can start before anything has been packed into it. A
// conflict between two files fails the open.
func Open(dirs []string, log *slog.Logger) (*Index, error) {
	abs := make([]string, 0, len(dirs))
	for _, d := range dirs {
		a, err := filepath.Abs(d)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(a, 0o755); err != nil {
			return nil, err
		}
		abs = append(abs, a)
	}
	idx := &Index{dirs: abs, log: log, files: map[string]fileState{}}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.refreshLocked()
	if len(idx.broken) > 0 {
		names := slices.Sorted(maps.Keys(idx.broken))
		return nil, idx.broken[names[0]]
	}
	return idx, nil
}

// Dirs returns the absolute paths of the directories the index reads.
func (idx *Index) Dirs() []string { return slices.Clone(idx.dirs) }

// Versions returns the local versions of name, keyed by version. The error is a
// ConflictError when two files disagree about one of them.
func (idx *Index) Versions(name string) (map[string]*npmpkg.Package, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if time.Since(idx.checked) >= refreshEvery {
		idx.refreshLocked()
	}
	if err := idx.broken[name]; err != nil {
		return nil, err
	}
	return idx.packages[name], nil
}

// Refresh rereads the directories now, however recently they were read. The
// server calls it when a file it is about to serve has changed since.
func (idx *Index) Refresh() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.refreshLocked()
}

// Count returns how many package names and versions the index holds.
func (idx *Index) Count() (names, versions int) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if time.Since(idx.checked) >= refreshEvery {
		idx.refreshLocked()
	}
	for _, vs := range idx.packages {
		versions += len(vs)
	}
	return len(idx.packages), versions
}

// refreshLocked relists the directories and rebuilds the index from the files
// found. Callers hold idx.mu.
func (idx *Index) refreshLocked() {
	idx.checked = time.Now()
	seen := map[string]bool{}
	for _, dir := range idx.dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			idx.log.Warn("cannot list a data directory", "dir", dir, "err", err)
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".tgz") || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			// Stat rather than the entry's own information, which describes a
			// symbolic link itself: a link to a tarball that is rebuilt keeps
			// its own size and time, and the rebuild would go unseen.
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue // removed between the listing and the stat, or a link to a directory
			}
			seen[path] = true
			old, ok := idx.files[path]
			if ok && old.size == info.Size() && old.modTime.Equal(info.ModTime()) {
				continue
			}
			pkg, err := npmpkg.Read(path)
			if err != nil {
				idx.log.Warn("skipping a file that is not an npm package", "file", path, "err", err)
				pkg = nil
			}
			idx.files[path] = fileState{size: info.Size(), modTime: info.ModTime(), pkg: pkg}
		}
	}
	for path := range idx.files {
		if !seen[path] {
			delete(idx.files, path)
		}
	}

	packages := map[string]map[string]*npmpkg.Package{}
	broken := map[string]error{}
	paths := make([]string, 0, len(idx.files))
	for path := range idx.files {
		paths = append(paths, path)
	}
	slices.Sort(paths) // a stable choice between two identical copies
	for _, path := range paths {
		pkg := idx.files[path].pkg
		if pkg == nil {
			continue
		}
		vs := packages[pkg.Name]
		if vs == nil {
			vs = map[string]*npmpkg.Package{}
			packages[pkg.Name] = vs
		}
		if prev, ok := vs[pkg.Version]; ok {
			if prev.Integrity != pkg.Integrity {
				broken[pkg.Name] = &ConflictError{Name: pkg.Name, Version: pkg.Version, Paths: []string{prev.Path, pkg.Path}}
			}
			continue
		}
		vs[pkg.Version] = pkg
	}
	for name, err := range broken {
		if _, was := idx.broken[name]; !was {
			idx.log.Error("refusing to serve a package", "err", err)
		}
	}
	idx.packages, idx.broken = packages, broken
}
