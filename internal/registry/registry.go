// Package registry serves the read half of the npm registry API from local
// tarballs, and from an upstream registry for everything else.
//
// A package with no local version is the upstream's, byte for byte. A package
// with one gets a packument built on request: the upstream's document with
// every local version written into it, a local version taking the place of a
// published one with the same number. Nothing is precomputed, so nothing goes
// stale.
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"github.com/codesweep-ai/npmrevs/internal/datadir"
	"github.com/codesweep-ai/npmrevs/internal/images"
	"github.com/codesweep-ai/npmrevs/internal/npmpkg"
)

// Config is what a Server serves from.
type Config struct {
	// Index holds the local tarballs.
	Index *datadir.Index
	// Images, when set, is a registry of per-version images read for the scopes
	// it covers.
	Images *images.Source
	// Upstream is the registry every other package comes from.
	Upstream *url.URL
	// Strict lists scopes, "@name", served from local versions only.
	Strict []string
	// Version is the cs-npmrevs version, reported by /-/npmrevs and in responses.
	Version string
	// Client carries requests to the upstream.
	Client *http.Client
	// UpstreamTTL is how long an upstream packument that was merged with local
	// versions is reused.
	UpstreamTTL time.Duration
	Log         *slog.Logger
}

// Server is the registry.
type Server struct {
	cfg Config
	mux *http.ServeMux

	mu    sync.Mutex
	cache map[string]cachedDoc
}

type cachedDoc struct {
	doc     *upstreamDoc
	fetched time.Time
}

// upstreamDoc is a packument as the upstream answered it. A nil *upstreamDoc
// means the upstream has no such package.
type upstreamDoc struct {
	contentType string
	body        []byte
}

// New returns a server for cfg.
func New(cfg Config) *Server {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 2 * time.Minute}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux(), cache: map[string]cachedDoc{}}
	s.mux.HandleFunc("GET /-/ping", s.ping)
	s.mux.HandleFunc("GET /-/npmrevs", s.status)
	s.mux.HandleFunc("POST /-/npm/v1/security/{rest...}", s.forward)
	s.mux.HandleFunc("GET /{name}", func(w http.ResponseWriter, r *http.Request) {
		s.packument(w, r, r.PathValue("name"))
	})
	s.mux.HandleFunc("GET /{scope}/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.PathValue("scope"), "@") {
			notFound(w)
			return
		}
		s.packument(w, r, r.PathValue("scope")+"/"+r.PathValue("name"))
	})
	s.mux.HandleFunc("GET /{name}/-/{file}", func(w http.ResponseWriter, r *http.Request) {
		s.tarball(w, r, r.PathValue("name"), r.PathValue("file"))
	})
	s.mux.HandleFunc("GET /{scope}/{name}/-/{file}", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.PathValue("scope"), "@") {
			notFound(w)
			return
		}
		s.tarball(w, r, r.PathValue("scope")+"/"+r.PathValue("name"), r.PathValue("file"))
	})
	s.mux.HandleFunc("/", s.fallback)
	return s
}

// ServeHTTP answers one request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	rec.Header().Set("Server", "cs-npmrevs/"+s.cfg.Version)
	s.mux.ServeHTTP(rec, r)
	s.cfg.Log.Debug("request", "method", r.Method, "path", r.URL.EscapedPath(),
		"status", rec.status, "took", time.Since(start).Round(time.Millisecond))
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) ping(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{})
}

// Status is what GET /-/npmrevs answers: enough for a script to tell this server
// from another program on the port, and to see what it serves.
type Status struct {
	Server  string `json:"server"`
	Version string `json:"version"`
	// PID is the server's own process, which a script that started it through
	// a launcher cannot otherwise know.
	PID          int      `json:"pid"`
	Data         []string `json:"data"`
	Upstream     string   `json:"upstream"`
	Images       string   `json:"images,omitempty"`
	ImagesScopes []string `json:"imagesScopes,omitempty"`
	Strict       []string `json:"strict,omitempty"`
	Packages     int      `json:"packages"`
	Versions     int      `json:"versions"`
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	names, versions := s.cfg.Index.Count()
	st := Status{
		Server:   "cs-npmrevs",
		Version:  s.cfg.Version,
		PID:      os.Getpid(),
		Data:     s.cfg.Index.Dirs(),
		Upstream: s.cfg.Upstream.String(),
		Strict:   s.cfg.Strict,
		Packages: names,
		Versions: versions,
	}
	if s.cfg.Images != nil {
		st.Images, st.ImagesScopes = s.cfg.Images.Registry, s.cfg.Images.Scopes
	}
	writeJSON(w, http.StatusOK, st)
}

