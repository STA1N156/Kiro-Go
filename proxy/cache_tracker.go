package proxy

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"hash"
	"kiro-go/config"
	"strconv"
	"sync"
	"time"
)

const cacheShardCount = 32
const cacheEntriesPerShard = 4096

// Only hashes and counters are retained, shared across accounts and models.
type promptCacheEntry struct {
	Hash      [32]byte
	Model     [32]byte
	UpdatedAt int64
	Tokens    int64
}
type promptCacheShard struct {
	sync.RWMutex
	entries map[[32]byte]promptCacheEntry
}
type promptCacheTracker struct {
	shards [cacheShardCount]promptCacheShard
	jobs   chan struct{}
}
type promptCacheBreakpoint struct {
	Fingerprint [32]byte
	Weight      int
}
type promptCacheProfile struct {
	ready          chan struct{}
	Breakpoints    []promptCacheBreakpoint
	TotalWeight    int
	Model          [32]byte
	MatchedWeight  int
	ObservedTokens int
	Exact          bool
}
type promptCacheUsage struct {
	CacheReadInputTokens int
}
type cachedTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

func newPromptCacheTracker() *promptCacheTracker {
	t := &promptCacheTracker{jobs: make(chan struct{}, 8)}
	for i := range t.shards {
		t.shards[i].entries = make(map[[32]byte]promptCacheEntry)
	}
	return t
}

// Hashing runs alongside upstream I/O. At saturation skip caching instead of
// queueing unbounded conversations or delaying first byte.
func (t *promptCacheTracker) begin(model string, build func(*cacheFingerprintBuilder)) *promptCacheProfile {
	if t == nil || !config.GetLocalCacheSettings().Enabled {
		return nil
	}
	select {
	case t.jobs <- struct{}{}:
	default:
		return nil
	}
	p := &promptCacheProfile{ready: make(chan struct{}), Model: sha256.Sum256([]byte(model))}
	go func() {
		defer func() { <-t.jobs; close(p.ready) }()
		b := &cacheFingerprintBuilder{hasher: sha256.New(), ids: make(map[string]string), profile: p}
		build(b)
		t.lookup(p)
	}()
	return p
}

func (t *promptCacheTracker) BuildClaudeProfile(req *ClaudeRequest) *promptCacheProfile {
	// Capture slice headers before web-search loops append internal turns.
	snapshot := *req
	return t.begin(req.Model, func(b *cacheFingerprintBuilder) {
		b.add("format", "anthropic")
		if len(snapshot.Tools) > 0 {
			b.add("tools", snapshot.Tools)
			b.checkpoint()
		}
		if snapshot.System != nil {
			b.add("system", normalizeCacheContent(snapshot.System, b.ids))
			b.checkpoint()
		}
		for _, msg := range snapshot.Messages {
			b.add("message", map[string]interface{}{"role": msg.Role, "content": normalizeCacheContent(msg.Content, b.ids)})
			b.checkpoint()
		}
	})
}

func (t *promptCacheTracker) BuildOpenAIProfile(req *OpenAIRequest) *promptCacheProfile {
	snapshot := *req
	return t.begin(req.Model, func(b *cacheFingerprintBuilder) {
		b.add("format", "openai")
		if len(snapshot.Tools) > 0 {
			b.add("tools", snapshot.Tools)
			b.checkpoint()
		}
		for _, msg := range snapshot.Messages {
			value := map[string]interface{}{"role": msg.Role, "content": msg.Content}
			if msg.ToolCallID != "" {
				value["tool_call_id"] = cacheToolID(b.ids, msg.ToolCallID)
			}
			if len(msg.ToolCalls) > 0 {
				calls := append([]ToolCall(nil), msg.ToolCalls...)
				for i := range calls {
					calls[i].ID = cacheToolID(b.ids, calls[i].ID)
				}
				value["tool_calls"] = calls
			}
			b.add("message", value)
			b.checkpoint()
		}
	})
}

type cacheFingerprintBuilder struct {
	hasher  hash.Hash
	ids     map[string]string
	profile *promptCacheProfile
}

func (b *cacheFingerprintBuilder) add(kind string, value interface{}) {
	data, _ := json.Marshal(value) // encoding/json sorts map keys deterministically.
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(data)))
	b.hasher.Write([]byte(kind))
	b.hasher.Write(size[:])
	b.hasher.Write(data)
	b.profile.TotalWeight += len(data) + len(kind) + len(size)
}
func (b *cacheFingerprintBuilder) checkpoint() {
	var key [32]byte
	copy(key[:], b.hasher.Sum(nil))
	b.profile.Breakpoints = append(b.profile.Breakpoints, promptCacheBreakpoint{key, b.profile.TotalWeight})
}
func cacheToolID(ids map[string]string, id string) string {
	if id == "" {
		return id
	}
	if v, ok := ids[id]; ok {
		return v
	}
	v := "call_" + strconv.Itoa(len(ids)+1)
	ids[id] = v
	return v
}

