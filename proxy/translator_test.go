package proxy

import (
	"strings"
	"testing"
)

func TestExtractOpenAIMessageTextStructured(t *testing.T) {
	content := []interface{}{
		map[string]interface{}{"type": "text", "text": "alpha"},
		map[string]interface{}{"type": "input_text", "text": "beta"},
	}

	if got := extractOpenAIMessageText(content); got != "alphabeta" {
		t.Fatalf("expected concatenated structured text, got %q", got)
	}

	nested := map[string]interface{}{
		"content": []interface{}{map[string]interface{}{"type": "text", "text": "nested"}},
	}
	if got := extractOpenAIMessageText(nested); got != "nested" {
		t.Fatalf("expected nested content extraction, got %q", got)
	}
}

func TestOpenAIToKiroPreservesStructuredAssistantAndToolContent(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{
				Role: "system",
				Content: []interface{}{
					map[string]interface{}{"type": "text", "text": "system-a"},
					map[string]interface{}{"type": "text", "text": "system-b"},
				},
			},
			{Role: "user", Content: "first-question"},
			{
				Role: "assistant",
				Content: []interface{}{
					map[string]interface{}{"type": "text", "text": "assistant-structured"},
				},
			},
			{
				Role:       "tool",
				ToolCallID: "call_1",
				Content: []interface{}{
					map[string]interface{}{"type": "text", "text": "tool-result-structured"},
				},
			},
		},
	}

	payload := OpenAIToKiro(req, "")

	if len(payload.ConversationState.History) != 2 {
		t.Fatalf("expected 2 history items, got %d", len(payload.ConversationState.History))
	}

	firstHistoryUser := payload.ConversationState.History[0].UserInputMessage
	if firstHistoryUser == nil {
		t.Fatalf("expected first history item to be user message")
	}
	if !strings.Contains(firstHistoryUser.Content, "system-a") ||
		!strings.Contains(firstHistoryUser.Content, "system-b") ||
		!strings.Contains(firstHistoryUser.Content, "first-question") {
		t.Fatalf("expected merged system+user content, got %q", firstHistoryUser.Content)
	}

	historyAssistant := payload.ConversationState.History[1].AssistantResponseMessage
	if historyAssistant == nil {
		t.Fatalf("expected second history item to be assistant message")
	}
	if historyAssistant.Content != "assistant-structured" {
		t.Fatalf("expected assistant structured content to be preserved, got %q", historyAssistant.Content)
	}

	cur := payload.ConversationState.CurrentMessage.UserInputMessage
	if cur.Content != "tool-result-structured" {
		t.Fatalf("expected tool-result continuation content, got %q", cur.Content)
	}
	if cur.UserInputMessageContext == nil || len(cur.UserInputMessageContext.ToolResults) != 1 {
		t.Fatalf("expected one tool result in current context")
	}
	gotToolText := cur.UserInputMessageContext.ToolResults[0].Content[0].Text
	if gotToolText != "tool-result-structured" {
		t.Fatalf("expected structured tool result text, got %q", gotToolText)
	}
}

func TestOpenAIToKiroAssistantMapContentInHistory(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "u1"},
			{Role: "assistant", Content: map[string]interface{}{"type": "text", "text": "assistant-map"}},
			{Role: "user", Content: "u2"},
		},
	}

	payload := OpenAIToKiro(req, "")

	if len(payload.ConversationState.History) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(payload.ConversationState.History))
	}
	assistant := payload.ConversationState.History[1].AssistantResponseMessage
	if assistant == nil {
		t.Fatalf("expected second history entry to be assistant")
	}
	if assistant.Content != "assistant-map" {
		t.Fatalf("expected assistant map content preserved, got %q", assistant.Content)
	}
}

func TestOpenAIToKiroAssistantToolCallsDoNotInjectPlaceholder(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "find weather"},
			{
				Role:    "assistant",
				Content: nil,
				ToolCalls: []ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: "get_weather", Arguments: "{}"},
				}},
			},
			{Role: "user", Content: "continue"},
		},
	}

	payload := OpenAIToKiro(req, "")
	if len(payload.ConversationState.History) < 2 {
		t.Fatalf("expected history with assistant tool call")
	}
	assistant := payload.ConversationState.History[1].AssistantResponseMessage
	if assistant == nil {
		t.Fatalf("expected assistant history entry")
	}
	if assistant.Content != "" {
		t.Fatalf("expected empty assistant content for tool-call-only turn, got %q", assistant.Content)
	}
}

