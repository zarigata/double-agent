package backup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zarigata/double-agent/internal/artifacts"
	"github.com/zarigata/double-agent/internal/backup"
	"github.com/zarigata/double-agent/internal/ids"
	"github.com/zarigata/double-agent/internal/store"
)

// TestABackupIsRestoredIntoANewNode walks through the whole flow: a node
// with data, a backup of it and the restore of that backup into a new
// directory.
func TestABackupIsRestoredIntoANewNode(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database := filepath.Join(dir, "state", "double-agent.db")
	root := filepath.Join(dir, "artifacts")

	st, err := store.Open(database)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	defer st.Close()

	user, err := st.CreateUser(ctx, store.User{
		ID:       ids.New(ids.PrefixUser),
		Username: "operator",
		Role:     store.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("create the account: %v", err)
	}
	artifactStore, err := artifacts.New(artifacts.Options{Root: root, MaxSize: 1 << 20})
	if err != nil {
		t.Fatalf("create the artifact store: %v", err)
	}
	info, err := artifactStore.Put(user.ID, "report.txt", bytesReader("the report of the workload"))
	if err != nil {
		t.Fatalf("store the artifact: %v", err)
	}
	record, err := st.CreateArtifact(ctx, store.Artifact{
		ID:     ids.New(ids.PrefixArtifact),
		UserID: user.ID,
		Name:   info.Name,
		Path:   info.Path,
		MIME:   info.MIME,
		Size:   info.Size,
		Digest: info.Digest,
		State:  store.ArtifactAvailable,
	})
	if err != nil {
		t.Fatalf("record the artifact: %v", err)
	}

	// The backup holds the database, the artifact store and the manifest.
	result, err := backup.Create(ctx, backup.Options{
		DataDir:      dir,
		DatabasePath: database,
		ArtifactRoot: root,
		DB:           st.DB(),
		Clock:        func() time.Time { return time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("create the backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.Dir, "double-agent.db")); err != nil {
		t.Fatalf("the backup holds no database: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.Dir, "manifest.json")); err != nil {
		t.Fatalf("the backup holds no manifest: %v", err)
	}
	if result.Manifest.SchemaVersion < 1 {
		t.Errorf("the manifest names the schema version %d", result.Manifest.SchemaVersion)
	}
	if len(result.Manifest.Files) < 2 {
		t.Fatalf("the manifest names %d files, want the database and the artifact", len(result.Manifest.Files))
	}

	// The backup restores into a new directory.
	target := t.TempDir()
	if err := backup.Restore(ctx, result.Dir, backup.Options{
		DataDir:      target,
		DatabasePath: filepath.Join(target, "state", "double-agent.db"),
		ArtifactRoot: filepath.Join(target, "artifacts"),
	}); err != nil {
		t.Fatalf("restore the backup: %v", err)
	}

	restored, err := store.Open(filepath.Join(target, "state", "double-agent.db"))
	if err != nil {
		t.Fatalf("open the restored store: %v", err)
	}
	defer restored.Close()

	restoredUser, err := restored.UserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("load the restored account: %v", err)
	}
	if restoredUser.Username != user.Username {
		t.Errorf("the restored account is named %q, want %q", restoredUser.Username, user.Username)
	}
	if restoredUser.Role != user.Role {
		t.Errorf("the restored account has the role %q, want %q", restoredUser.Role, user.Role)
	}
	restoredArtifact, err := restored.ArtifactByID(ctx, record.ID)
	if err != nil {
		t.Fatalf("load the restored artifact: %v", err)
	}
	if restoredArtifact.Digest != record.Digest {
		t.Errorf("the restored artifact carries the digest %q, want %q", restoredArtifact.Digest, record.Digest)
	}
	if restoredArtifact.Size != record.Size {
		t.Errorf("the restored artifact holds %d bytes, want %d", restoredArtifact.Size, record.Size)
	}
	if restoredArtifact.Name != record.Name {
		t.Errorf("the restored artifact is named %q, want %q", restoredArtifact.Name, record.Name)
	}

	// The file of the artifact is part of the backup as well.
	found := false
	for _, file := range result.Manifest.Files {
		if file.Path == "artifacts/"+restoredArtifact.UserID+"/"+
			restoredArtifact.Digest[:12]+"-"+restoredArtifact.Name {
			found = true
		}
	}
	if !found {
		t.Errorf("the manifest does not name the file of the artifact: %+v", result.Manifest.Files)
	}
}

// TestARestoreRefusesADamagedBackup checks that the manifest is verified
// before anything is written.
func TestARestoreRefusesADamagedBackup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database := filepath.Join(dir, "state", "double-agent.db")
	root := filepath.Join(dir, "artifacts")

	st, err := store.Open(database)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	defer st.Close()

	result, err := backup.Create(ctx, backup.Options{
		DataDir:      dir,
		DatabasePath: database,
		ArtifactRoot: root,
		DB:           st.DB(),
	})
	if err != nil {
		t.Fatalf("create the backup: %v", err)
	}

	// The database of the backup is damaged.
	path := filepath.Join(result.Dir, "double-agent.db")
	if err := os.WriteFile(path, []byte("not a database"), 0o640); err != nil {
		t.Fatalf("damage the backup: %v", err)
	}
	target := t.TempDir()
	err = backup.Restore(ctx, result.Dir, backup.Options{
		DataDir:      target,
		DatabasePath: filepath.Join(target, "state", "double-agent.db"),
		ArtifactRoot: filepath.Join(target, "artifacts"),
	})
	if err == nil {
		t.Fatal("the restore of a damaged backup succeeded")
	}
	if !errors.Is(err, backup.ErrInvalidBackup) {
		t.Errorf("the failure is %v, want %v", err, backup.ErrInvalidBackup)
	}
}

// bytesReader returns a reader over a string.
func bytesReader(content string) *strings.Reader { return strings.NewReader(content) }
