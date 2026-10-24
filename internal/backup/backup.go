// Package backup produces and restores the backups of a Double Agent node.
//
// A backup is a directory that holds a consistent copy of the metadata
// database, a copy of the artifact store and a manifest that names every
// file with its digest. The database is copied with the SQLite backup API
// (VACUUM INTO), which is safe on a live database: the copy is a snapshot
// that never observes a half written transaction.
//
// The restore verifies the manifest before it writes anything, so a damaged
// backup never replaces a healthy node.
package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go SQLite driver, no cgo required
)

// Errors returned by the package.
var (
	// ErrInvalidBackup is returned when a backup is not complete or does
	// not match its manifest.
	ErrInvalidBackup = errors.New("backup: the backup is not valid")
	// ErrNotFound is returned when a backup does not exist.
	ErrNotFound = errors.New("backup: the backup does not exist")
)

// databaseName is the name of the database inside a backup.
const databaseName = "double-agent.db"

// manifestName is the name of the manifest inside a backup.
const manifestName = "manifest.json"

// artifactDir is the name of the artifact store inside a backup.
const artifactDir = "artifacts"

// File is one file of a backup.
type File struct {
	// Path is the path of the file, relative to the root of the backup.
	Path string `json:"path"`
	// Size is the length of the file, in bytes.
	Size int64 `json:"size"`
	// Digest is the sha256 of the content, hex encoded.
	Digest string `json:"digest"`
}

// Manifest describes the content of a backup.
//
// The manifest is the contract of a backup: the restore verifies every file
// against it before it touches the node.
type Manifest struct {
	// CreatedAt is the moment the backup was made.
	CreatedAt time.Time `json:"created_at"`
	// SchemaVersion is the version of the schema of the database.
	SchemaVersion int `json:"schema_version"`
	// Database is the name of the database file inside the backup.
	Database string `json:"database"`
	// Files lists every file of the backup.
	Files []File `json:"files"`
}

// Options configures a backup.
type Options struct {
	// DataDir is the node data directory. The backup is written to
	// <DataDir>/backups/<timestamp>.
	DataDir string
	// DatabasePath is the path of the SQLite database of the node.
	DatabasePath string
	// ArtifactRoot is the directory of the artifact store of the node.
	ArtifactRoot string
	// DB is the live handle of the database. When it is empty the
	// database is opened for the copy, which is safe as well.
	DB *sql.DB
	// Clock returns the current time. Tests replace it.
	Clock func() time.Time
}

// Result describes a backup that was made.
type Result struct {
	// Dir is the directory of the backup.
	Dir string `json:"dir"`
	// Manifest is the manifest of the backup.
	Manifest Manifest `json:"manifest"`
}

// Create produces a consistent backup of the node.
//
// The database is copied first, so the copy and the manifest describe the
// same moment of the life of the node.
func Create(ctx context.Context, opts Options) (Result, error) {
	if strings.TrimSpace(opts.DataDir) == "" {
		return Result{}, errors.New("backup: the data directory is required")
	}
	if strings.TrimSpace(opts.DatabasePath) == "" {
		return Result{}, errors.New("backup: the path of the database is required")
	}
	now := opts.now()
	dir := filepath.Join(opts.DataDir, "backups", now.UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Result{}, fmt.Errorf("backup: create the backup directory: %w", err)
	}

	database := filepath.Join(dir, databaseName)
	if err := copyDatabase(ctx, opts, database); err != nil {
		return Result{}, err
	}
	version, err := schemaVersion(ctx, opts, database)
	if err != nil {
		return Result{}, err
	}

	manifest := Manifest{
		CreatedAt:     now.UTC(),
		SchemaVersion: version,
		Database:      databaseName,
		Files:         []File{},
	}
	if err := collect(ctx, dir, database, &manifest); err != nil {
		return Result{}, err
	}
	if err := copyTree(opts.ArtifactRoot, filepath.Join(dir, artifactDir), &manifest); err != nil {
		return Result{}, err
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("backup: encode the manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), encoded, 0o640); err != nil {
		return Result{}, fmt.Errorf("backup: write the manifest: %w", err)
	}
	return Result{Dir: dir, Manifest: manifest}, nil
}

// Restore restores a backup into a node.
//
// Every file of the backup is verified against the manifest before the
// first byte is written, so a damaged backup never replaces a node.
func Restore(ctx context.Context, dir string, opts Options) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("backup: the directory of the backup is required")
	}
	manifest, err := ReadManifest(dir)
	if err != nil {
		return err
	}
	if err := verify(dir, manifest); err != nil {
		return err
	}
	target := opts.DatabasePath
	if strings.TrimSpace(target) == "" {
		if strings.TrimSpace(opts.DataDir) == "" {
			return errors.New("backup: the path of the database is required")
		}
		target = filepath.Join(opts.DataDir, "state", databaseName)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("backup: create the state directory: %w", err)
	}
	source := filepath.Join(dir, manifest.Database)
	if err := copyFile(source, target); err != nil {
		return fmt.Errorf("backup: restore the database: %w", err)
	}
	root := opts.ArtifactRoot
	if strings.TrimSpace(root) == "" {
		if strings.TrimSpace(opts.DataDir) == "" {
			return errors.New("backup: the artifact store is required")
		}
		root = filepath.Join(opts.DataDir, artifactDir)
	}
	if err := restoreTree(filepath.Join(dir, artifactDir), root); err != nil {
		return err
	}
	return nil
}

