package proxy

import (
	"bytes"
	"encoding/json"
	"kiro-go/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func initCacheConfig(t *testing.T) {
	t.Helper()
	if err := config.Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatal(err)
	}
}
func TestPromptCacheExactPrefixAndModels(t *testing.T) {
	initCacheConfig(t)
	tracker := newPromptCacheTracker()
	req := &OpenAIRequest{Model: "sonnet", Messages: []OpenAIMessage{{Role: "user", Content: "a repeated prompt"}}}
	if got := tracker.finish(tracker.BuildOpenAIProfile(req), 10000).CacheReadInputTokens; got != 0 {
		t.Fatalf("cold hit: %d", got)
	}
	for _, tokens := range []int{1, 100, 1000, 10000} {
		if got := tracker.finish(tracker.BuildOpenAIProfile(req), tokens).CacheReadInputTokens; got != tokens*998/1000 {
			t.Fatalf("99.8%% cap: tokens=%d got=%d", tokens, got)
		}
	}
	req.Model = "opus"
	if got := tracker.finish(tracker.BuildOpenAIProfile(req), 20000).CacheReadInputTokens; got != 19960 {
		t.Fatalf("cross-model exact: %d", got)
	}
	req.Messages = append(req.Messages, OpenAIMessage{Role: "assistant", Content: "answer"}, OpenAIMessage{Role: "user", Content: "continue"})
	if got := tracker.finish(tracker.BuildOpenAIProfile(req), 25000).CacheReadInputTokens; got != 20000 {
		t.Fatalf("observed same-model prefix: %d", got)
	}
	req.Messages[0].Content = "different beginning"
	if got := tracker.finish(tracker.BuildOpenAIProfile(req), 25000).CacheReadInputTokens; got != 0 {
		t.Fatalf("unrelated hit: %d", got)
	}
}

func TestPromptCacheToolsAndSchemas(t *testing.T) {
	initCacheConfig(t)
	tracker := newPromptCacheTracker()
	makeReq := func(id string, arg int) *ClaudeRequest {
		return &ClaudeRequest{Model: "sonnet", Messages: []ClaudeMessage{
			{Role: "user", Content: "call tool"},
			{Role: "assistant", Content: []interface{}{map[string]interface{}{"type": "tool_use", "id": id, "name": "tool", "input": map[string]interface{}{"cache_control": arg}}}},
			{Role: "user", Content: []interface{}{map[string]interface{}{"type": "tool_result", "tool_use_id": id, "content": "result"}}},
		}}
	}
	req := makeReq("first", 1)
	tracker.finish(tracker.BuildClaudeProfile(req), 100)
	profile := tracker.BuildClaudeProfile(makeReq("second", 1))
	if got := tracker.finish(profile, 100).CacheReadInputTokens; got != 99 {
		t.Fatalf("tool IDs not normalized: %d", got)
	}
	profile = tracker.BuildClaudeProfile(makeReq("second", 2))
	<-profile.ready
	if profile.Exact {
		t.Fatal("tool argument named cache_control must not be stripped")
	}
	if req.Messages[1].Content.([]interface{})[0].(map[string]interface{})["id"] != "first" {
		t.Fatal("request mutated")
	}
}

func TestPromptCacheCrossModelPrefixUsesProportion(t *testing.T) {
	initCacheConfig(t)
	tracker := newPromptCacheTracker()
	req := &OpenAIRequest{Model: "a", Messages: []OpenAIMessage{{Role: "user", Content: strings.Repeat("hello ", 100)}}}
	tracker.finish(tracker.BuildOpenAIProfile(req), 9000)
	req.Model = "b"
	req.Messages = append(req.Messages, OpenAIMessage{Role: "user", Content: "continue"})
	profile := tracker.BuildOpenAIProfile(req)
	got := tracker.finish(profile, 1000).CacheReadInputTokens
	expected := minInt(998, int(int64(1000)*int64(profile.MatchedWeight)/int64(profile.TotalWeight)))
	if got != expected || profile.ObservedTokens != 0 {
		t.Fatalf("cross-model token count leaked: %d want %d", got, expected)
	}
}