func TestOpenAIConversationIDStableFromAnchor(t *testing.T) {
	baseMessages := []OpenAIMessage{
		{Role: "system", Content: "You are helpful"},
		{Role: "user", Content: "Build calculator"},
		{Role: "assistant", Content: "Sure"},
		{Role: "user", Content: "Continue"},
	}

	reqA := &OpenAIRequest{Model: "claude-sonnet-4.5", Messages: baseMessages}
	reqB := &OpenAIRequest{Model: "claude-sonnet-4.5", Messages: append(baseMessages, OpenAIMessage{Role: "assistant", Content: "Next step"})}

	payloadA := OpenAIToKiro(reqA, "")
	payloadB := OpenAIToKiro(reqB, "")

	if payloadA.ConversationState.ConversationID == "" || payloadB.ConversationState.ConversationID == "" {
		t.Fatalf("expected non-empty conversation IDs")
	}
	if payloadA.ConversationState.ConversationID != payloadB.ConversationState.ConversationID {
		t.Fatalf("expected stable conversation ID across turns, got %q vs %q", payloadA.ConversationState.ConversationID, payloadB.ConversationState.ConversationID)
	}
}

func TestClaudeConversationIDStableFromAnchor(t *testing.T) {
	reqA := &ClaudeRequest{
		Model:  "claude-sonnet-4.5",
		System: "sys",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}
	reqB := &ClaudeRequest{
		Model:  "claude-sonnet-4.5",
		System: "sys",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "next"},
		},
	}

	payloadA := ClaudeToKiro(reqA, "")
	payloadB := ClaudeToKiro(reqB, "")

	if payloadA.ConversationState.ConversationID == "" || payloadB.ConversationState.ConversationID == "" {
		t.Fatalf("expected non-empty conversation IDs")
	}
	if payloadA.ConversationState.ConversationID != payloadB.ConversationState.ConversationID {
		t.Fatalf("expected stable conversation ID across turns, got %q vs %q", payloadA.ConversationState.ConversationID, payloadB.ConversationState.ConversationID)
	}
}

