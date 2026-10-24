package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/zarigata/double-agent/internal/backup"
	"github.com/zarigata/double-agent/internal/config"
	"github.com/zarigata/double-agent/internal/daemon"
)

// backupCommand creates or restores a backup of the node.
//
// The command is deliberately offline friendly: it works on a stopped node so
// that the database is never copied while it is being written.
func backupCommand(args []string, stdout, stderr *os.File) int {
	fs := newFlagSet("backup", stderr)
	configPath := fs.String("config", "", "path of the configuration file")
	dataDir := fs.String("data-dir", "", "override the data directory")
	restoreFrom := fs.String("restore", "", "restore the node from a backup directory")
	list := fs.Bool("list", false, "list the available backups")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "double-agentd: %v\n", err)
		return exitError
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}

	if *list {
		return listBackups(cfg.DataDir, stdout, stderr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if *restoreFrom != "" {
		d, err := daemon.New(daemon.Options{Config: cfg})
		if err != nil {
			fmt.Fprintf(stderr, "double-agentd: %v\n", err)
			return exitError
		}
		// The daemon holds the database open; the restore replaces the file,
		// so the handle is released first.
		d.Close()
		if err := backup.Restore(ctx, *restoreFrom, backup.Options{
			DataDir:      cfg.DataDir,
			DatabasePath: cfg.DatabasePath(),
			ArtifactRoot: cfg.Artifacts.Root,
		}); err != nil {
			fmt.Fprintf(stderr, "double-agentd: %v\n", err)
			return exitError
		}
		fmt.Fprintf(stdout, "restored the node from %s\n", *restoreFrom)
		return exitOK
	}

	result, err := backup.Create(ctx, backup.Options{
		DataDir:      cfg.DataDir,
		DatabasePath: cfg.DatabasePath(),
		ArtifactRoot: cfg.Artifacts.Root,
	})
	if err != nil {
		fmt.Fprintf(stderr, "double-agentd: %v\n", err)
		return exitError
	}
	fmt.Fprintf(stdout, "backup created at %s\n", result.Dir)
	fmt.Fprintf(stdout, "  schema version: %d\n", result.Manifest.SchemaVersion)
	fmt.Fprintf(stdout, "  files:           %d\n", len(result.Manifest.Files))
	return exitOK
}

// listBackups prints the backups of a node.
func listBackups(dataDir string, stdout, stderr *os.File) int {
	dirs, err := backup.List(dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "double-agentd: %v\n", err)
		return exitError
	}
	if len(dirs) == 0 {
		fmt.Fprintln(stdout, "no backup was found")
		return exitOK
	}
	for _, dir := range dirs {
		manifest, err := backup.ReadManifest(dir)
		if err != nil {
			fmt.Fprintf(stdout, "%s\t(unreadable manifest: %v)\n", dir, err)
			continue
		}
		fmt.Fprintf(stdout, "%s\tcreated %s\tschema %d\t%d files\n",
			dir, manifest.CreatedAt.Format(time.RFC3339), manifest.SchemaVersion, len(manifest.Files))
	}
	return exitOK
}