func TestPromptCacheExpiryDisableAndPersistence(t *testing.T) {
	initCacheConfig(t)
	tracker := newPromptCacheTracker()
	req := &OpenAIRequest{Model: "a", Messages: []OpenAIMessage{{Role: "user", Content: "private words never on disk"}}}
	profile := tracker.BuildOpenAIProfile(req)
	tracker.finish(profile, 100)
	path := filepath.Join(t.TempDir(), "prompt-cache.bin")
	if err := tracker.saveIndex(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("private")) {
		t.Fatal("plaintext persisted")
	}
	restored := newPromptCacheTracker()
	if err := restored.loadIndex(path); err != nil {
		t.Fatal(err)
	}
	if got := restored.finish(restored.BuildOpenAIProfile(req), 100).CacheReadInputTokens; got != 99 {
		t.Fatalf("restart hit: %d", got)
	}
	key := profile.Breakpoints[0].Fingerprint
	shard := &tracker.shards[int(key[0])%cacheShardCount]
	shard.Lock()
	entry := shard.entries[key]
	entry.UpdatedAt = time.Now().Add(-6 * time.Minute).UnixNano()
	shard.entries[key] = entry
	shard.Unlock()
	if got := tracker.BuildOpenAIProfile(req).cachedTokens(100); got != 0 {
		t.Fatal("expired entry matched")
	}
	inflight := restored.BuildOpenAIProfile(req)
	if err := config.UpdateLocalCacheSettings(config.LocalCacheSettings{Enabled: false, TTLMinutes: 5}); err != nil {
		t.Fatal(err)
	}
	if tracker.BuildOpenAIProfile(req) != nil {
		t.Fatal("disabled cache starts work")
	}
	if restored.finish(inflight, 100).CacheReadInputTokens != 0 {
		t.Fatal("disabled inflight injected usage")
	}
	if _, ok := tracker.openAIUsage(nil, 100, 50)["prompt_tokens_details"]; ok {
		t.Fatal("disabled added usage")
	}
	if err := config.UpdateLocalCacheSettings(config.LocalCacheSettings{Enabled: true, TTLMinutes: 0}); err == nil {
		t.Fatal("accepted zero TTL")
	}
}

func TestPromptCacheUsageAccounting(t *testing.T) {
	initCacheConfig(t)
	m := buildClaudeUsageMap(100, 50, promptCacheUsage{CacheReadInputTokens: 99}, true)
	if m["input_tokens"] != 1 || m["cache_read_input_tokens"] != 99 || m["output_tokens"] != 50 || m["cache_creation_input_tokens"] != 0 {
		t.Fatalf("bad Claude usage: %#v", m)
	}
	raw, _ := json.Marshal(ResponsesUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150, InputTokensDetails: &cachedTokenDetails{99}})
	if !bytes.Contains(raw, []byte(`"cached_tokens":99`)) {
		t.Fatal(string(raw))
	}
}

func TestPromptCacheConcurrentRequestsAndStorage(t *testing.T) {
	initCacheConfig(t)
	tracker := newPromptCacheTracker()
	path := filepath.Join(t.TempDir(), "prompt-cache.bin")
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				req := &OpenAIRequest{Model: "a", Messages: []OpenAIMessage{{Role: "user", Content: "repeat"}}}
				tracker.finish(tracker.BuildOpenAIProfile(req), 100)
			}
		}()
	}
	for i := 0; i < 3; i++ {
		if err := tracker.saveIndex(path); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if got := tracker.finish(tracker.BuildOpenAIProfile(&OpenAIRequest{Model: "a", Messages: []OpenAIMessage{{Role: "user", Content: "repeat"}}}), 100).CacheReadInputTokens; got != 99 {
		t.Fatalf("warm hit: %d", got)
	}
}

func BenchmarkPromptCacheLongContext(b *testing.B) {
	tracker := newPromptCacheTracker()
	req := &OpenAIRequest{Model: "sonnet"}
	for i := 0; i < 100; i++ {
		req.Messages = append(req.Messages, OpenAIMessage{Role: "user", Content: strings.Repeat("long roleplay context ", 500)})
	}
	b.SetBytes(1050000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		profile := tracker.BuildOpenAIProfile(req)
		tracker.finish(profile, 200000)
	}
}