// fallback answers what no route matched. A write is refused by name, so an
// `npm publish` pointed here says why it failed.
func (s *Server) fallback(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		notFound(w)
		return
	}
	writeError(w, http.StatusMethodNotAllowed, fmt.Sprintf(
		"cs-npmrevs serves the read half of the registry API, and %s is not part of it. "+
			"A package reaches it as a tarball in a data directory, not through npm publish.", r.Method))
}

func (s *Server) strict(name string) bool {
	return slices.Contains(s.cfg.Strict, npmpkg.Scope(name))
}

// packument answers GET /<name>.
func (s *Server) packument(w http.ResponseWriter, r *http.Request, name string) {
	if !npmpkg.RoutableName(name) {
		notFound(w)
		return
	}
	entries, times, err := s.localEntries(r, name)
	var conflict *datadir.ConflictError
	switch {
	case errors.As(err, &conflict):
		writeError(w, http.StatusInternalServerError, "cs-npmrevs: "+err.Error())
		return
	case err != nil:
		s.cfg.Log.Warn("the images registry did not answer", "package", name, "err", err)
		writeError(w, http.StatusServiceUnavailable, "cs-npmrevs: the images registry did not answer: "+err.Error())
		return
	}

	strict := s.strict(name)
	if len(entries) == 0 {
		if strict {
			writeError(w, http.StatusNotFound, fmt.Sprintf(
				"cs-npmrevs: %s has no local version, and %s is served from local versions only (--strict)",
				name, npmpkg.Scope(name)))
			return
		}
		s.passThrough(w, r, name)
		return
	}

	var up *upstreamDoc
	if !strict {
		up, err = s.upstreamPackument(r.Context(), name, accept(r))
		if err != nil {
			s.cfg.Log.Warn("the upstream registry did not answer", "package", name, "err", err)
			writeError(w, http.StatusServiceUnavailable, fmt.Sprintf(
				"cs-npmrevs: %s has local versions, and the upstream registry did not answer for the rest: %v", name, err))
			return
		}
	}
	var body []byte
	contentType := "application/json"
	if up != nil {
		body, contentType = up.body, up.contentType
	}
	doc, err := merge(name, body, entries, times)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("cs-npmrevs: the upstream packument for %s: %v", name, err))
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc)
}

// localEntries returns the version objects of every local version of name, from
// the images first and the data directories over them, with the times the
// packument reports for them.
func (s *Server) localEntries(r *http.Request, name string) (map[string]json.RawMessage, map[string]time.Time, error) {
	base := "http://" + r.Host
	entries := map[string]json.RawMessage{}
	times := map[string]time.Time{}

	if s.cfg.Images != nil {
		metas, err := s.cfg.Images.Versions(r.Context(), name)
		if err != nil {
			return nil, nil, err
		}
		for v, md := range metas {
			e, err := withTarball(md, base+"/"+npmpkg.TarballPath(name, v))
			if err != nil {
				s.cfg.Log.Warn("skipping an image whose npm metadata does not parse", "package", name, "version", v, "err", err)
				continue
			}
			entries[v] = e
		}
	}

	local, err := s.cfg.Index.Versions(name)
	if err != nil {
		return nil, nil, err
	}
	for v, p := range local {
		e, err := json.Marshal(p.Entry(base))
		if err != nil {
			return nil, nil, err
		}
		entries[v] = e
		times[v] = p.ModTime
	}
	return entries, times, nil
}

// withTarball is the version object an image carried, with dist.tarball set to
// where this server serves it. The integrity and shasum are the ones the image
// declares, which are the ones its tarball is checked against before it is
// served, whatever the carried object says.
func withTarball(md *images.Metadata, tarball string) (json.RawMessage, error) {
	e := map[string]json.RawMessage{}
	if err := json.Unmarshal(md.Entry, &e); err != nil || e == nil {
		return nil, fmt.Errorf("the version object is not a JSON object: %.40s", md.Entry)
	}
	dist := map[string]any{}
	if raw, ok := e["dist"]; ok {
		if err := json.Unmarshal(raw, &dist); err != nil || dist == nil {
			return nil, errors.New("its dist is not a JSON object")
		}
	}
	dist["integrity"], dist["shasum"] = md.Integrity, md.Shasum
	dist["tarball"] = tarball
	d, err := json.Marshal(dist)
	if err != nil {
		return nil, err
	}
	e["dist"] = d
	return json.Marshal(e)
}

