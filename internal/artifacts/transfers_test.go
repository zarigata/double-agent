package artifacts_test

import (
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

// newUploads returns an upload manager with a temporary directory.
func newUploads(t *testing.T, now func() time.Time) (*artifacts.Uploads, *artifacts.Store) {
	t.Helper()
	store := newStore(t, 1<<20)
	uploads, err := artifacts.NewUploads(artifacts.UploadOptions{
		Root:     filepath.Join(t.TempDir(), "uploads"),
		Store:    store,
		MaxBytes: 1 << 20,
		TTL:      time.Hour,
		Clock:    now,
	})
	if err != nil {
		t.Fatalf("create the upload manager: %v", err)
	}
	return uploads, store
}

// TestAnUploadIsAnnouncedAndCompleted walks through the whole upload.
func TestAnUploadIsAnnouncedAndCompleted(t *testing.T) {
	uploads, store := newUploads(t, nil)
	const body = "the source of the workload"
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])

	upload, err := uploads.Begin("usr_1", "main.go", "text/plain", int64(len(body)))
	if err != nil {
		t.Fatalf("begin the upload: %v", err)
	}
	if upload.ID == "" {
		t.Fatal("the upload has no identifier")
	}
	if upload.Owner != "usr_1" {
		t.Errorf("the upload belongs to %q, want usr_1", upload.Owner)
	}
	if upload.State != artifacts.UploadPending {
		t.Errorf("the upload is in the state %q, want %q", upload.State, artifacts.UploadPending)
	}
	if upload.ExpiresAt == nil {
		t.Error("the upload has no deadline")
	}

	written, err := uploads.Write("usr_1", upload.ID, strings.NewReader(body))
	if err != nil {
		t.Fatalf("write the upload: %v", err)
	}
	if written.Received != int64(len(body)) {
		t.Errorf("the upload received %d bytes, want %d", written.Received, len(body))
	}
	if written.Digest != digest {
		t.Errorf("the upload hashes to %q, want %q", written.Digest, digest)
	}

	completed, info, err := uploads.Complete("usr_1", upload.ID, digest, "art_1")
	if err != nil {
		t.Fatalf("complete the upload: %v", err)
	}
	if completed.State != artifacts.UploadCompleted {
		t.Errorf("the upload is in the state %q, want %q", completed.State, artifacts.UploadCompleted)
	}
	if completed.ArtifactID != "art_1" {
		t.Errorf("the upload produced the artifact %q, want art_1", completed.ArtifactID)
	}
	if info.Digest != digest {
		t.Errorf("the artifact hashes to %q, want %q", info.Digest, digest)
	}
	// The file joined the artifact store of the owner.
	if _, err := store.Get("usr_1", filepath.Base(info.Path)); err != nil {
		t.Errorf("the artifact did not reach the store: %v", err)
	}
}

// TestAnUploadIsPrivateToItsOwner checks that the bytes of an upload belong
// to the account that announced them.
func TestAnUploadIsPrivateToItsOwner(t *testing.T) {
	uploads, _ := newUploads(t, nil)
	upload, err := uploads.Begin("usr_1", "main.go", "text/plain", 4)
	if err != nil {
		t.Fatalf("begin the upload: %v", err)
	}
	if _, err := uploads.Write("usr_2", upload.ID, strings.NewReader("body")); !errors.Is(err, artifacts.ErrUploadForbidden) {
		t.Errorf("the write of a stranger returned %v, want a forbidden upload", err)
	}
	if _, err := uploads.Status("usr_2", upload.ID); !errors.Is(err, artifacts.ErrUploadForbidden) {
		t.Errorf("the status of a stranger returned %v, want a forbidden upload", err)
	}
	if _, _, err := uploads.Complete("usr_2", upload.ID, strings.Repeat("a", 64), "art_1"); !errors.Is(err, artifacts.ErrUploadForbidden) {
		t.Errorf("the completion of a stranger returned %v, want a forbidden upload", err)
	}
	if err := uploads.Abandon("usr_2", upload.ID); !errors.Is(err, artifacts.ErrUploadForbidden) {
		t.Errorf("the abandon of a stranger returned %v, want a forbidden upload", err)
	}
}

// TestAnUploadRefusesAWrongDigest checks that the content of an upload is
// verified against the digest of the client.
func TestAnUploadRefusesAWrongDigest(t *testing.T) {
	uploads, _ := newUploads(t, nil)
	const body = "the source of the workload"
	upload, err := uploads.Begin("usr_1", "main.go", "text/plain", int64(len(body)))
	if err != nil {
		t.Fatalf("begin the upload: %v", err)
	}
	if _, err := uploads.Write("usr_1", upload.ID, strings.NewReader(body)); err != nil {
		t.Fatalf("write the upload: %v", err)
	}
	wrong := sha256.Sum256([]byte("another file"))
	_, _, err = uploads.Complete("usr_1", upload.ID, hex.EncodeToString(wrong[:]), "art_1")
	if !errors.Is(err, artifacts.ErrDigestMismatch) {
		t.Fatalf("the completion returned %v, want a digest mismatch", err)
	}
}

