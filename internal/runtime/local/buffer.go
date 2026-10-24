package local

// outputBudget is the shared allowance for the captured output of a command.
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

// limitedBuffer collects output up to the budget of a command.
type limitedBuffer struct {
	// budget is the shared allowance of the command.
	budget *outputBudget
	// data holds the captured bytes.
	data []byte
}

// Write implements io.Writer. It never fails: output past the budget is
// dropped instead of stopping the command.
func (l *limitedBuffer) Write(p []byte) (int, error) {
	allow := l.budget.take(len(p))
	if allow > 0 {
		l.data = append(l.data, p[:allow]...)
	}
	return len(p), nil
}

// Bytes returns the captured output.
func (l *limitedBuffer) Bytes() []byte { return l.data }