func TestPromptCacheProtocolRoundTrips(t *testing.T) {
	for _, protocol := range []string{"openai", "responses", "claude"} {
		t.Run(protocol, func(t *testing.T) {
			h, cleanup := setupResponsesTestHandler(t)
			defer cleanup()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "hello"}))
			}))
			defer upstream.Close()
			defer swapKiroEndpointsForTest(t, upstream)()
			for i, stream := range []bool{false, true, false} {
				if i == 2 {
					if err := config.UpdateLocalCacheSettings(config.LocalCacheSettings{Enabled: false, TTLMinutes: 5}); err != nil {
						t.Fatal(err)
					}
				}
				body := map[string]interface{}{"model": "claude-sonnet-4.5", "stream": stream, "store": false,
					"messages": []map[string]string{{"role": "user", "content": strings.Repeat("context ", 100)}}}
				if protocol == "responses" {
					body["input"] = body["messages"]
					delete(body, "messages")
				}
				if protocol == "claude" {
					body["max_tokens"] = 100
				}
				raw, _ := json.Marshal(body)
				r := httptest.NewRequest("POST", "/", bytes.NewReader(raw))
				w := httptest.NewRecorder()
				switch protocol {
				case "openai":
					h.handleOpenAIChat(w, r)
				case "responses":
					h.handleOpenAIResponses(w, r)
				case "claude":
					h.handleClaudeMessages(w, r)
				}
				if w.Code != 200 {
					t.Fatalf("%d: %s", w.Code, w.Body.String())
				}
				var usage map[string]interface{}
				readUsage := func(raw string) {
					var event map[string]interface{}
					if json.Unmarshal([]byte(raw), &event) != nil {
						return
					}
					if response, ok := event["response"].(map[string]interface{}); ok {
						event = response
					}
					if candidate, ok := event["usage"].(map[string]interface{}); ok {
						usage = candidate
					}
				}
				if stream {
					for _, line := range strings.Split(w.Body.String(), "\n") {
						if strings.HasPrefix(line, "data: ") {
							readUsage(strings.TrimPrefix(line, "data: "))
						}
					}
				} else {
					readUsage(w.Body.String())
				}
				if usage == nil {
					t.Fatalf("missing usage: %s", w.Body.String())
				}
				input, cached := 0, 0
				switch protocol {
				case "claude":
					input = int(usage["input_tokens"].(float64))
					if n, ok := usage["cache_read_input_tokens"].(float64); ok {
						cached = int(n)
					}
					input += cached
				case "openai":
					input = int(usage["prompt_tokens"].(float64))
					if d, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
						cached = int(d["cached_tokens"].(float64))
					}
				case "responses":
					input = int(usage["input_tokens"].(float64))
					if d, ok := usage["input_tokens_details"].(map[string]interface{}); ok {
						cached = int(d["cached_tokens"].(float64))
					}
				}
				want := 0
				if i == 1 {
					want = input * 998 / 1000
				}
				if input <= 0 || cached != want {
					t.Fatalf("step %d input=%d cached=%d want=%d; usage=%v", i, input, cached, want, usage)
				}
			}
		})
	}
}

func TestPromptCacheSettingsAPI(t *testing.T) {
	initCacheConfig(t)
	h := &Handler{}
	w := httptest.NewRecorder()
	h.apiGetSettings(w, httptest.NewRequest("GET", "/", nil))
	var result struct{ LocalCache config.LocalCacheSettings }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.LocalCache.Enabled || result.LocalCache.TTLMinutes != 5 {
		t.Fatalf("defaults: %+v", result)
	}
	for _, body := range []string{`{"localCache":{"enabled":false,"ttlMinutes":10}}`, `{"localCache":{"enabled":true,"ttlMinutes":10080}}`} {
		w = httptest.NewRecorder()
		h.apiUpdateSettings(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		if err := config.Load(); err != nil {
			t.Fatal(err)
		}
	}
	if s := config.GetLocalCacheSettings(); !s.Enabled || s.TTLMinutes != 10080 {
		t.Fatalf("settings not persisted: %+v", s)
	}
	w = httptest.NewRecorder()
	h.apiUpdateSettings(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"localCache":{"enabled":false,"ttlMinutes":0}}`)))
	if w.Code != 400 || !config.GetLocalCacheSettings().Enabled {
		t.Fatal("invalid setting changed config")
	}
}
