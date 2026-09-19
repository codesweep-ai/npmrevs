package images

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// Format is how an image is written to a file.
type Format string

const (
	// OCI is an OCI image layout in a tar file: `oci-archive:` to podman. It
	// keeps the manifest, and with it the annotations.
	OCI Format = "oci"
	// Docker is the format `docker save` writes: `docker-archive:` to podman,
	// and what `docker load` reads. It keeps the config and its labels, and
	// drops the manifest annotations.
	Docker Format = "docker"
)

// ParseFormat reads a format named on the command line.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case OCI, Docker:
		return Format(s), nil
	}
	return "", fmt.Errorf("unknown archive format %q: use oci or docker", s)
}

// WriteArchive writes img to path in the given format, recording ref as the
// name it is loaded under. The file is written beside path and renamed into
// place, so a reader never finds half of it.
func WriteArchive(path string, img v1.Image, ref string, format Format) error {
	tag, err := name.NewTag(ref, name.WeakValidation)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cs-npmrevs-*.tar")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	switch format {
	case Docker:
		err = tarball.Write(tag, img, tmp)
	case OCI:
		err = writeOCI(tmp, img, tag)
	default:
		err = fmt.Errorf("unknown archive format %q", format)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// CreateTemp makes the file private; an archive is meant to be read.
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// writeOCI writes an OCI image layout into w as a tar stream. The layout is
// built in a scratch directory and then archived with fixed ownership, modes
// and times, so the same image always gives the same bytes.
func writeOCI(w io.Writer, img v1.Image, tag name.Tag) error {
	dir, err := os.MkdirTemp("", "cs-npmrevs-layout-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	p, err := layout.Write(dir, empty.Index)
	if err != nil {
		return err
	}
	err = p.AppendImage(img, layout.WithAnnotations(map[string]string{
		// The tag, which is what podman's oci-archive:FILE:NAME matches, and the
		// full reference, which is what a containerd-backed `docker load` names
		// the image.
		"org.opencontainers.image.ref.name": tag.TagStr(),
		"io.containerd.image.name":          tag.Name(),
	}))
	if err != nil {
		return err
	}
	return tarDir(w, dir)
}

// tarDir archives every file under dir, in a fixed order, with fixed metadata.
func tarDir(w io.Writer, dir string) error {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != dir {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	slices.Sort(paths)
	tw := tar.NewWriter(w)
	for _, p := range paths {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: filepath.ToSlash(rel), ModTime: Epoch, Format: tar.FormatPAX}
		if info.IsDir() {
			hdr.Typeflag, hdr.Name, hdr.Mode = tar.TypeDir, hdr.Name+"/", 0o755
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			continue
		}
		hdr.Typeflag, hdr.Mode, hdr.Size = tar.TypeReg, 0o644, info.Size()
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if err := copyFile(tw, p); err != nil {
			return err
		}
	}
	return tw.Close()
}

func copyFile(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// Archive is the images read out of a file. Close releases what reading them
// needed on disk.
type Archive struct {
	Images  []v1.Image
	cleanup func()
}

// Close removes the scratch copy an OCI archive was unpacked into. The images
// read their blobs lazily, so they are unusable afterwards.
func (a *Archive) Close() {
	if a.cleanup != nil {
		a.cleanup()
	}
}

// OpenArchive reads every image in an archive: an OCI archive, a `docker save`
// archive, or an OCI image layout directory.
func OpenArchive(path string) (*Archive, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		imgs, err := imagesInLayout(path)
		if err != nil {
			return nil, err
		}
		return &Archive{Images: imgs}, nil
	}
	members, err := archiveMembers(path)
	if err != nil {
		return nil, err
	}
	// A `docker save` from a containerd-backed engine writes both files. The
	// OCI half keeps the annotations, so it is the one read.
	switch {
	case members["index.json"]:
		return openOCIArchive(path)
	case members["manifest.json"]:
		imgs, err := openDockerArchive(path)
		if err != nil {
			return nil, err
		}
		return &Archive{Images: imgs}, nil
	}
	return nil, fmt.Errorf("%s is neither an OCI archive nor a docker archive: it has no index.json and no manifest.json", path)
}

// archiveMembers lists the top-level files of a tar archive.
func archiveMembers(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	members := map[string]bool{}
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return members, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s is not a tar archive: %w", path, err)
		}
		members[strings.TrimPrefix(h.Name, "./")] = true
	}
}

func openDockerArchive(path string) ([]v1.Image, error) {
	opener := func() (io.ReadCloser, error) { return os.Open(path) }
	manifest, err := tarball.LoadManifest(opener)
	if err != nil {
		return nil, err
	}
	if len(manifest) == 1 {
		img, err := tarball.Image(opener, nil)
		if err != nil {
			return nil, err
		}
		return []v1.Image{img}, nil
	}
	var imgs []v1.Image
	for _, d := range manifest {
		if len(d.RepoTags) == 0 {
			continue
		}
		tag, err := name.NewTag(d.RepoTags[0], name.WeakValidation)
		if err != nil {
			return nil, err
		}
		img, err := tarball.Image(opener, &tag)
		if err != nil {
			return nil, err
		}
		imgs = append(imgs, img)
	}
	if len(imgs) == 0 {
		return nil, fmt.Errorf("%s holds several images and none of them is tagged", path)
	}
	return imgs, nil
}

func openOCIArchive(path string) (*Archive, error) {
	dir, err := os.MkdirTemp("", "cs-npmrevs-archive-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	if err := untar(path, dir); err != nil {
		cleanup()
		return nil, err
	}
	imgs, err := imagesInLayout(dir)
	if err != nil {
		cleanup()
		return nil, err
	}
	return &Archive{Images: imgs, cleanup: cleanup}, nil
}

// untar extracts an archive into dir, refusing any member that would land
// outside it.
func untar(path, dir string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		rel := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(h.Name, "./")))
		if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		target := filepath.Join(dir, rel)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeMember(target, tr); err != nil {
				return err
			}
		}
	}
}

func writeMember(target string, r io.Reader) error {
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// imagesInLayout returns every image an OCI layout's index names, following
// nested indexes.
func imagesInLayout(dir string) ([]v1.Image, error) {
	idx, err := layout.ImageIndexFromPath(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	return imagesInIndex(idx)
}

func imagesInIndex(idx v1.ImageIndex) ([]v1.Image, error) {
	m, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	var imgs []v1.Image
	for _, d := range m.Manifests {
		switch {
		case d.MediaType.IsImage():
			img, err := idx.Image(d.Digest)
			if err != nil {
				return nil, err
			}
			imgs = append(imgs, img)
		case d.MediaType.IsIndex():
			child, err := idx.ImageIndex(d.Digest)
			if err != nil {
				return nil, err
			}
			more, err := imagesInIndex(child)
			if err != nil {
				return nil, err
			}
			imgs = append(imgs, more...)
		}
	}
	if len(imgs) == 0 {
		return nil, errors.New("the index names no image")
	}
	return imgs, nil
}
