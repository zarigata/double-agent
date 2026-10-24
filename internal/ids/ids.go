// Package ids produces the prefixed, sortable identifiers used across the
// Double Agent control plane.
//
// Identifiers follow the ULID layout (a 48-bit millisecond timestamp followed
// by 80 bits of randomness) rendered in Crockford base32, which keeps them
// lexicographically sortable and matches the "job_01J..." shape shown in
// docs/MCP_API_AND_DISCOVERY.md.
package ids

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"
)

const encoding = "0123456789ABCDEFGHJKMNPQRSTVWXYZ" // Crockford base32

// Prefixes used by the control plane.
const (
	PrefixJob      = "job"
	PrefixInstance = "ins"
	PrefixArtifact = "art"
	PrefixUser     = "usr"
	PrefixAPIKey   = "key"
	PrefixSession  = "ses"
	PrefixAudit    = "aud"
	PrefixUpload   = "upl"
	PrefixPairing  = "pair"
	PrefixNode     = "node"
	PrefixTemplate = "tpl"
	PrefixRequest  = "req"
)

var (
	mu       sync.Mutex
	lastMS   int64
	lastRand [10]byte
)

// New returns a new identifier with the given prefix.
//
// The timestamp portion is monotonic: identifiers generated within the same
// millisecond still sort in creation order.
func New(prefix string) string {
	id, err := newULID()
	if err != nil {
		// crypto/rand failure is fatal for identifier uniqueness; fall back to
		// a time-derived value rather than panicking in a daemon.
		id = fmt.Sprintf("%016x%016x", time.Now().UnixNano(), time.Now().UnixNano())
	}
	if prefix == "" {
		return id
	}
	return prefix + "_" + id
}

// newULID renders a 26 character ULID.
func newULID() (string, error) {
	now := time.Now().UnixMilli()

	mu.Lock()
	if now == lastMS {
		// Increment the random component so same-millisecond IDs stay ordered.
		incr(&lastRand)
	} else {
		lastMS = now
		if _, err := rand.Read(lastRand[:]); err != nil {
			mu.Unlock()
			return "", err
		}
	}
	entropy := lastRand
	mu.Unlock()

	var raw [16]byte
	raw[0] = byte(now >> 40)
	raw[1] = byte(now >> 32)
	raw[2] = byte(now >> 24)
	raw[3] = byte(now >> 16)
	raw[4] = byte(now >> 8)
	raw[5] = byte(now)
	copy(raw[6:], entropy[:])

	return encodeULID(raw), nil
}

// incr adds one to a big-endian byte slice.
func incr(b *[10]byte) {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return
		}
	}
}

// encodeULID renders 16 bytes as 26 Crockford base32 characters.
func encodeULID(raw [16]byte) string {
	// 128 bits rendered as 26 base32 characters (130 bits, zero padded).
	// Copy the 10 entropy bytes into the low 80 bits.
	var bits [16]byte
	copy(bits[:], raw[:])

	// Standard ULID encoding: treat the value as a 128-bit big-endian integer.
	var out [26]byte
	// Extract 5 bits at a time from the most significant end. The most
	// significant group is written first so that the identifier sorts
	// lexicographically by creation time and the timestamp can be read back
	// from the front of the string.
	var acc uint16
	accBits := 0
	idx := 0
	for _, b8 := range bits {
		acc = acc<<8 | uint16(b8)
		accBits += 8
		for accBits >= 5 {
			accBits -= 5
			out[idx] = encoding[(acc>>accBits)&0x1f]
			idx++
		}
	}
	if accBits > 0 && idx < len(out) {
		out[idx] = encoding[(acc<<(5-accBits))&0x1f]
		idx++
	}
	for ; idx < len(out); idx++ {
		out[idx] = encoding[0]
	}
	return string(out[:])
}

// IsValid reports whether s is a well formed identifier for the prefix.
func IsValid(prefix, s string) bool {
	if prefix != "" {
		if !strings.HasPrefix(s, prefix+"_") {
			return false
		}
		s = strings.TrimPrefix(s, prefix+"_")
	}
	if len(s) != 26 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune(encoding, r) {
			return false
		}
	}
	return true
}

// Timestamp extracts the creation time encoded in a ULID identifier.
func Timestamp(id string) (time.Time, bool) {
	s := id
	if i := strings.IndexByte(s, '_'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) != 26 {
		return time.Time{}, false
	}
	var raw [16]byte
	var acc uint32
	accBits := 0
	pos := 0
	for _, r := range s {
		v := strings.IndexRune(encoding, r)
		if v < 0 {
			return time.Time{}, false
		}
		acc = acc<<5 | uint32(v)
		accBits += 5
		if accBits >= 8 {
			accBits -= 8
			if pos < len(raw) {
				raw[pos] = byte(acc >> accBits)
				pos++
			}
		}
	}
	if pos < 6 {
		return time.Time{}, false
	}
	ms := int64(raw[0])<<40 | int64(raw[1])<<32 | int64(raw[2])<<24 |
		int64(raw[3])<<16 | int64(raw[4])<<8 | int64(raw[5])
	return time.UnixMilli(ms).UTC(), true
}

// RandomHex returns n random bytes hex encoded. It is used for tokens and
// nonces that must not be guessable.
func RandomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", buf), nil
}

// RandomBytes returns n cryptographically random bytes.
func RandomBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// machineID derives a stable 64 bit identifier from a string, used for
// deterministic naming of control plane resources.
func machineID(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// Fingerprint renders a stable short identifier for a byte string.
func Fingerprint(b []byte) string {
	return fmt.Sprintf("%016x", binary.BigEndian.Uint64([]byte{byte(machineID(string(b)) >> 56), byte(machineID(string(b)) >> 48), byte(machineID(string(b)) >> 40), byte(machineID(string(b)) >> 32), byte(machineID(string(b)) >> 24), byte(machineID(string(b)) >> 16), byte(machineID(string(b)) >> 8), byte(machineID(string(b)))}))
}
