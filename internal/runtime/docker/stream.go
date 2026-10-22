package docker

import (
	"encoding/binary"
	"errors"
	"io"
)

// maxFrameSize bounds how large a single framed write may claim to be. The
// daemon writes at most a few kilobytes per frame; the limit keeps a broken
// or hostile stream from making the backend allocate without bound.
const maxFrameSize = 32 << 20

// frameHeaderSize is the size of the header that precedes every framed
// write: one byte for the stream, three reserved bytes and a big endian
// length.
const frameHeaderSize = 8

// streamStdout and streamStderr are the frame identifiers used by the
// daemon.
const (
	streamStdin  byte = 0
	streamStdout byte = 1
	streamStderr byte = 2
)

// outputBudget is the shared allowance for the captured output of a session.
// The allowance covers both streams so that a workload cannot fill the
// memory of the daemon with a single stream.
type outputBudget struct {
	// limit is the number of bytes that may be stored. A limit of zero
	// means unlimited.
	limit int64
	// used is the number of bytes that were offered to the budget.
	used int64
	// truncated reports whether output was dropped.
	truncated bool
}

// take reserves room for n bytes and reports how many of them may be
// stored.
func (b *outputBudget) take(n int) int {
	if b.limit <= 0 {
		b.used += int64(n)
		return n
	}
	remaining := b.limit - b.used
	if remaining <= 0 {
		b.truncated = true
		return 0
	}
	if int64(n) > remaining {
		b.used = b.limit
		b.truncated = true
		return int(remaining)
	}
	b.used += int64(n)
	return n
}

// limitedBuffer collects output up to the budget of the session.
type limitedBuffer struct {
	// budget is the shared allowance of the session.
	budget *outputBudget
	// data holds the captured bytes.
	data []byte
}

// Write implements io.Writer. It never fails: output past the budget is
// dropped instead of stopping the session.
func (l *limitedBuffer) Write(p []byte) (int, error) {
	allow := l.budget.take(len(p))
	if allow > 0 {
		l.data = append(l.data, p[:allow]...)
	}
	return len(p), nil
}

// Bytes returns the captured output.
func (l *limitedBuffer) Bytes() []byte { return l.data }

// demuxStream splits a Docker output stream into stdout and stderr.
//
// The daemon frames every write with an eight byte header whenever the
// session has no TTY. A stream that is not framed is passed through to
// stdout, which is what a TTY session produces. The hint says whether the
// daemon announced a framed stream; the first bytes are inspected anyway so
// that an unexpected answer is not mistaken for a frame.
func demuxStream(r io.Reader, stdout, stderr io.Writer, framed bool) error {
	head := make([]byte, frameHeaderSize)
	n, err := io.ReadFull(r, head)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			// Not enough data for a frame header: the stream carries
			// plain bytes.
			if n > 0 {
				if _, werr := stdout.Write(head[:n]); werr != nil {
					return werr
				}
			}
			return nil
		}
		return err
	}
	if !framed || !looksLikeFrame(head) {
		if _, err := stdout.Write(head); err != nil {
			return err
		}
		_, err := io.Copy(stdout, r)
		return err
	}
	for {
		if err := writeFrame(r, head, stdout, stderr); err != nil {
			return err
		}
		if _, err := io.ReadFull(r, head); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
	}
}

// streamFramed reports whether the media type of a session announces a
// framed stream.
//
// A session without a TTY is always framed, whatever the daemon calls the
// media type: the daemon only sends plain bytes to a TTY.
func streamFramed(mediaType string) bool {
	switch mediaType {
	case mediaTypeMultiplexedStream, mediaTypeRawStream:
		return true
	default:
		return false
	}
}

// looksLikeFrame reports whether the bytes start a framed write.
func looksLikeFrame(head []byte) bool {
	if len(head) < frameHeaderSize {
		return false
	}
	if head[0] > streamStderr {
		return false
	}
	if head[1] != 0 || head[2] != 0 || head[3] != 0 {
		return false
	}
	return binary.BigEndian.Uint32(head[4:8]) <= maxFrameSize
}

// writeFrame copies the payload of one framed write to its stream.
func writeFrame(r io.Reader, head []byte, stdout, stderr io.Writer) error {
	size := int64(binary.BigEndian.Uint32(head[4:8]))
	target := stdout
	if head[0] == streamStderr {
		target = stderr
	}
	if size == 0 {
		return nil
	}
	// The payload is streamed so that a large frame never has to be held
	// in memory as a whole.
	_, err := io.CopyN(target, r, size)
	return err
}
