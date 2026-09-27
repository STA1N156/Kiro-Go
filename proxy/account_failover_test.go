package proxy

import (
	"bytes"
	"encoding/json"
	"kiro-go/config"
	accountpool "kiro-go/pool"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestModelCooldownSettingsAPI(t *testing.T) {
	if err := config.Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatal(err)
	}
	config.SetPassword("cooldown-settings-test")
	h := &Handler{}
	request := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/admin/api/settings", strings.NewReader(body))
		r.Header.Set("X-Admin-Password", "cooldown-settings-test")
		w := httptest.NewRecorder()
		h.handleAdminAPI(w, r)
		return w
	}
	if w := request("GET", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"modelCooldownMinutes":3`) {
		t.Fatalf("wrong default: %d %s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{"modelCooldownMinutes":1}`, `{"modelCooldownMinutes":10080}`, `{"modelCooldownMinutes":8}`, `{}`} {
		if w := request("POST", body); w.Code != 200 {
			t.Fatalf("save failed: %d %s", w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{"modelCooldownMinutes":0}`, `{"modelCooldownMinutes":-1}`, `{"modelCooldownMinutes":10081}`, `{"modelCooldownMinutes":1.5}`, `{"modelCooldownMinutes":"3"}`} {
		if w := request("POST", body); w.Code != 400 || config.GetModelCooldownMinutes() != 8 {
			t.Fatalf("invalid duration accepted: %s (%d)", body, w.Code)
		}
	}
	if err := config.Load(); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"modelCooldownMinutes":8`) {
		t.Fatalf("saved duration lost: %d %s", w.Code, w.Body.String())
	}
}

func TestResetCooldownsAdminAPI(t *testing.T) {
	if err := config.Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatal(err)
	}
	config.SetPassword("local-admin-test")
	if err := config.AddAccount(config.Account{ID: "reset-test", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	p := accountpool.GetPool()
	p.Reload()
	p.RecordError("reset-test", true)
	p.CooldownModel("reset-test", "claude-opus-5.5", time.Hour)
	h := &Handler{pool: p, totalRequests: 7}
	for _, tc := range []struct {
		method, password string
		status           int
	}{
		{"POST", "", http.StatusUnauthorized},
		{"GET", "local-admin-test", http.StatusNotFound},
		{"POST", "local-admin-test", http.StatusOK},
	} {
		r := httptest.NewRequest(tc.method, "/admin/api/cooldowns/reset", nil)
		r.Header.Set("X-Admin-Password", tc.password)
		w := httptest.NewRecorder()
		h.handleAdminAPI(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: status %d, want %d", tc.method, w.Code, tc.status)
		}
		if tc.status != http.StatusOK && len(config.GetModelCooldowns()) == 0 {
			t.Fatal("rejected request cleared cooldowns")
		}
		if tc.status == http.StatusOK && !strings.Contains(w.Body.String(), `"success":true`) {
			t.Fatal("missing reset confirmation")
		}
	}
	if len(config.GetModelCooldowns()) != 0 || p.AvailableCount() != 1 {
		t.Fatal("reset did not clear account and model cooldowns")
	}
	if h.totalRequests != 7 {
		t.Fatal("cooldown reset changed statistics")
	}
}

func TestEmptyStreamModelCooldownAcrossAPIs(t *testing.T) {
	for _, protocol := range []string{"claude", "openai", "responses", "admin"} {
		for _, stream := range []bool{false, true} {
			t.Run(protocol+map[bool]string{false: "/json", true: "/stream"}[stream], func(t *testing.T) {
				if err := config.Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
					t.Fatal(err)
				}
				// Exercise both the default and a saved custom duration across all APIs.
				cooldownMinutes := 3
				if stream {
					cooldownMinutes = 8
					if err := config.UpdateSettingsPatch(nil, nil, "", &cooldownMinutes); err != nil {
						t.Fatal(err)
					}
				}
				for _, id := range []string{"empty-a", "empty-b"} {
					if err := config.AddAccount(config.Account{ID: id, Enabled: true, AccessToken: id, ProxyURL: kiroRetryTestProxyURL, ProfileArn: "arn:aws:codewhisperer:us-east-1:123456789012:profile/test"}); err != nil {
						t.Fatal(err)
					}
				}
				p := accountpool.GetPool()
				p.Reload()
				h := &Handler{pool: p, promptCache: newPromptCacheTracker()}
				installKiroRetryTestEndpoints(t)
				calls := map[string]int{}
				first := ""
				installKiroStreamTestClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
					id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
					var payload KiroPayload
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					model := payload.ConversationState.CurrentMessage.UserInputMessage.ModelID
					calls[id+model]++
					if first == "" {
						first = id
					}
					if id == first && model == "claude-opus-5.5" {
						return kiroStreamTestResponse(bytes.NewReader(nil)), nil
					}
					return kiroStreamTestResponse(bytes.NewReader(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "healthy"}))), nil
				}))
				installKiroRetryWait(t, func(time.Duration) { t.Error("empty stream must not wait/retry on the same account") })
				invoke := func(model string) *httptest.ResponseRecorder {
					body, _ := json.Marshal(map[string]interface{}{"model": model, "max_tokens": 2048, "stream": stream, "messages": []map[string]string{{"role": "user", "content": "hello"}}, "input": "hello", "store": false})
					r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
					w := httptest.NewRecorder()
					switch protocol {
					case "claude":
						h.handleClaudeMessages(w, r)
					case "openai":
						h.handleOpenAIChat(w, r)
					case "responses":
						h.handleOpenAIResponses(w, r)
					case "admin":
						h.apiTestAccount(w, r, "empty-a")
					}
					return w
				}
				w := invoke("claude-opus-5.5")
				if protocol != "admin" && (w.Code != 200 || !strings.Contains(w.Body.String(), "healthy")) {
					t.Fatalf("did not fail over: %d %s", w.Code, w.Body.String())
				}
				if protocol == "admin" && w.Code != 500 {
					t.Fatalf("admin empty response: %d", w.Code)
				}
				if calls[first+"claude-opus-5.5"] != 1 {
					t.Fatal("empty account was retried across endpoints")
				}
				wantDuration := time.Duration(cooldownMinutes) * time.Minute
				if remaining := time.Until(p.ModelCooldownUntil(first, "claude-opus-5.5")); remaining < wantDuration-time.Second || remaining > wantDuration {
					t.Fatalf("expected %d-minute cooldown: %s", cooldownMinutes, remaining)
				}
				// The account cards must expose cooldowns from tests as well as public requests.
				config.SetModelCooldown(first, "expired-model", time.Now().Add(-time.Second))
				cards := httptest.NewRecorder()
				h.apiGetAccounts(cards, httptest.NewRequest("GET", "/accounts", nil))
				var rows []struct {
					ID             string                 `json:"id"`
					ModelCooldowns []config.ModelCooldown `json:"modelCooldowns"`
				}
				if err := json.Unmarshal(cards.Body.Bytes(), &rows); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, row := range rows {
					if row.ID == first {
						found = true
						if len(row.ModelCooldowns) != 1 || row.ModelCooldowns[0].Model != "claude-opus-5.5" || row.ModelCooldowns[0].Until != p.ModelCooldownUntil(first, "claude-opus-5.5").Unix() {
							t.Fatalf("wrong card cooldowns: %+v", row.ModelCooldowns)
						}
					} else if len(row.ModelCooldowns) != 0 {
						t.Fatal("cooldown appeared on a different account")
					}
				}
				if !found {
					t.Fatal("cooling account missing from cards")
				}
				invoke("claude-opus-5.5-thinking")
				if calls[first+"claude-opus-5.5"] != 1 {
					t.Fatal("thinking suffix or admin call bypassed cooldown")
				}
				for i := 0; i < 3; i++ {
					h.handleAccountFailure(&config.Account{ID: first}, errEmptyKiroStream)
				}
				if p.AvailableCount() != 2 {
					t.Fatal("empty stream globally cooled an account")
				}
				other := "empty-a"
				if first == other {
					other = "empty-b"
				}
				if a := p.GetNextForModelExcluding("claude-opus-4.6", map[string]bool{other: true}); a == nil || a.ID != first {
					t.Fatal("another model was blocked")
				}
				w = invoke("claude-opus-4.6")
				if w.Code != 200 || !strings.Contains(w.Body.String(), "healthy") {
					t.Fatalf("other model failed: %d %s", w.Code, w.Body.String())
				}
				if !time.Now().Before(p.ModelCooldownUntil(first, "claude-opus-5.5")) {
					t.Fatal("another model's success cleared cooldown")
				}
			})
		}
	}
}

