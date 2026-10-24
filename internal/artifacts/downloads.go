package artifacts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zarigata/double-agent/internal/ids"
)

// Errors returned by the download manager.
var (
	// ErrTokenInvalid is returned when a download token is unknown, was
	// already redeemed or belongs to another artifact.
	ErrTokenInvalid = errors.New("artifacts: download token is not valid")
	// ErrTokenExpired is returned when a download token is past its
	// deadline.
	ErrTokenExpired = errors.New("artifacts: download token expired")
)

// tokenValue matches the value of a download token. The value names a file
// on the disk, so it accepts nothing but hexadecimal characters.
var tokenValue = regexp.MustCompile(`^[a-f0-9]{16,128}$`)

// DownloadToken is a short-lived grant to read one artifact.
//
// The token is the capability: it carries the right to read the content of
// one artifact, it expires and it may be redeemed once. A client that
// receives it never learns where the file lives on the host.
type DownloadToken struct {
	// Token is the opaque value the client presents.
	Token string `json:"token"`
	// ArtifactID is the artifact the token opens.
	ArtifactID string `json:"artifact_id"`
	// Owner is the account the token was issued to.
	Owner string `json:"owner"`
	// CreatedAt is the moment the token was issued.
	CreatedAt time.Time `json:"created_at"`
	// ExpiresAt is the deadline of the token.
	ExpiresAt time.Time `json:"expires_at"`
}

// DownloadOptions configures a download manager.
type DownloadOptions struct {
	// Root is the directory that holds the issued tokens.
	Root string
	// TTL is the lifetime of a token.
	TTL time.Duration
	// Clock returns the current time. Tests replace it.
	Clock func() time.Time
}

// Downloads issues and redeems the download tokens of the node.
//
// The tokens live on the disk beside the artifact store, so the MCP surface
// and the REST surface of one node agree on a token even though they never
// talk to each other.
type Downloads struct {
	dir string
	// ttl is the lifetime of a token.
	ttl time.Duration
	// now returns the current time.
	now func() time.Time
}

// NewDownloads creates a download manager.
func NewDownloads(opts DownloadOptions) (*Downloads, error) {
	if opts.Root == "" {
		return nil, errors.New("artifacts: the download root is required")
	}
	if opts.TTL <= 0 {
		opts.TTL = 5 * time.Minute
	}
	if opts.Clock == nil {
		opts.Clock = func() time.Time { return time.Now().UTC() }
	}
	if err := os.MkdirAll(opts.Root, 0o750); err != nil {
		return nil, fmt.Errorf("artifacts: create the download directory: %w", err)
	}
	return &Downloads{dir: opts.Root, ttl: opts.TTL, now: opts.Clock}, nil
}

// TTL returns the lifetime of the tokens of the manager.
func (d *Downloads) TTL() time.Duration { return d.ttl }

// Issue mints a token that opens one artifact.
//
// The token is random: it never carries the identifier of the artifact, the
// identifier of the owner or anything else a client could guess.
func (d *Downloads) Issue(artifactID, owner string) (DownloadToken, error) {
	if strings.TrimSpace(artifactID) == "" {
		return DownloadToken{}, errors.New("artifacts: the artifact identifier is required")
	}
	if strings.TrimSpace(owner) == "" {
		return DownloadToken{}, errors.New("artifacts: the owner is required")
	}
	secret, err := ids.RandomHex(32)
	if err != nil {
		return DownloadToken{}, fmt.Errorf("artifacts: mint the download token: %w", err)
	}
	now := d.now()
	token := DownloadToken{
		Token:      secret,
		ArtifactID: artifactID,
		Owner:      owner,
		CreatedAt:  now,
		ExpiresAt:  now.Add(d.ttl),
	}
	data, err := json.Marshal(token)
	if err != nil {
		return DownloadToken{}, fmt.Errorf("artifacts: encode the download token: %w", err)
	}
	path := d.path(secret)
	if err := os.WriteFile(path, data, 0o640); err != nil {
		return DownloadToken{}, fmt.Errorf("artifacts: write the download token: %w", err)
	}
	return token, nil
}

// Redeem consumes a token and returns the grant it carried.
//
// A token may be redeemed once: the record is renamed before it is read, so
// a second redemption of the same token finds nothing and is refused. An
// expired token is refused as well.
func (d *Downloads) Redeem(token, artifactID string) (DownloadToken, error) {
	value := strings.ToLower(strings.TrimSpace(token))
	if !tokenValue.MatchString(value) {
		return DownloadToken{}, fmt.Errorf("%w: the token is not well formed", ErrTokenInvalid)
	}
	path := d.path(value)
	// The rename is the lock: only the caller that renames the record
	// redeems the token, and the others find nothing.
	if err := os.Rename(path, path+".used"); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DownloadToken{}, fmt.Errorf("%w: the token is unknown or already used", ErrTokenInvalid)
		}
		return DownloadToken{}, fmt.Errorf("artifacts: redeem the download token: %w", err)
	}
	defer os.Remove(path + ".used")
	data, err := os.ReadFile(path + ".used")
	if err != nil {
		return DownloadToken{}, fmt.Errorf("artifacts: read the download token: %w", err)
	}
	var grant DownloadToken
	if err := json.Unmarshal(data, &grant); err != nil {
		return DownloadToken{}, fmt.Errorf("artifacts: decode the download token: %w", err)
	}
	if artifactID != "" && grant.ArtifactID != artifactID {
		return DownloadToken{}, fmt.Errorf("%w: the token opens another artifact", ErrTokenInvalid)
	}
	if !d.now().Before(grant.ExpiresAt) {
		return DownloadToken{}, fmt.Errorf("%w: the token expired at %s", ErrTokenExpired,
			grant.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return grant, nil
}

// path returns the path of the record of a token.
func (d *Downloads) path(token string) string {
	return filepath.Join(d.dir, token+".json")
}
