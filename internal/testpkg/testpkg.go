// Package testpkg builds npm package tarballs for tests, the way `npm pack`
// lays them out: every file under package/, package.json among them.
package testpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Tarball returns a .tgz holding manifest as package/package.json, and each
// file under package/. The bytes depend only on the arguments.
func Tarball(t testing.TB, manifest map[string]any, files map[string]string) []byte {
	t.Helper()
	pj, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]string{"package.json": string(pj)}
	maps.Copy(all, files)
	names := make([]string, 0, len(all))
	for k := range all {
		names = append(names, k)
	}
	slices.Sort(names)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.ModTime = time.Time{}
	tw := tar.NewWriter(gz)
	for _, n := range names {
		body := all[n]
		hdr := &tar.Header{
			Name:     "package/" + n,
			Mode:     0o644,
			Size:     int64(len(body)),
			ModTime:  time.Date(1985, 10, 26, 8, 15, 0, 0, time.UTC),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Manifest is a package.json with the given name and version, and whatever
// other fields are passed as key, value pairs.
func Manifest(name, version string, kv ...any) map[string]any {
	m := map[string]any{"name": name, "version": version}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

// Write writes a tarball of manifest into dir under the name npm pack gives it,
// and returns its path.
func Write(t testing.TB, dir string, manifest map[string]any, files map[string]string) string {
	t.Helper()
	name, _ := manifest["name"].(string)
	version, _ := manifest["version"].(string)
	file := strings.TrimPrefix(strings.ReplaceAll(name, "/", "-"), "@") + "-" + version + ".tgz"
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, Tarball(t, manifest, files), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