// merge writes the local versions into the upstream packument, or into an
// empty one when the upstream has none, and computes the dist-tags.
func merge(name string, upstream []byte, entries map[string]json.RawMessage, times map[string]time.Time) ([]byte, error) {
	doc := map[string]json.RawMessage{}
	if upstream != nil {
		if err := json.Unmarshal(upstream, &doc); err != nil {
			return nil, err
		}
	}
	versions := map[string]json.RawMessage{}
	tags := map[string]string{}
	if err := unmarshalField(doc, "versions", &versions); err != nil {
		return nil, err
	}
	if err := unmarshalField(doc, "dist-tags", &tags); err != nil {
		return nil, err
	}
	maps.Copy(versions, entries)
	if latest := HighestRelease(slices.Collect(maps.Keys(versions))); latest != "" {
		tags["latest"] = latest
	}

	set := func(key string, v any) error {
		raw, err := json.Marshal(v)
		doc[key] = raw
		return err
	}
	if _, ok := doc["name"]; !ok {
		if err := set("name", name); err != nil {
			return nil, err
		}
	}
	if err := set("versions", versions); err != nil {
		return nil, err
	}
	if err := set("dist-tags", tags); err != nil {
		return nil, err
	}

	newest := time.Time{}
	for _, t := range times {
		if t.After(newest) {
			newest = t
		}
	}
	// A full document dates each version under `time`, an abbreviated one only
	// says when the package last changed. Each gets the local versions in the
	// shape it already has.
	if _, full := doc["time"]; full || upstream == nil {
		tm := map[string]json.RawMessage{}
		if err := unmarshalField(doc, "time", &tm); err != nil {
			return nil, err
		}
		for v, t := range times {
			tm[v] = stamp(t)
		}
		if !newest.IsZero() {
			tm["modified"] = laterStamp(tm["modified"], newest)
			if _, ok := tm["created"]; !ok {
				tm["created"] = stamp(oldest(times))
			}
		}
		if err := set("time", tm); err != nil {
			return nil, err
		}
	}
	if _, abbreviated := doc["modified"]; (abbreviated || upstream == nil) && !newest.IsZero() {
		doc["modified"] = laterStamp(doc["modified"], newest)
	}
	return json.Marshal(doc)
}

