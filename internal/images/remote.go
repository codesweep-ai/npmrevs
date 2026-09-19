package images

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"golang.org/x/mod/semver"

	"github.com/codesweep-ai/npmrevs/internal/npmpkg"
)

// ErrNotFound marks an image, a tag or a repository the registry does not hold,
// or will not show to the credential in use. The two look alike from outside,
// and both mean there is nothing to read.
var ErrNotFound = errors.New("not found")

// Client reads images from registries.
type Client struct {
	// Getenv reads the token for ghcr.io. Tests replace it.
	Getenv func(string) string
	// UserAgent identifies cs-npmrevs to the registry.
	UserAgent string
	// Transport, when set, carries every request. Tests point it at a registry
	// served in-process.
	Transport http.RoundTripper
}

// keychain answers with a token for ghcr.io when the environment holds one, and
// anonymously everywhere else. It never reads Docker's or podman's credential
// files and never runs a credential helper: what cs-npmrevs sends is decided by
// its own environment.
type keychain struct{ getenv func(string) string }

func (k keychain) Resolve(r authn.Resource) (authn.Authenticator, error) {
	if r.RegistryStr() == "ghcr.io" && k.getenv != nil {
		for _, v := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
			if t := k.getenv(v); t != "" {
				return &authn.Basic{Username: "token", Password: t}, nil
			}
		}
	}
	return authn.Anonymous, nil
}

func (c *Client) options(ctx context.Context) []remote.Option {
	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(keychain{getenv: c.Getenv}),
	}
	if c.UserAgent != "" {
		opts = append(opts, remote.WithUserAgent(c.UserAgent))
	}
	if c.Transport != nil {
		opts = append(opts, remote.WithTransport(c.Transport))
	}
	return opts
}

// Image returns the image ref names.
func (c *Client) Image(ctx context.Context, ref string) (v1.Image, error) {
	r, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		return nil, err
	}
	img, err := remote.Image(r, c.options(ctx)...)
	if err != nil {
		return nil, classify(ref, err)
	}
	return img, nil
}

// Versions returns the versions of name that registry holds images of, lowest
// first. Tags that are not versions are left out.
func (c *Client) Versions(ctx context.Context, registry, pkg string) ([]string, error) {
	repoName, err := Repository(registry, pkg)
	if err != nil {
		return nil, err
	}
	repo, err := name.NewRepository(repoName, name.WeakValidation)
	if err != nil {
		return nil, err
	}
	tags, err := remote.List(repo, c.options(ctx)...)
	if err != nil {
		return nil, classify(repoName, err)
	}
	var versions []string
	for _, t := range tags {
		if npmpkg.ValidVersion(t) {
			versions = append(versions, t)
		}
	}
	SortVersions(versions)
	return versions, nil
}

// SortVersions orders versions by semver precedence, lowest first.
func SortVersions(versions []string) {
	slices.SortFunc(versions, func(a, b string) int { return semver.Compare("v"+a, "v"+b) })
}

