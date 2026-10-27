// 限制 bash 输出大小的缓冲器，最大 64 KiB
package tools

import (
	"bytes"
	"sync"
)

const maxBashOutput = 64 << 10 // bash 输出上限：64 KiB

// cappedOutput 是一个线程安全的有限缓冲器，
// 超出上限的数据会被截断但不会导致 Write 返回错误。
type cappedOutput struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

// Write 将数据写入缓冲器，超出上限的部分会被丢弃
func (w *cappedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := min(len(p), maxBashOutput-w.buf.Len())
	_, _ = w.buf.Write(p[:n])
	w.truncated = w.truncated || n < len(p)
	return len(p), nil
}

// String 返回已缓冲的输出内容
func (w *cappedOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// Truncated 返回输出是否被截断
func (w *cappedOutput) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}