func unmarshalField(doc map[string]json.RawMessage, key string, into any) error {
	raw, ok := doc[key]
	if !ok || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

// stamp formats t the way the npm registry writes times.
func stamp(t time.Time) json.RawMessage {
	b, _ := json.Marshal(t.UTC().Format("2006-01-02T15:04:05.000Z"))
	return b
}

// laterStamp returns whichever of an existing time and t is later.
func laterStamp(existing json.RawMessage, t time.Time) json.RawMessage {
	var s string
	if json.Unmarshal(existing, &s) == nil {
		if prev, err := time.Parse(time.RFC3339, s); err == nil && prev.After(t) {
			return existing
		}
	}
	return stamp(t)
}

func oldest(times map[string]time.Time) time.Time {
	var o time.Time
	for _, t := range times {
		if o.IsZero() || t.Before(o) {
			o = t
		}
	}
	return o
}

// HighestRelease returns the highest version that is not a prerelease, or ""
// when there is none. It is what `latest` points at: the version npmjs.com
// would have tagged had every one of them been published there.
func HighestRelease(versions []string) string {
	best := ""
	for _, v := range versions {
		sv := "v" + v
		if !npmpkg.ValidVersion(v) || semver.Prerelease(sv) != "" {
			continue
		}
		if best == "" || semver.Compare(sv, "v"+best) > 0 {
			best = v
		}
	}
	return best
}

// accept is the representation the client asked for, passed to the upstream
// so an install gets the abbreviated document and `npm view` the full one.
func accept(r *http.Request) string {
	if a := r.Header.Get("Accept"); a != "" {
		return a
	}
	return "application/json"
}

// upstreamURL is the upstream address of a path that is already escaped. It is
// joined as text, so an escaped slash in a scoped name reaches the upstream as
// the client sent it.
func (s *Server) upstreamURL(escapedPath, rawQuery string) string {
	u := strings.TrimSuffix(s.cfg.Upstream.String(), "/") + escapedPath
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	return u
}

// escapeName writes a package name the way npm puts it in a URL: the scope's
// slash escaped, the at sign left alone.
func escapeName(name string) string {
	return "/" + strings.Replace(url.PathEscape(name), "%40", "@", 1)
}

// upstreamPackument fetches name's packument from the upstream, from the cache
// while it is fresh. A nil document with a nil error means the upstream has no
// such package.
func (s *Server) upstreamPackument(ctx context.Context, name, acceptHeader string) (*upstreamDoc, error) {
	key := name + "\x00" + acceptHeader
	s.mu.Lock()
	c, ok := s.cache[key]
	s.mu.Unlock()
	if ok && time.Since(c.fetched) < s.cfg.UpstreamTTL {
		return c.doc, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.upstreamURL(escapeName(name), ""), http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("User-Agent", "cs-npmrevs/"+s.cfg.Version)
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var doc *upstreamDoc
	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		doc = &upstreamDoc{contentType: ct, body: body}
	case http.StatusNotFound:
		// A package nobody published upstream is the normal case for a local
		// one, not an error.
	default:
		return nil, fmt.Errorf("%s answered %s", req.URL.Redacted(), resp.Status)
	}
	s.mu.Lock()
	s.cache[key] = cachedDoc{doc: doc, fetched: time.Now()}
	s.mu.Unlock()
	return doc, nil
}

// passThrough streams the upstream's answer for a package with no local
// version, untouched apart from the caching headers.
func (s *Server) passThrough(w http.ResponseWriter, r *http.Request, name string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.upstreamURL(escapeName(name), r.URL.RawQuery), http.NoBody)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Accept", accept(r))
	req.Header.Set("User-Agent", "cs-npmrevs/"+s.cfg.Version)
	for _, h := range []string{"If-None-Match", "If-Modified-Since", "Npm-Command", "Npm-Session", "Npm-Scope"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		s.cfg.Log.Warn("the upstream registry did not answer", "package", name, "err", err)
		writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("cs-npmrevs: the upstream registry did not answer for %s: %v", name, err))
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// tarball answers GET /<name>/-/<file>.
func (s *Server) tarball(w http.ResponseWriter, r *http.Request, name, file string) {
	if !npmpkg.RoutableName(name) {
		notFound(w)
		return
	}
	version, ok := strings.CutPrefix(file, npmpkg.Bare(name)+"-")
	version, isTgz := strings.CutSuffix(version, ".tgz")
	if ok && isTgz && npmpkg.ValidVersion(version) {
		served, err := s.serveLocal(w, r, name, version)
		if err != nil {
			// A tarball the images registry could not hand over is a bad
			// gateway; a conflict or an unreadable file is this server's own.
			status := http.StatusInternalServerError
			if errors.Is(err, errImages) {
				status = http.StatusBadGateway
			}
			writeError(w, status, fmt.Sprintf("cs-npmrevs: %s@%s: %v", name, version, err))
			return
		}
		if served {
			return
		}
	}
	if s.strict(name) {
		writeError(w, http.StatusNotFound, fmt.Sprintf(
			"cs-npmrevs: no local tarball %s, and %s is served from local versions only (--strict)", file, npmpkg.Scope(name)))
		return
	}
	// The client came here for the upstream's tarball because npm rewrites the
	// upstream's host to the registry it was given. Sending it on keeps the
	// bytes off this server.
	http.Redirect(w, r, s.upstreamURL(r.URL.EscapedPath(), r.URL.RawQuery), http.StatusFound)
}

// serveLocal writes name@version's tarball when a data directory or an image
// holds it, and reports whether it did.
func (s *Server) serveLocal(w http.ResponseWriter, r *http.Request, name, version string) (bool, error) {
	local, err := s.cfg.Index.Versions(name)
	if err != nil {
		return false, err
	}
	path := ""
	if p, ok := local[version]; ok {
		// A file rewritten since the index last read it would be served
		// under the integrity of its old bytes, so it is read again first.
		if info, err := os.Stat(p.Path); err == nil && (info.Size() != p.Size || !info.ModTime().Equal(p.ModTime)) {
			s.cfg.Index.Refresh()
			if local, err = s.cfg.Index.Versions(name); err != nil {
				return false, err
			}
			if p, ok = local[version]; !ok {
				return false, nil
			}
		}
		path = p.Path
	} else if s.cfg.Images != nil && s.cfg.Images.Covers(name) {
		path, err = s.cfg.Images.Tarball(r.Context(), name, version)
		if errors.Is(err, images.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("%w: %w", errImages, err)
		}
	}
	if path == "" {
		return false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", info.ModTime(), f)
	return true, nil
}

// errImages marks a failure reading a tarball out of the images registry.
var errImages = errors.New("the images registry")

// forward relays an audit request to the upstream, so `npm audit` and the
// audit an install runs keep working.
func (s *Server) forward(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.upstreamURL(r.URL.EscapedPath(), r.URL.RawQuery), bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, h := range []string{"Content-Type", "Content-Encoding", "Accept"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "cs-npmrevs: the upstream registry did not answer: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if v := resp.Header.Get("Content-Type"); v != "" {
		w.Header().Set("Content-Type", v)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func notFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not found")
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
