package ids

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// crockford is the alphabet of the encoding used by the identifiers. The
// letters I, L, O and U are deliberately absent: they are too easy to confuse
// with other characters.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// TestNewUsesThePrefixAndTheULIDLayout checks the shape of a generated
// identifier.
func TestNewUsesThePrefixAndTheULIDLayout(t *testing.T) {
	id := New(PrefixJob)
	if !strings.HasPrefix(id, PrefixJob+"_") {
		t.Fatalf("the identifier %q does not carry the prefix %q", id, PrefixJob)
	}
	body := strings.TrimPrefix(id, PrefixJob+"_")
	if len(body) != 26 {
		t.Errorf("the body of %q holds %d characters, want 26", id, len(body))
	}
	for _, r := range body {
		if !strings.ContainsRune(crockford, r) {
			t.Errorf("the identifier %q holds the character %q, which is not Crockford base32", id, r)
		}
	}
	if !IsValid(PrefixJob, id) {
		t.Errorf("the identifier %q is not accepted by IsValid", id)
	}
}

// TestNewWithoutAPrefixReturnsTheBareIdentifier checks that an empty prefix
// produces an identifier without a separator.
func TestNewWithoutAPrefixReturnsTheBareIdentifier(t *testing.T) {
	id := New("")
	if strings.Contains(id, "_") {
		t.Fatalf("the identifier %q carries a separator", id)
	}
	if len(id) != 26 {
		t.Errorf("the identifier %q holds %d characters, want 26", id, len(id))
	}
	if !IsValid("", id) {
		t.Errorf("the identifier %q is not accepted by IsValid", id)
	}
}

// TestThePrefixesAreDistinct checks that no two resources share a prefix.
func TestThePrefixesAreDistinct(t *testing.T) {
	prefixes := []string{
		PrefixJob, PrefixInstance, PrefixArtifact, PrefixUser, PrefixAPIKey,
		PrefixSession, PrefixAudit, PrefixUpload, PrefixPairing, PrefixNode,
		PrefixTemplate, PrefixRequest,
	}
	seen := map[string]bool{}
	for _, prefix := range prefixes {
		if prefix == "" {
			t.Error("a prefix is empty")
			continue
		}
		if seen[prefix] {
			t.Errorf("the prefix %q is used twice", prefix)
		}
		seen[prefix] = true
	}
}

// TestTheIdentifiersAreUnique checks that two calls never return the same
// identifier.
func TestTheIdentifiersAreUnique(t *testing.T) {
	seen := make(map[string]bool, 2000)
	for i := 0; i < 2000; i++ {
		id := New(PrefixJob)
		if seen[id] {
			t.Fatalf("the identifier %q was issued twice", id)
		}
		seen[id] = true
	}
}

// TestTheIdentifiersSortByCreationTime checks the property the control plane
// relies on when it orders resources by identifier.
func TestTheIdentifiersSortByCreationTime(t *testing.T) {
	previous := ""
	for i := 0; i < 5; i++ {
		// The identifiers carry a millisecond timestamp, so the clock has to
		// move before the next one is issued.
		time.Sleep(2 * time.Millisecond)
		id := New(PrefixJob)
		if previous != "" && id <= previous {
			t.Fatalf("the identifier %q does not sort after %q", id, previous)
		}
		previous = id
	}
}

// TestTheIdentifiersOfTheSameMillisecondIncrease checks that the identifiers
// issued inside a single millisecond still keep their order.
func TestTheIdentifiersOfTheSameMillisecondIncrease(t *testing.T) {
	previous := ""
	for i := 0; i < 1000; i++ {
		id := New(PrefixJob)
		if previous != "" && id <= previous {
			t.Fatalf("the identifier %q does not sort after %q", id, previous)
		}
		previous = id
	}
}

// TestTimestampReadsTheCreationTime checks that the creation time survives
// the round trip through the identifier.
func TestTimestampReadsTheCreationTime(t *testing.T) {
	before := time.Now().UTC()
	id := New(PrefixJob)
	after := time.Now().UTC()

	stamp, ok := Timestamp(id)
	if !ok {
		t.Fatalf("the timestamp of %q could not be read", id)
	}
	// The identifier carries the millisecond of its creation, so the
	// recorded time may be up to a millisecond off in either direction.
	if stamp.Before(before.Add(-time.Millisecond)) || stamp.After(after.Add(time.Millisecond)) {
		t.Errorf("the timestamp of %q is %s, want a time between %s and %s",
			id, stamp, before, after)
	}
}

