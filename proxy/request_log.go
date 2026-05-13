package proxy

import (
	"sync"
	"time"
)

// RequestLog 单条请求日志
type RequestLog struct {
	ID           string  `json:"id"`
	Timestamp    int64   `json:"timestamp"`
	Model        string  `json:"model"`
	Account      string  `json:"account"`
	APIType      string  `json:"apiType"` // "claude" 或 "openai"
	Stream       bool    `json:"stream"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	Credits      float64 `json:"credits"`
	Duration     int64   `json:"duration"` // 毫秒
	Success      bool    `json:"success"`
	Error        string  `json:"error,omitempty"`
}

// RequestLogBuffer 环形缓冲区
type RequestLogBuffer struct {
	mu      sync.RWMutex
	entries []RequestLog
	size    int
	pos     int
	count   int
}

func NewRequestLogBuffer(size int) *RequestLogBuffer {
	return &RequestLogBuffer{
		entries: make([]RequestLog, size),
		size:    size,
	}
}

func (b *RequestLogBuffer) Add(log RequestLog) {
	b.mu.Lock()
	b.entries[b.pos] = log
	b.pos = (b.pos + 1) % b.size
	if b.count < b.size {
		b.count++
	}
	b.mu.Unlock()
}

// Recent 返回最近 n 条记录（按时间倒序）
func (b *RequestLogBuffer) Recent(n int) []RequestLog {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if n <= 0 || b.count == 0 {
		return nil
	}
	if n > b.count {
		n = b.count
	}

	result := make([]RequestLog, n)
	for i := 0; i < n; i++ {
		idx := (b.pos - 1 - i + b.size) % b.size
		result[i] = b.entries[idx]
	}
	return result
}

func (b *RequestLogBuffer) Count() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.count
}

func nowMs() int64 {
	return time.Now().UnixMilli()
}
