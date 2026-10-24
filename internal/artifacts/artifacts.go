// Package artifacts implements the controlled file store of the Double Agent
// control plane.
//
// Files exported by an instance are copied into the store, hashed, and served
// back through short lived, ownership checked downloads. The store never
// executes or interprets the content it holds.
package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Errors returned by the package.
var (
	// ErrTooLarge is returned when a file exceeds the configured limit.
	ErrTooLarge = errors.New("artifacts: file is too large")
	// ErrInvalidName is returned when a file name is not acceptable.
	ErrInvalidName = errors.New("artifacts: invalid file name")
	// ErrNotFound is returned when an artifact does not exist.
	ErrNotFound = errors.New("artifacts: artifact not found")
)

// safeName matches the accepted file names.
var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// Store is the artifact store on disk.
type Store struct {
	root string
	// maxSize is the largest single file the store accepts.
	maxSize int64
}

// Options configures a Store.
type Options struct {
	// Root is the directory that holds the stored files.
	Root string
	// MaxSize is the largest single file accepted, in bytes.
	MaxSize int64
}

// New creates a Store.
func New(opts Options) (*Store, error) {
	if opts.Root == "" {
		return nil, errors.New("artifacts: root is required")
	}
	if opts.MaxSize <= 0 {
		opts.MaxSize = 1 << 30
	}
	if err := os.MkdirAll(opts.Root, 0o750); err != nil {
		return nil, fmt.Errorf("artifacts: create root: %w", err)
	}
	return &Store{root: opts.Root, maxSize: opts.MaxSize}, nil
}

// Root returns the root directory of the store.
func (s *Store) Root() string { return s.root }

// MaxSize returns the largest file the store accepts, in bytes.
func (s *Store) MaxSize() int64 { return s.maxSize }

// ValidateName checks that a file name is safe to store.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: the name is empty", ErrInvalidName)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("%w: %q contains a path separator", ErrInvalidName, name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidName, name)
	}
	if !safeName.MatchString(name) {
		return fmt.Errorf("%w: %q is not allowed", ErrInvalidName, name)
	}
	return nil
}

// Put stores a file under the given owner and returns its metadata.
//
// The content is written to a temporary file first so a partial write never
// becomes visible as a valid artifact.
func (s *Store) Put(owner, name string, r io.Reader) (Info, error) {
	if err := ValidateName(name); err != nil {
		return Info{}, err
	}
	dir := filepath.Join(s.root, owner)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Info{}, fmt.Errorf("artifacts: create owner directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return Info{}, fmt.Errorf("artifacts: create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	hasher := sha256.New()
	// The limit is enforced while copying so a huge upload never fills the
	// disk before it is rejected.
	limited := io.LimitReader(r, s.maxSize+1)
	written, err := io.Copy(io.MultiWriter(tmp, hasher), limited)
	if err != nil {
		tmp.Close()
		return Info{}, fmt.Errorf("artifacts: write file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Info{}, fmt.Errorf("artifacts: close file: %w", err)
	}
	if written > s.maxSize {
		return Info{}, fmt.Errorf("%w: the limit is %d bytes", ErrTooLarge, s.maxSize)
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	// The name is prefixed with the digest so that two uploads of the same
	// name never overwrite each other silently.
	finalName := digest[:12] + "-" + name
	finalPath := filepath.Join(dir, finalName)
	if err := os.Rename(tmpName, finalPath); err != nil {
		return Info{}, fmt.Errorf("artifacts: publish file: %w", err)
	}
	return Info{
		Name:   name,
		Path:   finalPath,
		Size:   written,
		Digest: digest,
		MIME:   DetectMIME(name),
	}, nil
}

// Import moves a file into the store and returns its metadata.
//
// The file is moved instead of copied, because the two callers of this
// method produce a file the store keeps: the promotion of an upload and the
// export of a file that a workload produced. A file that lives on another
// file system is copied instead, and the source is removed afterwards.
func (s *Store) Import(owner, name, source string) (Info, error) {
	if err := ValidateName(name); err != nil {
		return Info{}, err
	}
	if strings.TrimSpace(source) == "" {
		return Info{}, fmt.Errorf("%w: the source is empty", ErrInvalidName)
	}
	info, err := os.Stat(source)
	if err != nil {
		return Info{}, fmt.Errorf("artifacts: read %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return Info{}, fmt.Errorf("artifacts: %s is not a regular file", source)
	}
	if info.Size() > s.maxSize {
		return Info{}, fmt.Errorf("%w: the limit is %d bytes", ErrTooLarge, s.maxSize)
	}
	digest, err := fileDigest(source)
	if err != nil {
		return Info{}, err
	}
	dir := filepath.Join(s.root, owner)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Info{}, fmt.Errorf("artifacts: create owner directory: %w", err)
	}
	// The name is prefixed with the digest so that two files of the same
	// name never overwrite each other silently.
	finalName := digest[:12] + "-" + name
	finalPath := filepath.Join(dir, finalName)
	if err := os.Rename(source, finalPath); err != nil {
		// A rename across file systems fails. The file is copied and the
		// source is removed, so the store never keeps two copies.
		if err := copyFile(source, finalPath); err != nil {
			return Info{}, fmt.Errorf("artifacts: publish file: %w", err)
		}
		if err := os.Remove(source); err != nil {
			return Info{}, fmt.Errorf("artifacts: remove the source file: %w", err)
		}
	}
	return Info{
		Name:   name,
		Path:   finalPath,
		Size:   info.Size(),
		Digest: digest,
		MIME:   DetectMIME(name),
	}, nil
}

// copyFile copies a file, creating the destination when it is missing.
func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open the source: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return fmt.Errorf("create the destination: %w", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy the content: %w", err)
	}
	return out.Close()
}

// Get returns the metadata of a stored file.
func (s *Store) Get(owner, storedName string) (Info, error) {
	path, err := s.path(owner, storedName)
	if err != nil {
		return Info{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Info{}, fmt.Errorf("%w: %s", ErrNotFound, storedName)
		}
		return Info{}, fmt.Errorf("artifacts: stat file: %w", err)
	}
	digest, err := fileDigest(path)
	if err != nil {
		return Info{}, err
	}
	return Info{
		Name:   displayName(storedName),
		Path:   path,
		Size:   info.Size(),
		Digest: digest,
		MIME:   DetectMIME(storedName),
	}, nil
}

// Open returns a reader over a stored file.
func (s *Store) Open(owner, storedName string) (*os.File, error) {
	path, err := s.path(owner, storedName)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, storedName)
		}
		return nil, fmt.Errorf("artifacts: open file: %w", err)
	}
	return f, nil
}

