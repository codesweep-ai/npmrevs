// Package lockfile finds the entries of an npm lockfile that resolved through a
// registry on this machine, and re-resolves them against a public one.
//
// A lockfile made through cs-npmrevs records cs-npmrevs's address for every package
// it served. Such a lockfile installs nowhere the server is not running, so it
// must not be committed as it is. Rewriting it is not a find-and-replace: each
// entry carries an integrity, and a local build's bytes are not the public
// registry's, so every entry is looked up where it is going.
package lockfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Entry is one package in a lockfile that resolved through a loopback address.
type Entry struct {
	// Key is where the lockfile lists it, such as "node_modules/@scope/name".
	Key       string
	Name      string
	Version   string
	Resolved  string
	Integrity string
}

// lock is the part of package-lock.json this package reads. Version 1 keeps a
// nested `dependencies` tree; versions 2 and 3 keep a flat `packages` map, and 2
// carries both.
type lock struct {
	LockfileVersion int                    `json:"lockfileVersion"`
	Packages        map[string]lockPackage `json:"packages"`
	Dependencies    map[string]lockDep     `json:"dependencies"`
}

type lockPackage struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Resolved  string `json:"resolved"`
	Integrity string `json:"integrity"`
}

type lockDep struct {
	Version      string             `json:"version"`
	Resolved     string             `json:"resolved"`
	Integrity    string             `json:"integrity"`
	Dependencies map[string]lockDep `json:"dependencies"`
}

// Local lists the entries of the lockfile in data that resolved through a
// loopback address, sorted by key.
func Local(data []byte) ([]Entry, error) {
	_, local, err := Scan(data)
	return local, err
}

// Scan reads the lockfile in data. It counts the entries that carry a resolved
// URL, which are the ones that could have come through this machine, and lists
// those that did, as Local does.
func Scan(data []byte) (resolved int, local []Entry, err error) {
	var l lock
	if err := json.Unmarshal(data, &l); err != nil {
		return 0, nil, fmt.Errorf("not a package-lock.json: %w", err)
	}
	for key, p := range l.Packages {
		if key == "" || p.Resolved == "" {
			continue
		}
		resolved++
		if !IsLoopback(p.Resolved) {
			continue
		}
		name := p.Name
		if name == "" {
			name = nameFromKey(key)
		}
		local = append(local, Entry{Key: key, Name: name, Version: p.Version, Resolved: p.Resolved, Integrity: p.Integrity})
	}
	if l.Packages == nil {
		walkV1(l.Dependencies, "", &resolved, &local)
	}
	slices.SortFunc(local, func(a, b Entry) int { return strings.Compare(a.Key, b.Key) })
	return resolved, local, nil
}

func walkV1(deps map[string]lockDep, prefix string, resolved *int, out *[]Entry) {
	for name, d := range deps {
		key := prefix + "node_modules/" + name
		if d.Resolved != "" {
			*resolved++
		}
		if IsLoopback(d.Resolved) {
			*out = append(*out, Entry{Key: key, Name: name, Version: d.Version, Resolved: d.Resolved, Integrity: d.Integrity})
		}
		walkV1(d.Dependencies, key+"/", resolved, out)
	}
}

// nameFromKey reads the package name out of a `packages` key, the part after
// the last node_modules/.
func nameFromKey(key string) string {
	i := strings.LastIndex(key, "node_modules/")
	if i < 0 {
		return key
	}
	return key[i+len("node_modules/"):]
}