// TestAnUploadRefusesAnInvalidDigest checks the format of the digest.
func TestAnUploadRefusesAnInvalidDigest(t *testing.T) {
	uploads, _ := newUploads(t, nil)
	upload, err := uploads.Begin("usr_1", "main.go", "text/plain", 4)
	if err != nil {
		t.Fatalf("begin the upload: %v", err)
	}
	if _, err := uploads.Write("usr_1", upload.ID, strings.NewReader("body")); err != nil {
		t.Fatalf("write the upload: %v", err)
	}
	for _, digest := range []string{"", "not-a-digest", strings.Repeat("z", 64)} {
		if _, _, err := uploads.Complete("usr_1", upload.ID, digest, "art_1"); !errors.Is(err, artifacts.ErrInvalidDigest) {
			t.Errorf("the digest %q returned %v, want an invalid digest", digest, err)
		}
	}
}

// TestAnUploadRefusesTooManyBytes checks the limits of an upload.
func TestAnUploadRefusesTooManyBytes(t *testing.T) {
	uploads, _ := newUploads(t, nil)
	if _, err := uploads.Begin("usr_1", "huge.bin", "application/octet-stream", 1<<40); !errors.Is(err, artifacts.ErrUploadTooLarge) {
		t.Fatalf("the announcement returned %v, want an upload that is too large", err)
	}
	upload, err := uploads.Begin("usr_1", "main.go", "text/plain", 4)
	if err != nil {
		t.Fatalf("begin the upload: %v", err)
	}
	// The client sends more bytes than it announced.
	if _, err := uploads.Write("usr_1", upload.ID, strings.NewReader("too many bytes")); !errors.Is(err, artifacts.ErrSizeMismatch) {
		t.Fatalf("the write returned %v, want a size mismatch", err)
	}
}

// TestAnExpiredUploadIsRefused checks the deadline of an upload.
func TestAnExpiredUploadIsRefused(t *testing.T) {
	now := time.Now().UTC()
	uploads, _ := newUploads(t, func() time.Time { return now })
	upload, err := uploads.Begin("usr_1", "main.go", "text/plain", 4)
	if err != nil {
		t.Fatalf("begin the upload: %v", err)
	}
	if _, err := uploads.Write("usr_1", upload.ID, strings.NewReader("body")); err != nil {
		t.Fatalf("write the upload: %v", err)
	}
	// The clock moves past the deadline of the upload.
	now = now.Add(2 * time.Hour)
	sum := sha256.Sum256([]byte("body"))
	if _, _, err := uploads.Complete("usr_1", upload.ID, hex.EncodeToString(sum[:]), "art_1"); !errors.Is(err, artifacts.ErrUploadExpired) {
		t.Fatalf("the completion returned %v, want an expired upload", err)
	}
}

// TestAnUnknownUploadIsReported checks the lookup of an upload that does not
// exist.
func TestAnUnknownUploadIsReported(t *testing.T) {
	uploads, _ := newUploads(t, nil)
	if _, err := uploads.Status("usr_1", "upl_missing"); !errors.Is(err, artifacts.ErrUploadNotFound) {
		t.Errorf("the status returned %v, want a missing upload", err)
	}
	if _, err := uploads.Status("usr_1", "../../etc/passwd"); !errors.Is(err, artifacts.ErrUploadNotFound) {
		t.Errorf("the status returned %v, want a missing upload", err)
	}
}

// TestACompletedUploadCannotBeCompletedAgain checks that an upload is only
// completed once.
func TestACompletedUploadCannotBeCompletedAgain(t *testing.T) {
	uploads, _ := newUploads(t, nil)
	const body = "the source of the workload"
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])

	upload, err := uploads.Begin("usr_1", "main.go", "text/plain", int64(len(body)))
	if err != nil {
		t.Fatalf("begin the upload: %v", err)
	}
	if _, err := uploads.Write("usr_1", upload.ID, strings.NewReader(body)); err != nil {
		t.Fatalf("write the upload: %v", err)
	}
	if _, _, err := uploads.Complete("usr_1", upload.ID, digest, "art_1"); err != nil {
		t.Fatalf("complete the upload: %v", err)
	}
	if _, _, err := uploads.Complete("usr_1", upload.ID, digest, "art_2"); !errors.Is(err, artifacts.ErrUploadFinished) {
		t.Fatalf("the second completion returned %v, want a finished upload", err)
	}
}