// classify turns the registry's refusals into ErrNotFound, and leaves every
// other failure as it is.
func classify(what string, err error) error {
	if terr, ok := errors.AsType[*transport.Error](err); ok {
		switch terr.StatusCode {
		case http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("%s: %w", what, ErrNotFound)
		}
		for _, d := range terr.Errors {
			switch d.Code {
			case transport.NameUnknownErrorCode, transport.ManifestUnknownErrorCode,
				transport.DeniedErrorCode, transport.UnauthorizedErrorCode:
				return fmt.Errorf("%s: %w", what, ErrNotFound)
			}
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// Source is the per-version images of a registry, read lazily for a server:
// a tag list per package, a manifest per version, and a layer only when a
// tarball is asked for.
type Source struct {
	Client   *Client
	Registry string
	// Scopes are the scopes, "@name", whose packages are looked up at all.
	Scopes []string
	// CacheDir holds the tarballs read out of layers.
	CacheDir string
	// TagsTTL is how long a tag list is trusted.
	TagsTTL time.Duration

	mu    sync.Mutex
	tags  map[string]tagList
	metas map[string]*Metadata
}

type tagList struct {
	versions []string
	fetched  time.Time
}

// Covers reports whether pkg's scope is one the source is asked to look up.
func (s *Source) Covers(pkg string) bool {
	return slices.Contains(s.Scopes, npmpkg.Scope(pkg))
}

// Versions returns what the registry holds of pkg, keyed by version. A package
// the registry has no repository for has no versions.
func (s *Source) Versions(ctx context.Context, pkg string) (map[string]*Metadata, error) {
	if !s.Covers(pkg) {
		return nil, nil
	}
	versions, err := s.tagList(ctx, pkg)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*Metadata, len(versions))
	for _, v := range versions {
		md, err := s.metadata(ctx, pkg, v)
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNoMetadata) {
			continue // pruned since the list was read, or an image of something else
		}
		if err != nil {
			return nil, err
		}
		out[v] = md
	}
	return out, nil
}

func (s *Source) tagList(ctx context.Context, pkg string) ([]string, error) {
	s.mu.Lock()
	if tl, ok := s.tags[pkg]; ok && time.Since(tl.fetched) < s.TagsTTL {
		s.mu.Unlock()
		return tl.versions, nil
	}
	s.mu.Unlock()

	versions, err := s.Client.Versions(ctx, s.Registry, pkg)
	if errors.Is(err, ErrNotFound) {
		versions, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.tags == nil {
		s.tags = map[string]tagList{}
	}
	s.tags[pkg] = tagList{versions: versions, fetched: time.Now()}
	s.mu.Unlock()
	return versions, nil
}

// metadata reads one version's image manifest. A tag names one version, which
// is never rebuilt with other bytes, so what it said once it says for good.
func (s *Source) metadata(ctx context.Context, pkg, version string) (*Metadata, error) {
	ref, err := Ref(s.Registry, pkg, version)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	md, ok := s.metas[ref]
	s.mu.Unlock()
	if ok {
		return md, nil
	}
	img, err := s.Client.Image(ctx, ref)
	if err != nil {
		return nil, err
	}
	md, err = ReadMetadata(img)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	if md.Name != pkg || md.Version != version {
		return nil, fmt.Errorf("%s: %w: it says it holds %s@%s", ref, ErrNoMetadata, md.Name, md.Version)
	}
	s.mu.Lock()
	if s.metas == nil {
		s.metas = map[string]*Metadata{}
	}
	s.metas[ref] = md
	s.mu.Unlock()
	return md, nil
}

// Tarball returns the path of pkg@version's tarball in the cache, reading it
// out of its image first when it is not there yet. The bytes are checked
// against the integrity the image declares before anything is kept, and the
// image is read one file at a time until the matching one is found.
func (s *Source) Tarball(ctx context.Context, pkg, version string) (string, error) {
	md, err := s.metadata(ctx, pkg, version)
	if err != nil {
		return "", err
	}
	// A directory per name, so no two packages share a file: "@a/b-c" and
	// "@a-b/c" would under the names npm pack gives them.
	dir := filepath.Join(s.CacheDir, "tarballs", filepath.FromSlash(pkg))
	file := version + ".tgz"
	path := filepath.Join(dir, file)
	if p, err := npmpkg.Read(path); err == nil && p.Integrity == md.Integrity {
		return path, nil
	}
	ref, err := Ref(s.Registry, pkg, version)
	if err != nil {
		return "", err
	}
	img, err := s.Client.Image(ctx, ref)
	if err != nil {
		return "", err
	}
	found := false
	err = ExtractTarballs(img, dir, func(p *npmpkg.Package) (string, error) {
		if p.Integrity != md.Integrity {
			return "", nil
		}
		found = true
		return file, ErrStop
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w", ref, err)
	}
	if !found {
		return "", fmt.Errorf("%s holds no tarball whose integrity is %s", ref, md.Integrity)
	}
	return path, nil
}
