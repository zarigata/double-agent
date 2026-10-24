package docker

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// tarFile is a single file that is packed into a container archive.
type tarFile struct {
	// Name is the archive entry name. It is relative to the directory the
	// archive is unpacked in.
	Name string
	// Mode is the file mode.
	Mode int64
	// Data is the file content.
	Data []byte
}

// buildTar packs files into an uncompressed tar archive.
//
// The container archive API expects a tar stream, not a single file, so the
// entry name is the name of the file inside the target directory.
func buildTar(files []tarFile) ([]byte, error) {
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	for _, file := range files {
		header := &tar.Header{
			Name:     file.Name,
			Mode:     file.Mode,
			Size:     int64(len(file.Data)),
			Typeflag: tar.TypeReg,
		}
		if err := writer.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("docker: write tar header for %s: %w", file.Name, err)
		}
		if _, err := writer.Write(file.Data); err != nil {
			return nil, fmt.Errorf("docker: write tar payload for %s: %w", file.Name, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("docker: close tar archive: %w", err)
	}
	return buf.Bytes(), nil
}

// extractTar reads a regular file from a tar archive.
//
// The daemon answers a file download with an archive that holds the file
// itself; a directory download holds the directory and everything below it.
// The entry that matches the wanted name wins, and the first regular file is
// used as a fallback.
func extractTar(r io.Reader, want string, maxSize int64) (string, []byte, error) {
	reader := tar.NewReader(r)
	var fallbackName string
	var fallbackData []byte
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, fmt.Errorf("docker: read tar archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if maxSize > 0 && header.Size > maxSize {
			return "", nil, fmt.Errorf("docker: archived file %s is larger than the %d byte limit", header.Name, maxSize)
		}
		data, err := io.ReadAll(io.LimitReader(reader, maxSize+1))
		if err != nil {
			return "", nil, fmt.Errorf("docker: read archived file %s: %w", header.Name, err)
		}
		if int64(len(data)) > maxSize && maxSize > 0 {
			return "", nil, fmt.Errorf("docker: archived file %s is larger than the %d byte limit", header.Name, maxSize)
		}
		if want != "" && header.Name == want {
			return header.Name, data, nil
		}
		if fallbackName == "" {
			fallbackName = header.Name
			fallbackData = data
		}
	}
	if fallbackName == "" {
		return "", nil, fmt.Errorf("docker: archive holds no regular file")
	}
	return fallbackName, fallbackData, nil
}