// Remove deletes a stored file.
func (s *Store) Remove(owner, storedName string) error {
	path, err := s.path(owner, storedName)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, storedName)
		}
		return fmt.Errorf("artifacts: remove file: %w", err)
	}
	return nil
}

// path resolves a stored file name inside the owner directory, refusing any
// path that would escape it.
func (s *Store) path(owner, storedName string) (string, error) {
	if owner == "" || storedName == "" {
		return "", fmt.Errorf("%w: the owner or name is empty", ErrInvalidName)
	}
	if strings.ContainsAny(storedName, "/\\") || strings.Contains(storedName, "..") {
		return "", fmt.Errorf("%w: %q is not allowed", ErrInvalidName, storedName)
	}
	// The current directory would resolve to the directory of the owner
	// itself, which is not a file of the store.
	if storedName == "." {
		return "", fmt.Errorf("%w: %q is not allowed", ErrInvalidName, storedName)
	}
	dir := filepath.Join(s.root, owner)
	full := filepath.Join(dir, storedName)
	// The join above cannot escape because the name has no separator, but the
	// check is kept as a defence against a future change of the rules.
	rel, err := filepath.Rel(dir, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%w: %q escapes the store", ErrInvalidName, storedName)
	}
	return full, nil
}

// Info describes a stored file.
type Info struct {
	// Name is the display name of the file.
	Name string `json:"name"`
	// Path is the location on disk.
	Path string `json:"-"`
	// Size is the length in bytes.
	Size int64 `json:"size"`
	// Digest is the SHA-256 of the content, hex encoded.
	Digest string `json:"digest"`
	// MIME is the detected content type.
	MIME string `json:"mime"`
}

// fileDigest computes the SHA-256 of a file.
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("artifacts: open for hashing: %w", err)
	}
	defer f.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", fmt.Errorf("artifacts: hash file: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// displayName strips the digest prefix that Put adds to the stored name.
func displayName(storedName string) string {
	if i := strings.IndexByte(storedName, '-'); i == 12 {
		return storedName[i+1:]
	}
	return storedName
}

// DetectMIME guesses the content type of a file from its name.
func DetectMIME(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".log", ".md":
		return "text/plain; charset=utf-8"
	case ".json":
		return "application/json"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".csv":
		return "text/csv; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".tar":
		return "application/x-tar"
	case ".gz":
		return "application/gzip"
	case ".zip":
		return "application/zip"
	case ".sh":
		return "application/x-sh"
	case ".py":
		return "text/x-python; charset=utf-8"
	case ".yaml", ".yml":
		return "application/yaml"
	default:
		return "application/octet-stream"
	}
}

// Expiry returns the retention deadline of an artifact.
func Expiry(retention time.Duration, from time.Time) *time.Time {
	if retention <= 0 {
		return nil
	}
	deadline := from.Add(retention)
	return &deadline
}
