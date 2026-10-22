package artifacts_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/artifacts"
)

// newStore creates an artifact store in a temporary directory.
func newStore(t *testing.T, maxSize int64) *artifacts.Store {
	t.Helper()
	store, err := artifacts.New(artifacts.Options{
		Root:    filepath.Join(t.TempDir(), "artifacts"),
		MaxSize: maxSize,
	})
	if err != nil {
		t.Fatalf("create the store: %v", err)
	}
	return store
}

// TestPutAndGetRoundTrip checks that a stored file can be read back.
func TestPutAndGetRoundTrip(t *testing.T) {
	store := newStore(t, 1<<20)
	const body = "the content of the artifact"

	info, err := store.Put("usr_1", "notes.txt", strings.NewReader(body))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if info.Name != "notes.txt" {
		t.Errorf("the artifact is named %q, want notes.txt", info.Name)
	}
	if info.Size != int64(len(body)) {
		t.Errorf("the artifact holds %d bytes, want %d", info.Size, len(body))
	}
	if info.MIME != "text/plain; charset=utf-8" {
		t.Errorf("the artifact carries the type %q", info.MIME)
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Fatalf("the artifact was not written: %v", err)
	}

	storedName := filepath.Base(info.Path)
	got, err := store.Get("usr_1", storedName)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "notes.txt" {
		t.Errorf("the stored artifact is named %q, want notes.txt", got.Name)
	}
	if got.Digest != info.Digest {
		t.Errorf("the digest changed: %q and %q", got.Digest, info.Digest)
	}
	if got.Size != info.Size {
		t.Errorf("the size changed: %d and %d", got.Size, info.Size)
	}

	file, err := store.Open("usr_1", storedName)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(file); err != nil {
		t.Fatalf("read: %v", err)
	}
	if buf.String() != body {
		t.Errorf("the content is %q, want %q", buf.String(), body)
	}
}

// TestTheDigestIsTheHashOfTheContent checks the digest of a stored file.
func TestTheDigestIsTheHashOfTheContent(t *testing.T) {
	store := newStore(t, 1<<20)
	const body = "the content of the artifact"
	sum := sha256.Sum256([]byte(body))

	info, err := store.Put("usr_1", "notes.txt", strings.NewReader(body))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if info.Digest != hex.EncodeToString(sum[:]) {
		t.Errorf("the digest is %q, want %q", info.Digest, hex.EncodeToString(sum[:]))
	}
	// The stored name carries the beginning of the digest so that two
	// uploads of the same name never collide.
	if !strings.HasPrefix(filepath.Base(info.Path), info.Digest[:12]) {
		t.Errorf("the stored name %q does not carry the digest", filepath.Base(info.Path))
	}
}

// TestAFileOverTheLimitIsRejected checks that the size limit is enforced.
func TestAFileOverTheLimitIsRejected(t *testing.T) {
	store := newStore(t, 10)
	_, err := store.Put("usr_1", "big.txt", strings.NewReader("01234567890"))
	if !errors.Is(err, artifacts.ErrTooLarge) {
		t.Fatalf("a file over the limit returned %v, want a file that is too large", err)
	}
	// A file of exactly the limit is accepted.
	if _, err := store.Put("usr_1", "exact.txt", strings.NewReader("0123456789")); err != nil {
		t.Errorf("a file of the limit was refused: %v", err)
	}
}

// TestAPathTraversalIsRejected checks that a name cannot leave the store.
func TestAPathTraversalIsRejected(t *testing.T) {
	store := newStore(t, 1<<20)
	names := []string{
		"../../etc/passwd",
		"..",
		".",
		"sub/dir.txt",
		`sub\dir.txt`,
		"",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			_, err := store.Put("usr_1", name, strings.NewReader("body"))
			if !errors.Is(err, artifacts.ErrInvalidName) {
				t.Errorf("the name %q returned %v, want an invalid name", name, err)
			}
			if _, err := store.Get("usr_1", name); !errors.Is(err, artifacts.ErrInvalidName) {
				t.Errorf("the name %q returned %v, want an invalid name", name, err)
			}
			if err := store.Remove("usr_1", name); !errors.Is(err, artifacts.ErrInvalidName) {
				t.Errorf("the name %q returned %v, want an invalid name", name, err)
			}
		})
	}
}

