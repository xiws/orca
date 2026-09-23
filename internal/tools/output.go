package tools

import (
	"bytes"
	"sync"
)

const maxBashOutput = 64 << 10

type cappedOutput struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := min(len(p), maxBashOutput-w.buf.Len())
	_, _ = w.buf.Write(p[:n])
	w.truncated = w.truncated || n < len(p)
	return len(p), nil
}
func (w *cappedOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
func (w *cappedOutput) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}