func TestAccountRetryLimit(t *testing.T) {
	for _, protocol := range []string{"claude", "openai", "responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(protocol+map[bool]string{false: "/json", true: "/stream"}[stream], func(t *testing.T) {
				if err := config.Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
					if err := config.AddAccount(config.Account{ID: id, Enabled: true, AuthMethod: "api_key", KiroApiKey: id, ProxyURL: kiroRetryTestProxyURL}); err != nil {
						t.Fatal(err)
					}
				}
				p := accountpool.GetPool()
				p.Reload()
				h := &Handler{pool: p, promptCache: newPromptCacheTracker()}
				calls := make(map[string]int)
				installKiroStreamTestClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls[r.Header.Get("Authorization")]++
					response := kiroStreamTestResponse(strings.NewReader("upstream unavailable"))
					response.StatusCode = http.StatusInternalServerError
					return response, nil
				}))
				body, _ := json.Marshal(map[string]interface{}{"model": "claude-opus-5.5", "max_tokens": 2048, "stream": stream, "messages": []map[string]string{{"role": "user", "content": "hello"}}, "input": "hello", "store": false})
				r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
				w := httptest.NewRecorder()
				switch protocol {
				case "claude":
					h.handleClaudeMessages(w, r)
				case "openai":
					h.handleOpenAIChat(w, r)
				case "responses":
					h.handleOpenAIResponses(w, r)
				}
				if len(calls) != 5 {
					t.Fatalf("expected 5 distinct credentials including the first, got %v", calls)
				}
				for id, count := range calls {
					if count != 1 {
						t.Fatalf("credential %s attempted %d times", id, count)
					}
				}
			})
		}
	}
}

func TestAccountFailureClassifiers(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) bool
		msg  string
	}{
		{name: "quota", fn: isQuotaErrorMessage, msg: "HTTP 429: quota exhausted"},
		{name: "overage", fn: isOverageErrorMessage, msg: "HTTP 402 from Kiro IDE: OVERAGE limit exceeded"},
		{name: "suspension", fn: isSuspensionErrorMessage, msg: "Your User ID temporarily is suspended"},
		{name: "profile", fn: isProfileUnavailableErrorMessage, msg: "no available Kiro profile"},
		{name: "auth", fn: isAuthErrorMessage, msg: "Authentication failed - token invalid or expired"},
	}

	for _, tc := range tests {
		if !tc.fn(tc.msg) {
			t.Fatalf("%s classifier did not match %q", tc.name, tc.msg)
		}
	}
}