// TestTheStoredFilesCanBeListed checks the layout of the store: the files of
// an owner live in a directory of their own.
func TestTheStoredFilesCanBeListed(t *testing.T) {
	store := newStore(t, 1<<20)
	first, err := store.Put("usr_1", "one.txt", strings.NewReader("one"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	second, err := store.Put("usr_1", "two.txt", strings.NewReader("two"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := store.Put("usr_2", "other.txt", strings.NewReader("other")); err != nil {
		t.Fatalf("put: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(store.Root(), "usr_1"))
	if err != nil {
		t.Fatalf("list the owner directory: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("the owner holds %d files, want 2", len(entries))
	}
	names := map[string]bool{}
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	for _, info := range []artifacts.Info{first, second} {
		if !names[filepath.Base(info.Path)] {
			t.Errorf("the listing does not hold %q", filepath.Base(info.Path))
		}
	}
	// The files of another owner are not part of the listing.
	other, err := os.ReadDir(filepath.Join(store.Root(), "usr_2"))
	if err != nil {
		t.Fatalf("list the owner directory: %v", err)
	}
	if len(other) != 1 {
		t.Errorf("the owner holds %d files, want 1", len(other))
	}
}

// TestRemoveDeletesTheFile checks the removal of a stored file.
func TestRemoveDeletesTheFile(t *testing.T) {
	store := newStore(t, 1<<20)
	info, err := store.Put("usr_1", "notes.txt", strings.NewReader("body"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	storedName := filepath.Base(info.Path)
	if err := store.Remove("usr_1", storedName); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(info.Path); !os.IsNotExist(err) {
		t.Errorf("the file still exists: %v", err)
	}
	if _, err := store.Get("usr_1", storedName); !errors.Is(err, artifacts.ErrNotFound) {
		t.Errorf("the removed artifact is still found: %v", err)
	}
	if err := store.Remove("usr_1", storedName); !errors.Is(err, artifacts.ErrNotFound) {
		t.Errorf("removing a missing file returned %v, want a missing file", err)
	}
}

// TestAnUnknownArtifactIsReported checks the lookup of a file that was never
// stored.
func TestAnUnknownArtifactIsReported(t *testing.T) {
	store := newStore(t, 1<<20)
	if _, err := store.Get("usr_1", "missing.txt"); !errors.Is(err, artifacts.ErrNotFound) {
		t.Errorf("the lookup returned %v, want a missing file", err)
	}
	if _, err := store.Open("usr_1", "missing.txt"); !errors.Is(err, artifacts.ErrNotFound) {
		t.Errorf("the open returned %v, want a missing file", err)
	}
}

// TestValidateName checks the names the store accepts.
func TestValidateName(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{name: "notes.txt"},
		{name: "a-file_1.bin"},
		{name: "A"},
		{name: "", wantErr: true},
		{name: ".hidden", wantErr: true},
		{name: "-leading", wantErr: true},
		{name: "with space.txt", wantErr: true},
		{name: "with/slash.txt", wantErr: true},
		{name: "..", wantErr: true},
		{name: strings.Repeat("a", 129), wantErr: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := artifacts.ValidateName(test.name)
			if test.wantErr && err == nil {
				t.Errorf("the name %q was accepted", test.name)
			}
			if !test.wantErr && err != nil {
				t.Errorf("the name %q was refused: %v", test.name, err)
			}
		})
	}
}

// TestDetectMIME checks the content type of a file name.
func TestDetectMIME(t *testing.T) {
	cases := map[string]string{
		"notes.txt":   "text/plain; charset=utf-8",
		"data.json":   "application/json",
		"page.html":   "text/html; charset=utf-8",
		"table.csv":   "text/csv; charset=utf-8",
		"image.png":   "image/png",
		"image.JPG":   "image/jpeg",
		"archive.zip": "application/zip",
		"unknown.xyz": "application/octet-stream",
	}
	for name, want := range cases {
		if got := artifacts.DetectMIME(name); got != want {
			t.Errorf("the type of %q is %q, want %q", name, got, want)
		}
	}
}

// TestExpiry checks the retention deadline of an artifact.
func TestExpiry(t *testing.T) {
	if got := artifacts.Expiry(0, time.Now()); got != nil {
		t.Errorf("an artifact without a retention has the deadline %v", got)
	}
	from := time.Now().UTC()
	got := artifacts.Expiry(time.Hour, from)
	if got == nil {
		t.Fatal("an artifact with a retention has no deadline")
	}
	if !got.Equal(from.Add(time.Hour)) {
		t.Errorf("the deadline is %s, want %s", got, from.Add(time.Hour))
	}
}

// TestANewStoreNeedsARoot checks the construction of a store.
func TestANewStoreNeedsARoot(t *testing.T) {
	if _, err := artifacts.New(artifacts.Options{}); err == nil {
		t.Error("a store without a root was created")
	}
}