func TestMapModelHandlesOpus47(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-7":          "claude-opus-4.7",
		"claude-opus-4.7":          "claude-opus-4.7",
		"claude-opus-4-7-thinking": "claude-opus-4.7",
	}
	for input, want := range cases {
		if got := MapModel(input); got != want {
			t.Fatalf("MapModel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGetContextWindowSize1MModels(t *testing.T) {
	oneMillion := []string{"claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-4-6"}
	for _, m := range oneMillion {
		if got := GetContextWindowSize(m); got != 1_000_000 {
			t.Fatalf("GetContextWindowSize(%q) = %d, want 1_000_000", m, got)
		}
	}
	if got := GetContextWindowSize("claude-opus-4-5"); got != 200_000 {
		t.Fatalf("GetContextWindowSize(opus-4.5) = %d, want 200_000", got)
	}
}

func TestResolveClaudeThinkingFromBodyEnabled(t *testing.T) {
	req := &ClaudeRequest{
		Model:    "claude-opus-4-5",
		Thinking: &ClaudeThinkingConfig{Type: "enabled", BudgetTokens: 8000},
	}
	mapped, prompt := ResolveClaudeThinking(req, "-thinking")
	if mapped != "claude-opus-4.5" {
		t.Fatalf("model = %q, want claude-opus-4.5", mapped)
	}
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(prompt, "<thinking_mode>enabled</thinking_mode>") {
		t.Fatalf("missing enabled tag: %q", prompt)
	}
	if !strings.Contains(prompt, "<max_thinking_length>8000</max_thinking_length>") {
		t.Fatalf("budget not applied: %q", prompt)
	}
}

func TestResolveClaudeThinkingFromBodyAdaptive(t *testing.T) {
	req := &ClaudeRequest{
		Model:    "claude-sonnet-4-6",
		Thinking: &ClaudeThinkingConfig{Type: "adaptive", Effort: "medium"},
	}
	_, prompt := ResolveClaudeThinking(req, "-thinking")
	if !strings.Contains(prompt, "<thinking_mode>adaptive</thinking_mode>") {
		t.Fatalf("missing adaptive tag: %q", prompt)
	}
	if !strings.Contains(prompt, "<thinking_effort>medium</thinking_effort>") {
		t.Fatalf("effort not applied: %q", prompt)
	}
}

func TestResolveClaudeThinkingDisabledOverridesSuffix(t *testing.T) {
	req := &ClaudeRequest{
		Model:    "claude-opus-4-5-thinking",
		Thinking: &ClaudeThinkingConfig{Type: "disabled"},
	}
	_, prompt := ResolveClaudeThinking(req, "-thinking")
	if prompt != "" {
		t.Fatalf("disabled should win over suffix; got %q", prompt)
	}
}

func TestResolveClaudeThinkingFallsBackToSuffix(t *testing.T) {
	req := &ClaudeRequest{Model: "claude-opus-4-5-thinking"}
	mapped, prompt := ResolveClaudeThinking(req, "-thinking")
	if mapped != "claude-opus-4.5" {
		t.Fatalf("suffix not stripped: %q", mapped)
	}
	if prompt == "" {
		t.Fatalf("suffix should enable thinking with default prompt")
	}
}

func TestResolveOpenAIThinkingFromReasoningEffort(t *testing.T) {
	req := &OpenAIRequest{Model: "claude-sonnet-4-6", ReasoningEffort: "high"}
	_, prompt := ResolveOpenAIThinking(req, "-thinking")
	if !strings.Contains(prompt, "<thinking_mode>adaptive</thinking_mode>") {
		t.Fatalf("expected adaptive: %q", prompt)
	}
	if !strings.Contains(prompt, "<thinking_effort>high</thinking_effort>") {
		t.Fatalf("expected effort=high: %q", prompt)
	}
}

func TestClaudeToKiroInjectsThinkingPrompt(t *testing.T) {
	req := &ClaudeRequest{
		Model:    "claude-opus-4-5",
		System:   "你是助手",
		Messages: []ClaudeMessage{{Role: "user", Content: "hello"}},
	}
	prompt := "<thinking_mode>enabled</thinking_mode><max_thinking_length>5000</max_thinking_length>"
	payload := ClaudeToKiro(req, prompt)
	content := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(content, "<thinking_mode>enabled</thinking_mode>") {
		t.Fatalf("missing thinking_mode tag in current message content: %q", content)
	}
	if !strings.Contains(content, "<max_thinking_length>5000</max_thinking_length>") {
		t.Fatalf("budget not in prompt: %q", content)
	}
}

func TestResolveAndConvertEndToEnd(t *testing.T) {
	req := &ClaudeRequest{
		Model:    "claude-opus-4-5",
		System:   "你是助手",
		Thinking: &ClaudeThinkingConfig{Type: "enabled", BudgetTokens: 12000},
		Messages: []ClaudeMessage{{Role: "user", Content: "证明哥德巴赫猜想"}},
	}
	mappedModel, prompt := ResolveClaudeThinking(req, "-thinking")
	req.Model = mappedModel
	if prompt == "" {
		t.Fatal("body.thinking failed to resolve a prompt")
	}
	payload := ClaudeToKiro(req, prompt)
	content := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	for _, must := range []string{
		"<thinking_mode>enabled</thinking_mode>",
		"<max_thinking_length>12000</max_thinking_length>",
		"你是助手",
		"证明哥德巴赫猜想",
	} {
		if !strings.Contains(content, must) {
			t.Fatalf("missing fragment %q in payload content:\n%s", must, content)
		}
	}
	if payload.ConversationState.CurrentMessage.UserInputMessage.ModelID != "claude-opus-4.5" {
		t.Fatalf("modelId mismatch: %q", payload.ConversationState.CurrentMessage.UserInputMessage.ModelID)
	}
}
