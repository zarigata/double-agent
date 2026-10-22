package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zarigata/double-agent/internal/ids"
)

// Errors returned by the upload manager.
var (
	// ErrUploadNotFound is returned when an upload does not exist.
	ErrUploadNotFound = errors.New("artifacts: upload not found")
	// ErrUploadForbidden is returned when an upload belongs to another
	// owner. The bytes of an upload are private to the account that
	// announced them.
	ErrUploadForbidden = errors.New("artifacts: upload belongs to another owner")
	// ErrUploadExpired is returned when an upload is past its deadline.
	ErrUploadExpired = errors.New("artifacts: upload expired")
	// ErrUploadFinished is returned when an upload was already completed.
	ErrUploadFinished = errors.New("artifacts: upload already finished")
	// ErrUploadTooLarge is returned when an upload exceeds the accepted size.
	ErrUploadTooLarge = errors.New("artifacts: upload is too large")
	// ErrDigestMismatch is returned when the content does not match the
	// digest the client announced.
	ErrDigestMismatch = errors.New("artifacts: digest mismatch")
	// ErrInvalidDigest is returned when a digest is not a sha256 value.
	ErrInvalidDigest = errors.New("artifacts: invalid digest")
	// ErrSizeMismatch is returned when the content does not match the size
	// the client announced.
	ErrSizeMismatch = errors.New("artifacts: size mismatch")
)

// uploadID matches the identifiers of the uploads. The identifier names a
// file on the disk, so it accepts nothing but letters, digits, dashes and
// underscores.
var uploadID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// UploadState is the lifecycle state of an upload.
type UploadState string

const (
	// UploadPending means the bytes of the upload may still arrive.
	UploadPending UploadState = "pending"
	// UploadCompleted means the file joined the artifact store.
	UploadCompleted UploadState = "completed"
)

// Upload is a resumable upload of the node.
//
// The owner of the upload is the account that announced it: no other account
// may read the bytes, complete the upload or abandon it.
type Upload struct {
	// ID identifies the upload.
	ID string `json:"id"`
	// Owner is the account that announced the upload.
	Owner string `json:"owner"`
	// Name is the display name of the file.
	Name string `json:"name"`
	// ContentType is the content type the client announced.
	ContentType string `json:"content_type,omitempty"`
	// Size is the number of bytes the client announced.
	Size int64 `json:"size"`
	// Received is the number of bytes that arrived so far.
	Received int64 `json:"received"`
	// Digest is the sha256 of the content, hex encoded. It is filled once
	// the bytes arrived or once the upload was completed.
	Digest string `json:"digest,omitempty"`
	// State is the lifecycle state of the upload.
	State UploadState `json:"state"`
	// CreatedAt is the moment the upload was announced.
	CreatedAt time.Time `json:"created_at"`
	// ExpiresAt is the deadline of the upload.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// ArtifactID is the artifact the upload produced.
	ArtifactID string `json:"artifact_id,omitempty"`
}

// UploadOptions configures an upload manager.
type UploadOptions struct {
	// Root is the directory that holds the pending uploads.
	Root string
	// Store is the artifact store the completed files join.
	Store *Store
	// MaxBytes is the largest upload accepted, in bytes.
	MaxBytes int64
	// TTL is the lifetime of a pending upload.
	TTL time.Duration
	// Clock returns the current time. Tests replace it.
	Clock func() time.Time
}

// Uploads manages the resumable uploads of the node.
//
// The state of an upload lives on the disk next to the artifact store, so
// the MCP surface and the REST surface of one node agree on an upload even
// though they never talk to each other. The bytes of a pending upload are
// never readable: only the owner may complete the upload, and the content
// is verified against the digest the client announced.
type Uploads struct {
	dir   string
	store *Store
	// maxBytes is the largest upload accepted.
	maxBytes int64
	// ttl is the lifetime of a pending upload.
	ttl time.Duration
	// now returns the current time.
	now func() time.Time
}

// NewUploads creates an upload manager.
func NewUploads(opts UploadOptions) (*Uploads, error) {
	if opts.Root == "" {
		return nil, errors.New("artifacts: the upload root is required")
	}
	if opts.Store == nil {
		return nil, errors.New("artifacts: the artifact store is required")
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 1 << 30
	}
	if opts.TTL <= 0 {
		opts.TTL = 24 * time.Hour
	}
	if opts.Clock == nil {
		opts.Clock = func() time.Time { return time.Now().UTC() }
	}
	if err := os.MkdirAll(opts.Root, 0o750); err != nil {
		return nil, fmt.Errorf("artifacts: create the upload directory: %w", err)
	}
	return &Uploads{
		dir:      opts.Root,
		store:    opts.Store,
		maxBytes: opts.MaxBytes,
		ttl:      opts.TTL,
		now:      opts.Clock,
	}, nil
}