// TestTimestampRejectsAMalformedIdentifier checks that a value that is not an
// identifier is refused.
func TestTimestampRejectsAMalformedIdentifier(t *testing.T) {
	cases := []string{"", "job", "job_short", "job_01ARZ3NDEKTSV4RRFFQ69G5FA", "job_01ARZ3NDEKTSV4RRFFQ69G5FAVV"}
	for _, id := range cases {
		if _, ok := Timestamp(id); ok {
			t.Errorf("the malformed identifier %q was accepted", id)
		}
	}
}

// TestIsValid checks the acceptance rules of an identifier.
func TestIsValid(t *testing.T) {
	valid := New(PrefixJob)
	cases := []struct {
		name   string
		prefix string
		id     string
		want   bool
	}{
		{name: "a generated identifier", prefix: PrefixJob, id: valid, want: true},
		{name: "a prefixed identifier checked without a prefix", prefix: "", id: valid, want: false},
		{name: "the body alone", prefix: "", id: strings.TrimPrefix(valid, PrefixJob+"_"), want: true},
		{name: "another prefix", prefix: PrefixInstance, id: valid, want: false},
		{name: "no prefix at all", prefix: PrefixJob, id: strings.TrimPrefix(valid, PrefixJob+"_"), want: false},
		{name: "an empty identifier", prefix: PrefixJob, id: "", want: false},
		{name: "a truncated identifier", prefix: "", id: valid[:20], want: false},
		{name: "an extended identifier", prefix: "", id: valid + "A", want: false},
		{name: "a character outside the alphabet", prefix: "", id: strings.Repeat("U", 26), want: false},
		{name: "a lower case identifier", prefix: "", id: strings.ToLower(valid), want: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := IsValid(test.prefix, test.id); got != test.want {
				t.Errorf("IsValid(%q, %q) = %t, want %t", test.prefix, test.id, got, test.want)
			}
		})
	}
}

// TestRandomHexReturnsTheRequestedNumberOfBytes checks the length of the
// random tokens.
func TestRandomHexReturnsTheRequestedNumberOfBytes(t *testing.T) {
	for _, n := range []int{0, 1, 16, 32} {
		value, err := RandomHex(n)
		if err != nil {
			t.Fatalf("random hex: %v", err)
		}
		if len(value) != 2*n {
			t.Errorf("the token holds %d characters, want %d", len(value), 2*n)
		}
		if _, err := hex.DecodeString(value); err != nil {
			t.Errorf("the token %q is not hexadecimal: %v", value, err)
		}
	}
}

// TestRandomHexReturnsADifferentToken checks that two calls do not return the
// same value.
func TestRandomHexReturnsADifferentToken(t *testing.T) {
	first, err := RandomHex(16)
	if err != nil {
		t.Fatalf("random hex: %v", err)
	}
	second, err := RandomHex(16)
	if err != nil {
		t.Fatalf("random hex: %v", err)
	}
	if first == second {
		t.Errorf("two calls returned the same token %q", first)
	}
}

// TestRandomBytesReturnsTheRequestedNumberOfBytes checks the length of the
// random buffers.
func TestRandomBytesReturnsTheRequestedNumberOfBytes(t *testing.T) {
	for _, n := range []int{0, 1, 16, 64} {
		buf, err := RandomBytes(n)
		if err != nil {
			t.Fatalf("random bytes: %v", err)
		}
		if len(buf) != n {
			t.Errorf("the buffer holds %d bytes, want %d", len(buf), n)
		}
	}
}

// TestRandomBytesReturnsADifferentBuffer checks that two calls do not return
// the same value.
func TestRandomBytesReturnsADifferentBuffer(t *testing.T) {
	first, err := RandomBytes(32)
	if err != nil {
		t.Fatalf("random bytes: %v", err)
	}
	second, err := RandomBytes(32)
	if err != nil {
		t.Fatalf("random bytes: %v", err)
	}
	if string(first) == string(second) {
		t.Error("two calls returned the same buffer")
	}
}

// TestFingerprintIsStable checks that the fingerprint of a value does not
// change between two calls.
func TestFingerprintIsStable(t *testing.T) {
	first := Fingerprint([]byte("the same content"))
	second := Fingerprint([]byte("the same content"))
	if first != second {
		t.Errorf("the fingerprint changed between two calls: %q and %q", first, second)
	}
	if len(first) != 16 {
		t.Errorf("the fingerprint %q holds %d characters, want 16", first, len(first))
	}
	other := Fingerprint([]byte("another content"))
	if other == first {
		t.Error("two different values share a fingerprint")
	}
}
