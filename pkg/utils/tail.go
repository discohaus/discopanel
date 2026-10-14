package utils

// Keeps only the newest bytes written up to a cap
type TailWriter struct {
	max     int
	buf     []byte
	dropped int64
}

// Tail writer retaining at most max bytes
func NewTailWriter(max int) *TailWriter {
	if max < 1 {
		max = 1
	}
	return &TailWriter{max: max}
}

// Accepts every byte, discarding the oldest past the cap
func (t *TailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= t.max {
		t.dropped += int64(len(t.buf)) + int64(len(p)-t.max)
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return n, nil
	}
	if overflow := len(t.buf) + len(p) - t.max; overflow > 0 {
		t.dropped += int64(overflow)
		t.buf = t.buf[:copy(t.buf, t.buf[overflow:])]
	}
	t.grow(len(p))
	t.buf = append(t.buf, p...)
	return n, nil
}

// Grows capacity toward the cap without ever passing it
func (t *TailWriter) grow(extra int) {
	need := len(t.buf) + extra
	if need <= cap(t.buf) {
		return
	}
	size := 2 * cap(t.buf)
	if size < need {
		size = need
	}
	if size > t.max {
		size = t.max
	}
	grown := make([]byte, len(t.buf), size)
	copy(grown, t.buf)
	t.buf = grown
}

// Bytes discarded so far
func (t *TailWriter) Dropped() int64 {
	return t.dropped
}

// Retained bytes, newest last
func (t *TailWriter) Bytes() []byte {
	return t.buf
}

// Retained bytes as a string
func (t *TailWriter) String() string {
	return string(t.buf)
}

// Keeps the last max bytes of s, reports the cut
func TailString(s string, max int) (string, int64) {
	if max < 0 {
		max = 0
	}
	if len(s) <= max {
		return s, 0
	}
	return s[len(s)-max:], int64(len(s) - max)
}