// IsLoopback reports whether a resolved URL points at this machine.
func IsLoopback(resolved string) bool {
	u, err := url.Parse(resolved)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Change is what a rewrite does to one entry.
type Change struct {
	Entry
	NewResolved  string
	NewIntegrity string
}

// IntegrityChanged reports whether the target registry's bytes differ from the
// ones the lockfile recorded, which is what a local build of a published
// version looks like.
func (c Change) IntegrityChanged() bool { return c.NewIntegrity != c.Integrity }

// MissingError lists the entries the target registry has no version of. A
// lockfile naming them installs nowhere but here, so nothing is rewritten.
type MissingError struct{ Entries []Entry }

func (e *MissingError) Error() string {
	names := make([]string, len(e.Entries))
	for i, en := range e.Entries {
		names[i] = en.Name + "@" + en.Version
	}
	return "the target registry has no " + strings.Join(names, ", ")
}

// Resolver looks a version up on the target registry.
type Resolver struct {
	Registry  *url.URL
	Client    *http.Client
	UserAgent string
}

// dist is the part of a version object a rewrite needs.
type dist struct {
	Tarball   string `json:"tarball"`
	Integrity string `json:"integrity"`
}

// Lookup returns where the registry serves name@version and its integrity, and
// found=false when it has no such version.
func (r *Resolver) Lookup(ctx context.Context, name, version string) (d dist, found bool, err error) {
	u := strings.TrimSuffix(r.Registry.String(), "/") + "/" + strings.Replace(url.PathEscape(name), "%40", "@", 1)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return dist{}, false, err
	}
	req.Header.Set("Accept", "application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8")
	if r.UserAgent != "" {
		req.Header.Set("User-Agent", r.UserAgent)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return dist{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return dist{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return dist{}, false, fmt.Errorf("%s answered %s", u, resp.Status)
	}
	var doc struct {
		Versions map[string]struct {
			Dist dist `json:"dist"`
		} `json:"versions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(&doc); err != nil {
		return dist{}, false, fmt.Errorf("%s: %w", u, err)
	}
	v, ok := doc.Versions[version]
	if !ok || v.Dist.Tarball == "" {
		return dist{}, false, nil
	}
	return v.Dist, true, nil
}

// Plan works out what a rewrite of the lockfile in data changes, without
// changing anything. It fails with a MissingError when the target lacks any of
// the versions.
func Plan(ctx context.Context, data []byte, r *Resolver) ([]Change, error) {
	entries, err := Local(data)
	if err != nil {
		return nil, err
	}
	var changes []Change
	var missing []Entry
	for _, e := range entries {
		d, found, err := r.Lookup(ctx, e.Name, e.Version)
		if err != nil {
			return nil, err
		}
		if !found {
			missing = append(missing, e)
			continue
		}
		changes = append(changes, Change{Entry: e, NewResolved: d.Tarball, NewIntegrity: d.Integrity})
	}
	if len(missing) > 0 {
		return nil, &MissingError{Entries: missing}
	}
	return changes, nil
}

// Apply returns data with every change made. It edits the text rather than
// re-encoding the document, so the key order and layout npm wrote are kept and
// the diff shows only the URLs and integrities that moved.
//
// npm writes an entry's integrity straight after its resolved URL, so a change
// replaces the two together, as the pair it belongs to. An integrity is never
// replaced on its own: another entry, one that did not come through this
// machine, may carry the same one.
func Apply(data []byte, changes []Change) ([]byte, error) {
	out := data
	done := map[[2]string]bool{}
	for _, c := range changes {
		// Two entries can carry one URL and one integrity, and the first
		// replacement already rewrote both.
		key := [2]string{c.Resolved, c.Integrity}
		if done[key] {
			continue
		}
		done[key] = true
		if c.Integrity == "" {
			oldR := quoted("resolved", c.Resolved)
			if !bytes.Contains(out, oldR) {
				return nil, fmt.Errorf("%s: cannot find its resolved URL in the file", c.Key)
			}
			out = bytes.ReplaceAll(out, oldR, quoted("resolved", c.NewResolved))
			continue
		}
		pair := regexp.MustCompile(regexp.QuoteMeta(string(quoted("resolved", c.Resolved))) +
			`(,\s*)` + regexp.QuoteMeta(string(quoted("integrity", c.Integrity))))
		if !pair.Match(out) {
			return nil, fmt.Errorf("%s: cannot find its resolved URL and integrity together in the file", c.Key)
		}
		repl := strings.ReplaceAll(string(quoted("resolved", c.NewResolved)), "$", "$$") + "${1}" +
			strings.ReplaceAll(string(quoted("integrity", c.NewIntegrity)), "$", "$$")
		out = pair.ReplaceAll(out, []byte(repl))
	}
	if !json.Valid(out) {
		return nil, errors.New("the rewritten lockfile is not valid JSON")
	}
	return out, nil
}

// quoted is `"key": "value"` as npm writes it, which escapes no HTML
// characters: a URL with & in it is written with the & as it is.
func quoted(key, value string) []byte {
	return []byte(jsonString(key) + ": " + jsonString(value))
}

func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

// WriteFile replaces the file at path with data, keeping its permissions. The
// new content is written beside it and renamed over it, so an interrupted
// rewrite leaves the old lockfile whole.
func WriteFile(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), info.Mode().Perm())
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
