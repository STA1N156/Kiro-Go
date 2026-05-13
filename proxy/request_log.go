package proxy

import (
	"encoding/json"
	"kiro-go/config"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RequestLog 单条请求日志
type RequestLog struct {
	ID           string  `json:"id"`
	Timestamp    int64   `json:"timestamp"`
	Model        string  `json:"model"`
	Account      string  `json:"account"`
	APIType      string  `json:"apiType"`
	Stream       bool    `json:"stream"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	Credits      float64 `json:"credits"`
	Duration     int64   `json:"duration"`
	Success      bool    `json:"success"`
	Error        string  `json:"error,omitempty"`
	KeyName      string  `json:"keyName,omitempty"`
}

// RequestLogStore 持久化日志存储
type RequestLogStore struct {
	mu       sync.RWMutex
	entries  []RequestLog
	filePath string
	stop     chan struct{}
}

func NewRequestLogStore(dataDir string) *RequestLogStore {
	s := &RequestLogStore{
		filePath: filepath.Join(dataDir, "request_logs.json"),
		stop:     make(chan struct{}),
	}
	s.load()
	s.cleanup()
	go s.backgroundCleanup()
	return s
}

func (s *RequestLogStore) Add(log RequestLog) {
	s.mu.Lock()
	s.entries = append(s.entries, log)
	s.mu.Unlock()
}

// Recent 返回最近 n 条记录（按时间倒序）
func (s *RequestLogStore) Recent(n int) []RequestLog {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := len(s.entries)
	if total == 0 {
		return nil
	}
	if n <= 0 || n > total {
		n = total
	}

	result := make([]RequestLog, n)
	for i := 0; i < n; i++ {
		result[i] = s.entries[total-1-i]
	}
	return result
}

// Page 返回分页结果（按时间倒序）和总数
func (s *RequestLogStore) Page(page, pageSize int) ([]RequestLog, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := len(s.entries)
	if total == 0 {
		return nil, 0
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	if page <= 0 {
		page = 1
	}

	offset := (page - 1) * pageSize
	if offset >= total {
		return nil, total
	}

	end := total - offset
	start := end - pageSize
	if start < 0 {
		start = 0
	}

	result := make([]RequestLog, end-start)
	for i := 0; i < end-start; i++ {
		result[i] = s.entries[end-1-i]
	}
	return result, total
}

func (s *RequestLogStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Clear 手动清除所有日志
func (s *RequestLogStore) Clear() {
	s.mu.Lock()
	s.entries = nil
	s.mu.Unlock()
	s.save()
}

// Save 持久化到文件
func (s *RequestLogStore) save() {
	s.mu.RLock()
	data, err := json.Marshal(s.entries)
	s.mu.RUnlock()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.filePath), 0755)
	_ = os.WriteFile(s.filePath, data, 0644)
}

func (s *RequestLogStore) load() {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return
	}
	var entries []RequestLog
	if json.Unmarshal(data, &entries) == nil {
		s.entries = entries
	}
}

// cleanup 清理过期日志
func (s *RequestLogStore) cleanup() {
	days := config.GetLogRetentionDays()
	cutoff := time.Now().Unix() - int64(days*86400)

	s.mu.Lock()
	filtered := s.entries[:0]
	for _, e := range s.entries {
		if e.Timestamp >= cutoff {
			filtered = append(filtered, e)
		}
	}
	changed := len(filtered) != len(s.entries)
	s.entries = filtered
	s.mu.Unlock()

	if changed {
		s.save()
	}
}

// backgroundCleanup 每小时清理一次过期日志，每 5 分钟持久化一次
func (s *RequestLogStore) backgroundCleanup() {
	saveTicker := time.NewTicker(5 * time.Minute)
	cleanTicker := time.NewTicker(1 * time.Hour)
	defer saveTicker.Stop()
	defer cleanTicker.Stop()

	for {
		select {
		case <-saveTicker.C:
			s.save()
		case <-cleanTicker.C:
			s.cleanup()
		case <-s.stop:
			s.save()
			return
		}
	}
}

func (s *RequestLogStore) Stop() {
	close(s.stop)
}

func nowMs() int64 {
	return time.Now().UnixMilli()
}