// ReadManifest reads the manifest of a backup.
func ReadManifest(dir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("%w: %s", ErrNotFound, dir)
	}
	if err != nil {
		return Manifest{}, fmt.Errorf("backup: read the manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: the manifest is not valid: %s", ErrInvalidBackup, err)
	}
	if manifest.Database == "" || len(manifest.Files) == 0 {
		return Manifest{}, fmt.Errorf("%w: the manifest is empty", ErrInvalidBackup)
	}
	return manifest, nil
}

// verify checks every file of a backup against the manifest.
func verify(dir string, manifest Manifest) error {
	for _, file := range manifest.Files {
		path := filepath.Join(dir, filepath.FromSlash(file.Path))
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("%w: the file %s is missing", ErrInvalidBackup, file.Path)
		}
		if info.Size() != file.Size {
			return fmt.Errorf("%w: the file %s holds %d bytes, the manifest names %d",
				ErrInvalidBackup, file.Path, info.Size(), file.Size)
		}
		digest, err := fileDigest(path)
		if err != nil {
			return err
		}
		if digest != file.Digest {
			return fmt.Errorf("%w: the file %s does not match its digest", ErrInvalidBackup, file.Path)
		}
	}
	return nil
}

// collect adds a file to the manifest.
func collect(ctx context.Context, root, path string, manifest *Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("backup: read %s: %w", path, err)
	}
	digest, err := fileDigest(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("backup: resolve %s: %w", path, err)
	}
	manifest.Files = append(manifest.Files, File{
		Path:   filepath.ToSlash(relative),
		Size:   info.Size(),
		Digest: digest,
	})
	return nil
}

// copyTree copies a directory into a backup and adds every file to the
// manifest.
func copyTree(source, destination string, manifest *Manifest) error {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("backup: read the artifact store: %w", err)
	}
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return fmt.Errorf("backup: create the artifact directory: %w", err)
		}
		if err := copyFile(path, target); err != nil {
			return err
		}
		digest, err := fileDigest(target)
		if err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, File{
			Path:   filepath.ToSlash(filepath.Join(artifactDir, relative)),
			Size:   info.Size(),
			Digest: digest,
		})
		return nil
	})
}

// restoreTree copies the artifact store of a backup back into a node.
func restoreTree(source, destination string) error {
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("backup: read the artifact store of the backup: %w", err)
	}
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return fmt.Errorf("backup: create the artifact directory: %w", err)
		}
		return copyFile(path, target)
	})
}

// copyDatabase writes a consistent copy of the database.
//
// The SQLite backup API is used: VACUUM INTO writes a fresh database that
// holds the committed content of the source, even while the node is
// running.
func copyDatabase(ctx context.Context, opts Options, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return fmt.Errorf("backup: create the backup directory: %w", err)
	}
	// The destination of VACUUM INTO must not exist.
	_ = os.Remove(destination)
	db := opts.DB
	if db == nil {
		opened, err := sql.Open("sqlite", "file:"+opts.DatabasePath+"?_pragma=busy_timeout(5000)")
		if err != nil {
			return fmt.Errorf("backup: open the database: %w", err)
		}
		defer opened.Close()
		db = opened
	}
	// The name of the file is a literal of the statement, so the quotes it
	// may hold are escaped.
	statement := "VACUUM INTO '" + strings.ReplaceAll(destination, "'", "''") + "'"
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("backup: copy the database: %w", err)
	}
	return nil
}

// schemaVersion reads the version of the schema of the database.
func schemaVersion(ctx context.Context, opts Options, database string) (int, error) {
	db := opts.DB
	if db == nil {
		opened, err := sql.Open("sqlite", "file:"+database+"?_pragma=busy_timeout(5000)")
		if err != nil {
			return 0, fmt.Errorf("backup: open the copy of the database: %w", err)
		}
		defer opened.Close()
		db = opened
	}
	var version sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		// A database without the table of the migrations has no version
		// yet: the backup is still valid.
		return 0, nil
	}
	return int(version.Int64), nil
}

// copyFile copies a file and creates the destination when it is missing.
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

// fileDigest computes the sha256 of a file.
func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("backup: open %s: %w", path, err)
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("backup: hash %s: %w", path, err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// now returns the current time of the options.
func (o Options) now() time.Time {
	if o.Clock == nil {
		return time.Now().UTC()
	}
	return o.Clock().UTC()
}

// List returns the backup directories of a node, newest first.
//
// A directory that does not hold a readable manifest is still reported, so
// that an operator can see that a backup is damaged instead of silently
// losing it.
func List(dataDir string) ([]string, error) {
	root := filepath.Join(dataDir, "backups")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("backup: read the backup directory: %w", err)
	}
	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirs = append(dirs, filepath.Join(root, entry.Name()))
	}
	// The timestamped directory names sort chronologically.
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	return dirs, nil
}