// MaxBytes returns the largest upload the manager accepts.
func (u *Uploads) MaxBytes() int64 { return u.maxBytes }

// Begin announces an upload and reserves a place for its bytes.
//
// The announcement is checked against the limits of the node before anything
// is written, so a client that asks for a file that is too large learns it
// immediately instead of after the transfer.
func (u *Uploads) Begin(owner, name, contentType string, size int64) (Upload, error) {
	if strings.TrimSpace(owner) == "" {
		return Upload{}, fmt.Errorf("%w: the owner is empty", ErrUploadForbidden)
	}
	if err := ValidateName(name); err != nil {
		return Upload{}, err
	}
	if size < 0 {
		return Upload{}, fmt.Errorf("%w: the size is negative", ErrSizeMismatch)
	}
	if size > u.maxBytes {
		return Upload{}, fmt.Errorf("%w: the limit is %d bytes", ErrUploadTooLarge, u.maxBytes)
	}
	now := u.now()
	deadline := now.Add(u.ttl)
	record := Upload{
		ID:          ids.New(ids.PrefixUpload),
		Owner:       owner,
		Name:        name,
		ContentType: strings.TrimSpace(contentType),
		Size:        size,
		State:       UploadPending,
		CreatedAt:   now,
		ExpiresAt:   &deadline,
	}
	if err := u.save(record); err != nil {
		return Upload{}, err
	}
	// The pending file is created empty, so the transfer never has to
	// create a file inside a directory the client may not write to.
	file, err := os.OpenFile(u.partPath(record.ID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return Upload{}, fmt.Errorf("artifacts: create the pending file: %w", err)
	}
	if err := file.Close(); err != nil {
		return Upload{}, fmt.Errorf("artifacts: create the pending file: %w", err)
	}
	return record, nil
}

// Write streams the bytes of an upload into the pending file.
//
// The limit of the node is enforced while the bytes arrive, so an upload
// that sends more than it announced never fills the disk. The digest of the
// content is computed on the way, so the completion never reads the file
// twice.
func (u *Uploads) Write(owner, id string, r io.Reader) (Upload, error) {
	record, err := u.load(owner, id)
	if err != nil {
		return Upload{}, err
	}
	if record.State != UploadPending {
		return Upload{}, fmt.Errorf("%w: the upload is %s", ErrUploadFinished, record.State)
	}
	if record.Size > u.maxBytes {
		// The announcement was made on another surface of the node, so
		// the limit of this one is checked again before the bytes are
		// accepted.
		return Upload{}, fmt.Errorf("%w: the limit is %d bytes", ErrUploadTooLarge, u.maxBytes)
	}
	file, err := os.OpenFile(u.partPath(record.ID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return Upload{}, fmt.Errorf("artifacts: open the pending file: %w", err)
	}
	defer file.Close()

	hasher := sha256.New()
	// The reader stops one byte after the announced size, so a body that is
	// too long is detected instead of silently truncated.
	limited := io.LimitReader(r, record.Size+1)
	written, err := io.Copy(io.MultiWriter(file, hasher), limited)
	if err != nil {
		return Upload{}, fmt.Errorf("artifacts: write the pending file: %w", err)
	}
	if written > record.Size {
		return Upload{}, fmt.Errorf("%w: the upload holds more than the %d announced bytes",
			ErrSizeMismatch, record.Size)
	}
	record.Received = written
	record.Digest = hex.EncodeToString(hasher.Sum(nil))
	if err := u.save(record); err != nil {
		return Upload{}, err
	}
	return record, nil
}

// Status reads the state of an upload.
func (u *Uploads) Status(owner, id string) (Upload, error) {
	return u.load(owner, id)
}

// Complete verifies the bytes of an upload and promotes the file into the
// artifact store.
//
// The digest the client announces is compared with the digest of the bytes
// that arrived: a mismatch is refused and the file never reaches the store.
// The artifact identifier is supplied by the caller, because the metadata
// database belongs to the control plane and not to this package.
func (u *Uploads) Complete(owner, id, digest, artifactID string) (Upload, Info, error) {
	record, err := u.load(owner, id)
	if err != nil {
		return Upload{}, Info{}, err
	}
	if record.State == UploadCompleted {
		return Upload{}, Info{}, fmt.Errorf("%w: the upload is already complete", ErrUploadFinished)
	}
	if err := u.expired(record); err != nil {
		return Upload{}, Info{}, err
	}
	expected := strings.ToLower(strings.TrimSpace(digest))
	if !validDigest(expected) {
		return Upload{}, Info{}, fmt.Errorf("%w: the digest is not a sha256 value", ErrInvalidDigest)
	}
	part := u.partPath(record.ID)
	info, err := os.Stat(part)
	if errors.Is(err, os.ErrNotExist) {
		return Upload{}, Info{}, fmt.Errorf("%w: the bytes of the upload are missing", ErrUploadNotFound)
	}
	if err != nil {
		return Upload{}, Info{}, fmt.Errorf("artifacts: read the pending file: %w", err)
	}
	if info.Size() != record.Size {
		return Upload{}, Info{}, fmt.Errorf("%w: the upload holds %d bytes, %d were announced",
			ErrSizeMismatch, info.Size(), record.Size)
	}
	actual, err := fileDigest(part)
	if err != nil {
		return Upload{}, Info{}, err
	}
	if actual != expected {
		return Upload{}, Info{}, fmt.Errorf("%w: the content hashes to %s", ErrDigestMismatch, actual)
	}
	stored, err := u.store.Import(owner, record.Name, part)
	if err != nil {
		return Upload{}, Info{}, err
	}
	record.State = UploadCompleted
	record.Digest = actual
	record.Received = stored.Size
	record.ArtifactID = artifactID
	if err := u.save(record); err != nil {
		return Upload{}, Info{}, err
	}
	return record, stored, nil
}

// Abandon drops a pending upload and the bytes that arrived.
func (u *Uploads) Abandon(owner, id string) error {
	record, err := u.load(owner, id)
	if err != nil {
		return err
	}
	if err := os.Remove(u.partPath(record.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("artifacts: remove the pending file: %w", err)
	}
	if err := os.Remove(u.recordPath(record.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("artifacts: remove the upload: %w", err)
	}
	return nil
}

// PartPath returns the path of the pending file of an upload. The path stays
// inside the control plane: it is never sent to a client.
func (u *Uploads) PartPath(id string) string { return u.partPath(id) }

// load reads the record of an upload and checks its owner.
func (u *Uploads) load(owner, id string) (Upload, error) {
	if !uploadID.MatchString(strings.TrimSpace(id)) {
		return Upload{}, fmt.Errorf("%w: %q", ErrUploadNotFound, id)
	}
	data, err := os.ReadFile(u.recordPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return Upload{}, fmt.Errorf("%w: %s", ErrUploadNotFound, id)
	}
	if err != nil {
		return Upload{}, fmt.Errorf("artifacts: read the upload %s: %w", id, err)
	}
	var record Upload
	if err := json.Unmarshal(data, &record); err != nil {
		return Upload{}, fmt.Errorf("artifacts: decode the upload %s: %w", id, err)
	}
	if owner != "" && record.Owner != owner {
		return Upload{}, fmt.Errorf("%w: %s", ErrUploadForbidden, id)
	}
	return record, nil
}

// expired reports whether an upload is past its deadline.
func (u *Uploads) expired(record Upload) error {
	if record.ExpiresAt == nil {
		return nil
	}
	if u.now().Before(*record.ExpiresAt) {
		return nil
	}
	return fmt.Errorf("%w: the upload expired at %s", ErrUploadExpired,
		record.ExpiresAt.UTC().Format(time.RFC3339))
}

// save writes the record of an upload.
func (u *Uploads) save(record Upload) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("artifacts: encode the upload %s: %w", record.ID, err)
	}
	path := u.recordPath(record.ID)
	if err := os.WriteFile(path, data, 0o640); err != nil {
		return fmt.Errorf("artifacts: write the upload %s: %w", record.ID, err)
	}
	return nil
}

// recordPath returns the path of the record of an upload.
func (u *Uploads) recordPath(id string) string {
	return filepath.Join(u.dir, id+".json")
}

// partPath returns the path of the pending file of an upload.
func (u *Uploads) partPath(id string) string {
	return filepath.Join(u.dir, id+".part")
}

// validDigest reports whether a value is a sha256 digest.
func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