// Normalize protocol blocks only, never tool arguments or JSON schemas.
func normalizeCacheContent(value interface{}, ids map[string]string) interface{} {
	blocks, ok := value.([]interface{})
	if !ok {
		return value
	}
	out := make([]interface{}, 0, len(blocks))
	for _, item := range blocks {
		block, ok := item.(map[string]interface{})
		if !ok {
			out = append(out, item)
			continue
		}
		copyBlock := make(map[string]interface{}, len(block))
		for key, v := range block {
			if key != "cache_control" {
				copyBlock[key] = v
			}
		}
		switch block["type"] {
		case "tool_use", "server_tool_use":
			if id, ok := block["id"].(string); ok {
				copyBlock["id"] = cacheToolID(ids, id)
			}
		case "tool_result", "web_search_tool_result":
			if id, ok := block["tool_use_id"].(string); ok {
				copyBlock["tool_use_id"] = cacheToolID(ids, id)
			}
		}
		out = append(out, copyBlock)
	}
	return out
}

func (t *promptCacheTracker) lookup(p *promptCacheProfile) {
	ttl := time.Duration(config.GetLocalCacheSettings().TTLMinutes) * time.Minute
	cutoff := time.Now().Add(-ttl).UnixNano()
	for i := len(p.Breakpoints) - 1; i >= 0; i-- {
		point := p.Breakpoints[i]
		shard := &t.shards[int(point.Fingerprint[0])%cacheShardCount]
		shard.RLock()
		entry, ok := shard.entries[point.Fingerprint]
		shard.RUnlock()
		if !ok || entry.UpdatedAt <= cutoff {
			continue
		}
		p.MatchedWeight, p.Exact = point.Weight, i == len(p.Breakpoints)-1
		if entry.Model == p.Model {
			p.ObservedTokens = int(entry.Tokens)
		}
		return
	}
}
func (p *promptCacheProfile) cachedTokens(inputTokens int) int {
	if p == nil || inputTokens <= 0 {
		return 0
	}
	<-p.ready
	limit := int(int64(inputTokens) * 998 / 1000)
	if p.Exact {
		return limit
	}
	if p.ObservedTokens > 0 {
		return minInt(limit, p.ObservedTokens)
	}
	if p.TotalWeight == 0 {
		return 0
	}
	return minInt(limit, int(int64(inputTokens)*int64(p.MatchedWeight)/int64(p.TotalWeight)))
}

// Only successful completions warm the cache. No disk I/O on the request path.
func (t *promptCacheTracker) finish(p *promptCacheProfile, inputTokens int) promptCacheUsage {
	if t == nil || p == nil || !config.GetLocalCacheSettings().Enabled {
		return promptCacheUsage{}
	}
	usage := promptCacheUsage{CacheReadInputTokens: p.cachedTokens(inputTokens)}
	<-p.ready
	stamp := time.Now().UnixNano()
	for i, point := range p.Breakpoints {
		shard := &t.shards[int(point.Fingerprint[0])%cacheShardCount]
		shard.Lock()
		entry, exists := shard.entries[point.Fingerprint]
		if !exists && len(shard.entries) >= cacheEntriesPerShard {
			// Constant-time capacity eviction; no full-index scan on requests.
			for key := range shard.entries {
				delete(shard.entries, key)
				break
			}
		}
		entry.Hash, entry.UpdatedAt = point.Fingerprint, stamp
		if i == len(p.Breakpoints)-1 {
			entry.Model, entry.Tokens = p.Model, int64(inputTokens)
		}
		shard.entries[point.Fingerprint] = entry
		shard.Unlock()
	}
	return usage
}
func billedClaudeInputTokens(inputTokens int, usage promptCacheUsage) int {
	return maxInt(inputTokens-usage.CacheReadInputTokens, 0)
}
func buildClaudeUsageMap(inputTokens, outputTokens int, usage promptCacheUsage, includeCache bool) map[string]interface{} {
	result := map[string]interface{}{"input_tokens": billedClaudeInputTokens(inputTokens, usage), "output_tokens": outputTokens}
	if includeCache {
		result["cache_creation_input_tokens"] = 0
		result["cache_read_input_tokens"] = usage.CacheReadInputTokens
	}
	return result
}

func (t *promptCacheTracker) openAIUsage(profile *promptCacheProfile, inputTokens, outputTokens int) map[string]interface{} {
	usage := map[string]interface{}{"prompt_tokens": inputTokens, "completion_tokens": outputTokens, "total_tokens": inputTokens + outputTokens}
	if profile != nil {
		usage["prompt_tokens_details"] = cachedTokenDetails{t.finish(profile, inputTokens).CacheReadInputTokens}
	}
	return usage
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
