package proxy

import (
	"bytes"
	"encoding/json"
	"kiro-go/config"
	accountpool "kiro-go/pool"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeThinkingForwarding(t *testing.T) {
	for _, protocol := range []string{"openai", "claude", "responses"} {
		for _, suffix := range []string{"", "-thinking"} {
			for _, stream := range []bool{false, true} {
				t.Run(protocol+suffix+map[bool]string{false: "/json", true: "/stream"}[stream], func(t *testing.T) {
					if err := config.Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
						t.Fatal(err)
					}
					account := config.Account{ID: "native-test", Enabled: true, AccessToken: "test-token", ExpiresAt: time.Now().Add(time.Hour).Unix(), ProxyURL: kiroRetryTestProxyURL, ProfileArn: "arn:aws:codewhisperer:us-east-1:123456789012:profile/test"}
					if err := config.AddAccount(account); err != nil {
						t.Fatal(err)
					}
					installKiroStreamTestClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
						var payload KiroPayload
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Fatal(err)
						}
						fields := payload.AdditionalModelRequestFields
						mode, ok := fields["thinking"].(map[string]interface{})
						if !ok {
							t.Fatal("missing native thinking fields")
						}
						if suffix == "" {
							if mode["type"] != "disabled" {
								t.Fatalf("plain model not disabled: %#v", fields)
							}
						} else if mode["type"] != "adaptive" || mode["display"] != "summarized" || fields["output_config"].(map[string]interface{})["effort"] != "high" {
							t.Fatalf("thinking model not high: %#v", fields)
						}
						// Even unexpected upstream reasoning on a disabled request must not be hidden.
						var body bytes.Buffer
						body.Write(awsEventStreamFrame(t, "reasoningContentEvent", map[string]interface{}{"text": "alpha"}))
						body.Write(awsEventStreamFrame(t, "reasoningContentEvent", map[string]interface{}{"text": "beta"}))
						body.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "answer"}))
						return kiroStreamTestResponse(bytes.NewReader(body.Bytes())), nil
					}))
					p := accountpool.GetPool()
					p.Reload()
					h := &Handler{pool: p, promptCache: newPromptCacheTracker()}
					body := map[string]interface{}{"model": "claude-opus-4.6" + suffix, "max_tokens": 4096, "stream": stream, "messages": []map[string]string{{"role": "user", "content": "hello"}}}
					if protocol == "claude" {
						body["thinking"] = map[string]string{"type": "adaptive", "display": "omitted"}
					}
					if protocol == "responses" {
						body["input"] = "hello"
						body["store"] = false
					}
					encoded, _ := json.Marshal(body)
					req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(encoded))
					rec := httptest.NewRecorder()
					switch protocol {
					case "openai":
						h.handleOpenAIChat(rec, req)
					case "claude":
						h.handleClaudeMessages(rec, req)
					case "responses":
						h.handleOpenAIResponses(rec, req)
					}
					result := rec.Body.String()
					if rec.Code != 200 || !strings.Contains(result, "alpha") || !strings.Contains(result, "beta") || !strings.Contains(result, "answer") {
						t.Fatalf("reasoning or answer lost, HTTP %d: %s", rec.Code, result)
					}
					if protocol == "responses" && stream {
						ids := map[string]float64{}
						var complete ResponsesObject
						for _, line := range strings.Split(result, "\n") {
							if !strings.HasPrefix(line, "data: ") {
								continue
							}
							var event map[string]interface{}
							if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
								continue
							}
							if event["type"] == "response.output_item.added" {
								item := event["item"].(map[string]interface{})
								ids[item["id"].(string)] = event["output_index"].(float64)
							}
							if event["type"] == "response.completed" {
								data, _ := json.Marshal(event["response"])
								json.Unmarshal(data, &complete)
							}
						}
						if len(complete.Output) != 2 || complete.Output[0].Type != "reasoning" || complete.Output[0].Summary[0].Text != "alphabeta" {
							t.Fatalf("invalid final output: %#v", complete.Output)
						}
						for i, item := range complete.Output {
							index, ok := ids[item.ID]
							if !ok || int(index) != i {
								t.Fatal("stream and final item IDs/indices differ")
							}
						}
					}
				})
			}
		}
	}
}

func TestStatsSaverFlushesOnTickAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Init(path); err != nil {
		t.Fatal(err)
	}
	key, err := config.AddApiKey(config.ApiKeyEntry{Key: "sk-flush", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{stopStatsSaver: make(chan struct{}), stopRefresh: make(chan struct{}), statsSaverDone: make(chan struct{})}
	go h.backgroundStatsSaver()
	t.Cleanup(h.Close)
	h.recordSuccessForApiKey(key.ID, 100, 20, 1)
	readSaved := func() config.Config {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var saved config.Config
		if err := json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		return saved
	}
	deadline := time.Now().Add(5 * time.Second)
	for readSaved().TotalRequests == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if saved := readSaved(); saved.TotalRequests != 1 || saved.ApiKeys[0].TokensUsed != 120 {
		t.Fatal("periodic batch not saved")
	}
	h.recordSuccessForApiKey(key.ID, 100, 20, 1)
	h.Close()
	if saved := readSaved(); saved.TotalRequests != 2 || saved.TotalTokens != 240 || saved.ApiKeys[0].TokensUsed != 240 {
		t.Fatal("close did not flush final usage")
	}
}

func TestNonStreamLongReplyAndReasoning(t *testing.T) {
	const pieces = 512
	textChunk, thoughtChunk := "正文内容🙂\n", "思考步骤。\n"
	wantText, wantThought := strings.TrimSpace(strings.Repeat(textChunk, pieces)), strings.Repeat(thoughtChunk, pieces)
	for _, protocol := range []string{"openai", "claude", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			h, cleanup := setupResponsesTestHandler(t)
			defer cleanup()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for i := 0; i < pieces; i++ {
					w.Write(awsEventStreamFrame(t, "reasoningContentEvent", map[string]interface{}{"text": thoughtChunk}))
					w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": textChunk}))
				}
			}))
			defer server.Close()
			defer swapKiroEndpointsForTest(t, server)()
			body := `{"model":"claude-sonnet-4.5-thinking","max_tokens":20000,"messages":[{"role":"user","content":"hello"}],"thinking":{"type":"enabled","budget_tokens":10000}}`
			if protocol == "responses" {
				body = `{"model":"claude-sonnet-4.5-thinking","input":"hello","store":false}`
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/", strings.NewReader(body))
			var content, reasoning string
			var outputTokens int
			switch protocol {
			case "openai":
				h.handleOpenAIChat(w, r)
				var response struct {
					Choices []struct {
						Message struct {
							Content   string `json:"content"`
							Reasoning string `json:"reasoning_content"`
						} `json:"message"`
					} `json:"choices"`
					Usage struct {
						OutputTokens int `json:"completion_tokens"`
					} `json:"usage"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if len(response.Choices) == 1 {
					content, reasoning = response.Choices[0].Message.Content, response.Choices[0].Message.Reasoning
				}
				outputTokens = response.Usage.OutputTokens
			case "claude":
				h.handleClaudeMessages(w, r)
				var response ClaudeResponse
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				for _, block := range response.Content {
					if block.Type == "text" {
						content += block.Text
					}
					if block.Type == "thinking" {
						reasoning += block.Thinking
					}
				}
				outputTokens = response.Usage.OutputTokens
			case "responses":
				h.handleOpenAIResponses(w, r)
				var response ResponsesObject
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				for _, item := range response.Output {
					for _, block := range item.Content {
						content += block.Text
					}
				}
				// Responses previously counted reasoning without including it in output.
				outputTokens = response.Usage.OutputTokens
			}
			if w.Code != 200 || content != wantText || (protocol != "responses" && reasoning != wantThought) {
				t.Fatalf("%s output mismatch: status=%d text=%d/%d reasoning=%d/%d", protocol, w.Code, len(content), len(wantText), len(reasoning), len(wantThought))
			}
			if want := estimateOpenAIOutputTokens(wantText, wantThought, nil); outputTokens != want {
				t.Fatalf("usage changed: got %d want %d", outputTokens, want)
			}
		})
	}
}

var longReplyBenchmarkResult string

func BenchmarkLongReplyAssembly(b *testing.B) {
	chunk := strings.Repeat("长回复。", 16)
	const pieces = 1024
	for _, mode := range []string{"concatenation", "builder"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(chunk) * pieces))
			for n := 0; n < b.N; n++ {
				if mode == "builder" {
					var out strings.Builder
					for i := 0; i < pieces; i++ {
						out.WriteString(chunk)
					}
					longReplyBenchmarkResult = out.String()
				} else {
					var out string
					for i := 0; i < pieces; i++ {
						out += chunk
					}
					longReplyBenchmarkResult = out
				}
			}
		})
	}
}

func TestThinkingSourceReasoningFirst(t *testing.T) {
	source := thinkingSourceReasoningEvent
	if allowTagSource(&source) {
		t.Fatalf("expected tag source to be rejected after reasoning source selected")
	}
}

func TestClaudeNonStreamRetriesNextAccountAfterPreResponseFailure(t *testing.T) {
	cfgFile := t.TempDir() + "/config.json"
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}

	if err := config.AddAccount(config.Account{
		ID:          "first",
		Enabled:     true,
		AccessToken: "token-first",
		ProfileArn:  "arn:aws:codewhisperer:profile/first",
	}); err != nil {
		t.Fatalf("add first account: %v", err)
	}
	if err := config.AddAccount(config.Account{
		ID:          "second",
		Enabled:     true,
		AccessToken: "token-second",
		ProfileArn:  "arn:aws:codewhisperer:profile/second",
	}); err != nil {
		t.Fatalf("add second account: %v", err)
	}
	if err := config.UpdatePreferredEndpoint("kiro"); err != nil {
		t.Fatalf("set preferred endpoint: %v", err)
	}
	if err := config.UpdateEndpointFallback(false); err != nil {
		t.Fatalf("disable endpoint fallback: %v", err)
	}

	requestTokens := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		requestTokens = append(requestTokens, token)
		// Fail the first attempted account (whichever it is) so the handler
		// is forced to add it to `excluded` and retry the other one.
		if len(requestTokens) == 1 {
			http.Error(w, "temporary upstream failure", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{
			"content": "retried successfully",
		}))
	}))
	defer server.Close()

	oldEndpoints := kiroEndpoints
	kiroEndpoints = []kiroEndpoint{{
		URL:    server.URL,
		Origin: "AI_EDITOR",
		Name:   "test",
	}}
	defer func() { kiroEndpoints = oldEndpoints }()

	oldClient := kiroHttpStore.Load()
	kiroHttpStore.Store(&http.Client{Timeout: time.Second, Transport: &http.Transport{}})
	defer kiroHttpStore.Store(oldClient)

	p := accountpool.GetPool()
	p.Reload()
	h := &Handler{
		pool:        p,
		promptCache: newPromptCacheTracker(),
	}

	payload := &KiroPayload{}
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{
		Content: "hello",
		ModelID: "claude-sonnet-4.5",
		Origin:  "AI_EDITOR",
	}

	rec := httptest.NewRecorder()
	h.handleClaudeNonStream(rec, payload, "claude-sonnet-4.5", claudeThinkingResponseOptions{}, 1, nil, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected retry to succeed, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(requestTokens) != 2 {
		t.Fatalf("expected two account attempts, got %v", requestTokens)
	}
	if requestTokens[0] == requestTokens[1] {
		t.Fatalf("expected first account to be excluded before retry, got %v", requestTokens)
	}
	tokenSet := map[string]bool{requestTokens[0]: true, requestTokens[1]: true}
	if !tokenSet["token-first"] || !tokenSet["token-second"] {
		t.Fatalf("expected both accounts to be tried, got %v", requestTokens)
	}

	var resp ClaudeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Content) == 0 || resp.Content[0].Text != "retried successfully" {
		t.Fatalf("expected retried response content, got %#v", resp.Content)
	}
}

func TestThinkingSourceTagFirst(t *testing.T) {
	var source thinkingStreamSource

	if !allowTagSource(&source) {
		t.Fatalf("expected tag source to be accepted first")
	}
	if source != thinkingSourceTagBlock {
		t.Fatalf("expected source to be tag, got %v", source)
	}
}

func TestThinkingSourceSameSourceRemainsAllowed(t *testing.T) {
	var source thinkingStreamSource

	if !allowTagSource(&source) {
		t.Fatalf("expected initial tag source selection to succeed")
	}
	if !allowTagSource(&source) {
		t.Fatalf("expected repeated tag source selection to stay allowed")
	}

}

func TestValidateOpenAIRequestShapeRejectsAssistantPrefill(t *testing.T) {
	req := &OpenAIRequest{
		Messages: []OpenAIMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "prefill"},
		},
	}

	if msg := validateOpenAIRequestShape(req); msg == "" {
		t.Fatalf("expected assistant-prefill final message to be rejected")
	}
}

func TestValidateOpenAIRequestShapeAllowsToolResultFinalTurn(t *testing.T) {
	req := &OpenAIRequest{
		Messages: []OpenAIMessage{
			{Role: "user", Content: "find weather"},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "get_weather", Arguments: "{}"},
				}},
			},
			{Role: "tool", ToolCallID: "call_1", Content: "sunny"},
		},
	}

	if msg := validateOpenAIRequestShape(req); msg != "" {
		t.Fatalf("expected tool-result final turn to be valid, got %q", msg)
	}
}

func TestValidateClaudeRequestShapeRejectsAssistantPrefill(t *testing.T) {
	req := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "prefill"},
		},
	}

	if msg := validateClaudeRequestShape(req); msg == "" {
		t.Fatalf("expected assistant-prefill final message to be rejected")
	}
}

func TestThinkingModeUsesModelSuffix(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		thinking     *ClaudeThinkingConfig
		wantModel    string
		wantThinking bool
	}{
		{
			name:         "adaptive request cannot enable a plain model",
			model:        "claude-sonnet-4.6",
			thinking:     &ClaudeThinkingConfig{Type: "adaptive"},
			wantModel:    "claude-sonnet-4.6",
			wantThinking: false,
		},
		{
			name:         "enabled request cannot enable a plain model",
			model:        "claude-opus-4.5",
			thinking:     &ClaudeThinkingConfig{Type: "enabled", BudgetTokens: 2048},
			wantModel:    "claude-opus-4.5",
			wantThinking: false,
		},
		{
			name:         "disabled request keeps thinking off",
			model:        "claude-opus-4.7",
			thinking:     &ClaudeThinkingConfig{Type: "disabled"},
			wantModel:    "claude-opus-4.7",
			wantThinking: false,
		},
		{
			name:         "suffix remains supported when thinking is disabled",
			model:        "claude-sonnet-4.5-thinking",
			thinking:     &ClaudeThinkingConfig{Type: "disabled"},
			wantModel:    "claude-sonnet-4.5",
			wantThinking: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotModel, gotThinking := ParseModelAndThinking(tc.model, "-thinking")
			if gotModel != tc.wantModel {
				t.Fatalf("expected model %q, got %q", tc.wantModel, gotModel)
			}
			if gotThinking != tc.wantThinking {
				t.Fatalf("expected thinking=%v, got %v", tc.wantThinking, gotThinking)
			}
		})
	}
}

func TestNativeThinkingPreservesPrompt(t *testing.T) {
	req := &ClaudeRequest{
		Model:    "claude-sonnet-4.6",
		System:   "Follow the user instructions.",
		Messages: []ClaudeMessage{{Role: "user", Content: "hello"}},
	}
	payload := ClaudeToKiro(req, true)
	if got := payload.ConversationState.History[0].UserInputMessage.Content; got != req.System {
		t.Fatalf("native thinking changed the prompt: %q", got)
	}
	if original, ok := req.System.(string); !ok || original != "Follow the user instructions." {
		t.Fatalf("expected original request system prompt to stay unchanged, got %#v", req.System)
	}
}

func TestNativeThinkingPreservesStructuredSystemBlocks(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-sonnet-4.6",
		System: []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": "cached system",
				"cache_control": map[string]interface{}{
					"type": "ephemeral",
					"ttl":  "5m",
				},
			},
		},
	}

	ClaudeToKiro(req, true)
	blocks, ok := req.System.([]interface{})
	if !ok {
		t.Fatalf("expected structured system blocks, got %T", req.System)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected original system block only, got %d", len(blocks))
	}
	second, ok := blocks[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected original system block to remain a map, got %T", blocks[0])
	}
	cacheControl, ok := second["cache_control"].(map[string]interface{})
	if !ok || cacheControl["type"] != "ephemeral" {
		t.Fatalf("expected original cache_control to be preserved, got %#v", second["cache_control"])
	}
}

func TestNativeThinkingFields(t *testing.T) {
	if fields := nativeThinkingFields("qwen3-coder-next", false); fields != nil {
		t.Fatalf("Claude-only parameters leaked into another model family: %#v", fields)
	}
	for _, enabled := range []bool{false, true} {
		fields := nativeThinkingFields("claude-opus-4.6", enabled)
		thinking := fields["thinking"].(ClaudeThinkingConfig)
		if enabled {
			if thinking.Type != "adaptive" || thinking.Display != "summarized" || fields["output_config"].(map[string]string)["effort"] != "high" {
				t.Fatalf("invalid native thinking fields: %#v", fields)
			}
		} else if thinking.Type != "disabled" || thinking.Display != "" || fields["output_config"] != nil {
			t.Fatalf("plain models must explicitly disable thinking: %#v", fields)
		}
	}
}

func TestValidateClaudeThinkingConfig(t *testing.T) {
	tests := []struct {
		name        string
		thinking    *ClaudeThinkingConfig
		maxTokens   int
		expectError bool
	}{
		{
			name:        "adaptive is valid",
			thinking:    &ClaudeThinkingConfig{Type: "adaptive"},
			maxTokens:   4096,
			expectError: false,
		},
		{
			name:        "enabled requires budget",
			thinking:    &ClaudeThinkingConfig{Type: "enabled"},
			maxTokens:   4096,
			expectError: true,
		},
		{
			name:        "enabled requires at least 1024 budget tokens",
			thinking:    &ClaudeThinkingConfig{Type: "enabled", BudgetTokens: 512},
			maxTokens:   4096,
			expectError: true,
		},
		{
			name:        "enabled rejects max tokens zero",
			thinking:    &ClaudeThinkingConfig{Type: "enabled", BudgetTokens: 2048},
			maxTokens:   0,
			expectError: true,
		},
		{
			name:        "enabled budget must stay below max tokens",
			thinking:    &ClaudeThinkingConfig{Type: "enabled", BudgetTokens: 4096},
			maxTokens:   4096,
			expectError: true,
		},
		{
			name:        "disabled rejects display",
			thinking:    &ClaudeThinkingConfig{Type: "disabled", Display: "summarized"},
			maxTokens:   4096,
			expectError: true,
		},
		{
			name:        "missing type is rejected",
			thinking:    &ClaudeThinkingConfig{},
			maxTokens:   4096,
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errMsg := validateClaudeThinkingConfig(tc.thinking, tc.maxTokens)
			if tc.expectError && errMsg == "" {
				t.Fatalf("expected validation error")
			}
			if !tc.expectError && errMsg != "" {
				t.Fatalf("expected thinking config to be valid, got %q", errMsg)
			}
		})
	}
}

func TestResolveClaudeThinkingResponseOptions(t *testing.T) {
	tests := []struct {
		name       string
		thinking   *ClaudeThinkingConfig
		defaultFmt string
		wantFmt    string
	}{
		{
			name:       "default config is preserved when display unset",
			thinking:   &ClaudeThinkingConfig{Type: "enabled", BudgetTokens: 2048},
			defaultFmt: "think",
			wantFmt:    "think",
		},
		{
			name:       "summarized forces official thinking blocks",
			thinking:   &ClaudeThinkingConfig{Type: "adaptive", Display: "summarized"},
			defaultFmt: "reasoning_content",
			wantFmt:    "thinking",
		},
		{
			name:       "omitted uses thinking blocks without hiding content",
			thinking:   &ClaudeThinkingConfig{Type: "adaptive", Display: "omitted"},
			defaultFmt: "think",
			wantFmt:    "thinking",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := resolveClaudeThinkingResponseOptions(tc.thinking, tc.defaultFmt)
			if opts.Format != tc.wantFmt {
				t.Fatalf("expected format %q, got %q", tc.wantFmt, opts.Format)
			}
		})
	}
}

func TestMergeUniqueModelsPreservesUnionAcrossAccounts(t *testing.T) {
	base := []ModelInfo{
		{ModelId: "claude-sonnet-4.5", InputTypes: []string{"TEXT"}},
	}
	incoming := []ModelInfo{
		{ModelId: "claude-sonnet-4.5", InputTypes: []string{"image"}},
		{ModelId: "claude-opus-4-7", InputTypes: []string{"text"}},
	}

	merged := mergeUniqueModels(base, incoming)
	if len(merged) != 2 {
		t.Fatalf("expected 2 unique models, got %d", len(merged))
	}
	if !modelSupportsImage(merged[0].InputTypes) {
		t.Fatalf("expected merged input types to preserve image capability, got %#v", merged[0].InputTypes)
	}
	if merged[1].ModelId != "claude-opus-4-7" {
		t.Fatalf("expected second model to be claude-opus-4-7, got %q", merged[1].ModelId)
	}
}

func TestBuildAnthropicModelsResponseGeneratesThinkingVariants(t *testing.T) {
	models := buildAnthropicModelsResponse([]ModelInfo{{
		ModelId:    "claude-sonnet-4.5",
		InputTypes: []string{"text", "image"},
	}}, "-thinking")

	if len(models) != 2 {
		t.Fatalf("expected base model and thinking variant, got %d", len(models))
	}
	if models[0]["id"] != "claude-sonnet-4.5" {
		t.Fatalf("unexpected base model id: %#v", models[0]["id"])
	}
	if models[1]["id"] != "claude-sonnet-4.5-thinking" {
		t.Fatalf("unexpected thinking model id: %#v", models[1]["id"])
	}
	if supportsImage, ok := models[0]["supports_image"].(bool); !ok || !supportsImage {
		t.Fatalf("expected image capability to be preserved, got %#v", models[0]["supports_image"])
	}
}
