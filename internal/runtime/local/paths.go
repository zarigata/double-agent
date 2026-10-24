package local

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// workspaceRelative maps a guest path onto a path relative to the instance
// workspace.
//
// A guest path is either relative to the workspace or absolute inside it.
// Anything that would leave the workspace is rejected, which keeps a
// workload from reading or writing the rest of the host.
func workspaceRelative(guest string) (string, error) {
	value := strings.TrimSpace(guest)
	if value == "" {
		return "", errors.New("local: empty path")
	}
	value = filepath.ToSlash(value)
	if strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("local: path %q is outside the instance workspace", guest)
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", fmt.Errorf("local: path %q escapes the instance workspace", guest)
		}
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+value), "/")
	if cleaned == "" {
		return "", fmt.Errorf("local: path %q does not name a file", guest)
	}
	return cleaned, nil
}

// ensureWithin reports whether path stays inside root.
func ensureWithin(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("%s is outside %s", path, root)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside %s", path, root)
	}
	return nil
}

// sanitizeName maps an identifier onto a name that is safe to use as a
// directory name.
func sanitizeName(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	name := builder.String()
	if name == "" {
		return "instance"
	}
	return name
}

// copyFile copies a regular file and gives the copy the wanted mode.
func copyFile(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