// TestAnUploadCanBeAbandoned checks that a pending upload can be dropped.
func TestAnUploadCanBeAbandoned(t *testing.T) {
	uploads, _ := newUploads(t, nil)
	upload, err := uploads.Begin("usr_1", "main.go", "text/plain", 4)
	if err != nil {
		t.Fatalf("begin the upload: %v", err)
	}
	if _, err := uploads.Write("usr_1", upload.ID, strings.NewReader("body")); err != nil {
		t.Fatalf("write the upload: %v", err)
	}
	part := uploads.PartPath(upload.ID)
	if err := uploads.Abandon("usr_1", upload.ID); err != nil {
		t.Fatalf("abandon the upload: %v", err)
	}
	if _, err := uploads.Status("usr_1", upload.ID); !errors.Is(err, artifacts.ErrUploadNotFound) {
		t.Errorf("the status returned %v, want a missing upload", err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf("the pending file still exists: %v", err)
	}
}

// newDownloads returns a download manager with a temporary directory.
func newDownloads(t *testing.T, now func() time.Time) *artifacts.Downloads {
	t.Helper()
	downloads, err := artifacts.NewDownloads(artifacts.DownloadOptions{
		Root:  filepath.Join(t.TempDir(), "downloads"),
		TTL:   time.Hour,
		Clock: now,
	})
	if err != nil {
		t.Fatalf("create the download manager: %v", err)
	}
	return downloads
}

// TestATokenIsIssuedAndRedeemed checks the life of a download token.
func TestATokenIsIssuedAndRedeemed(t *testing.T) {
	downloads := newDownloads(t, nil)
	token, err := downloads.Issue("art_1", "usr_1")
	if err != nil {
		t.Fatalf("issue the token: %v", err)
	}
	if token.Token == "" {
		t.Fatal("the token has no value")
	}
	if token.ArtifactID != "art_1" || token.Owner != "usr_1" {
		t.Errorf("the token opens %q for %q", token.ArtifactID, token.Owner)
	}
	if !token.ExpiresAt.After(token.CreatedAt) {
		t.Error("the token expires before it was created")
	}

	grant, err := downloads.Redeem(token.Token, "art_1")
	if err != nil {
		t.Fatalf("redeem the token: %v", err)
	}
	if grant.ArtifactID != "art_1" {
		t.Errorf("the grant opens %q, want art_1", grant.ArtifactID)
	}
	// A token may be redeemed once.
	if _, err := downloads.Redeem(token.Token, "art_1"); !errors.Is(err, artifacts.ErrTokenInvalid) {
		t.Errorf("the second redemption returned %v, want an invalid token", err)
	}
}

// TestATokenIsRefusedForAnotherArtifact checks that a token opens the
// artifact it was issued for.
func TestATokenIsRefusedForAnotherArtifact(t *testing.T) {
	downloads := newDownloads(t, nil)
	token, err := downloads.Issue("art_1", "usr_1")
	if err != nil {
		t.Fatalf("issue the token: %v", err)
	}
	if _, err := downloads.Redeem(token.Token, "art_2"); !errors.Is(err, artifacts.ErrTokenInvalid) {
		t.Errorf("the redemption returned %v, want an invalid token", err)
	}
}

// TestAnExpiredTokenIsRefused checks the deadline of a token.
func TestAnExpiredTokenIsRefused(t *testing.T) {
	now := time.Now().UTC()
	downloads := newDownloads(t, func() time.Time { return now })
	token, err := downloads.Issue("art_1", "usr_1")
	if err != nil {
		t.Fatalf("issue the token: %v", err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := downloads.Redeem(token.Token, "art_1"); !errors.Is(err, artifacts.ErrTokenExpired) {
		t.Errorf("the redemption returned %v, want an expired token", err)
	}
}

// TestAMalformedTokenIsRefused checks the format of a token.
func TestAMalformedTokenIsRefused(t *testing.T) {
	downloads := newDownloads(t, nil)
	for _, token := range []string{"", "not-hexadecimal", "../../etc/passwd", "abc"} {
		if _, err := downloads.Redeem(token, "art_1"); !errors.Is(err, artifacts.ErrTokenInvalid) {
			t.Errorf("the token %q returned %v, want an invalid token", token, err)
		}
	}
}

// TestIssueNeedsAnArtifactAndAnOwner checks the arguments of the manager.
func TestIssueNeedsAnArtifactAndAnOwner(t *testing.T) {
	downloads := newDownloads(t, nil)
	if _, err := downloads.Issue("", "usr_1"); err == nil {
		t.Error("a token without an artifact was issued")
	}
	if _, err := downloads.Issue("art_1", ""); err == nil {
		t.Error("a token without an owner was issued")
	}
}
