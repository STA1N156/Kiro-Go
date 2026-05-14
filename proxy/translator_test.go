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
	if !strings.Contains(cur.Content, "tool-result-structured") {
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
}

func TestExtractThinkingIgnoresQuotedEndTag(t *testing.T) {
	// thinking 内容里引用了字面 </thinking>，不应在此处提前截断
	content := "<thinking>I will explain `</thinking>` tag now.\n\nFinal answer.\n\n</thinking>\n\nResult here."
	text, reasoning := extractThinkingFromContent(content)
	if text != "Result here." {
		t.Fatalf("text = %q, want %q", text, "Result here.")
	}
	if !strings.Contains(reasoning, "I will explain") {
		t.Fatalf("reasoning truncated: %q", reasoning)
	}
	if !strings.Contains(reasoning, "Final answer.") {
		t.Fatalf("expected reasoning to include trailing portion, got %q", reasoning)
	}
}

func TestExtractThinkingHandlesEndOfBuffer(t *testing.T) {
	// </thinking> 紧贴字符串末尾（仅剩空白），也应识别
	content := "<thinking>only thinking content</thinking>   "
	text, reasoning := extractThinkingFromContent(content)
	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
	if reasoning != "only thinking content" {
		t.Fatalf("reasoning = %q", reasoning)
	}
}

func TestExtractThinkingMultipleBlocks(t *testing.T) {
	content := "<thinking>part 1</thinking>\n\nmiddle\n\n<thinking>part 2</thinking>\n\nend"
	text, reasoning := extractThinkingFromContent(content)
	if !strings.Contains(reasoning, "part 1") || !strings.Contains(reasoning, "part 2") {
		t.Fatalf("missing parts: %q", reasoning)
	}
	if !strings.Contains(text, "middle") || !strings.Contains(text, "end") {
		t.Fatalf("text fragments lost: %q", text)
	}
}

func TestMapModelHandlesSonnet37(t *testing.T) {
	cases := map[string]string{
		"claude-3-7-sonnet-20250219":          "claude-3-7-sonnet-20250219",
		"claude-3-7-sonnet":                   "claude-3-7-sonnet-20250219",
		"claude-3-7-sonnet-20250219-thinking": "claude-3-7-sonnet-20250219",
	}
	for input, want := range cases {
		if got := MapModel(input); got != want {
			t.Fatalf("MapModel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseBracketToolCallsBasic(t *testing.T) {
	content := "Sure, I'll do that.\n[Called search with args: {\"query\": \"weather\", \"limit\": 5}]\nDone."
	cleaned, calls := parseBracketToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Name != "search" {
		t.Fatalf("name = %q", calls[0].Name)
	}
	if calls[0].Input["query"] != "weather" {
		t.Fatalf("query missing: %#v", calls[0].Input)
	}
	if strings.Contains(cleaned, "[Called") {
		t.Fatalf("bracket text not stripped: %q", cleaned)
	}
}

func TestParseBracketToolCallsRepairsLooseJSON(t *testing.T) {
	// 尾逗号 + 未引号键
	content := `[Called fetch with args: {url: "https://x", method: "GET",}]`
	_, calls := parseBracketToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Input["url"] != "https://x" {
		t.Fatalf("url = %#v", calls[0].Input["url"])
	}
}

func TestParseBracketToolCallsMultiple(t *testing.T) {
	content := `Step 1: [Called a with args: {"x":1}] Step 2: [Called b with args: {"y":2}] end`
	cleaned, calls := parseBracketToolCalls(content)
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	names := []string{calls[0].Name, calls[1].Name}
	if !(names[0] == "a" && names[1] == "b") {
		t.Fatalf("order wrong: %v", names)
	}
	if strings.Contains(cleaned, "[Called") {
		t.Fatalf("brackets not stripped: %q", cleaned)
	}
}

func TestParseBracketToolCallsNoMatchPassThrough(t *testing.T) {
	content := "Plain text response, no tool calls."
	cleaned, calls := parseBracketToolCalls(content)
	if len(calls) != 0 {
		t.Fatalf("unexpected calls: %v", calls)
	}
	if cleaned != content {
		t.Fatalf("text changed: %q", cleaned)
	}
}

func TestRepairLooseJSONLeavesValidJSONAlone(t *testing.T) {
	in := `{"a":1,"b":[1,2,3]}`
	if got := repairLooseJSON(in); got != in {
		t.Fatalf("modified valid json: %q", got)
	}
}

func TestOpenAIConversationIDRandomForSyntheticAnchor(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "assistant", Content: "prefill"},
		},
	}

	payloadA := OpenAIToKiro(req, "")
	payloadB := OpenAIToKiro(req, "")

	if payloadA.ConversationState.ConversationID == payloadB.ConversationState.ConversationID {
		t.Fatalf("expected synthetic anchor to generate non-deterministic conversation IDs")
	}
}

func TestClaudeToKiroDropsLeadingAssistantHistory(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-sonnet-4.5",
		Messages: []ClaudeMessage{
			{Role: "assistant", Content: "prefill"},
			{Role: "user", Content: "real user message"},
		},
	}

	payload := ClaudeToKiro(req, "")

	if len(payload.ConversationState.History) != 0 {
		t.Fatalf("expected leading assistant-only history to be dropped, got %d entries", len(payload.ConversationState.History))
	}

	if strings.Contains(payload.ConversationState.CurrentMessage.UserInputMessage.Content, "Begin conversation") {
		t.Fatalf("unexpected synthetic Begin conversation injection in current content: %q", payload.ConversationState.CurrentMessage.UserInputMessage.Content)
	}
}

func TestKiroToClaudeResponseCanEmitEmptyThinkingBlock(t *testing.T) {
	resp := KiroToClaudeResponse("final answer", "", true, nil, 10, 20, "claude-sonnet-4.6")

	if len(resp.Content) != 2 {
		t.Fatalf("expected empty thinking block plus text block, got %d blocks", len(resp.Content))
	}
	if resp.Content[0].Type != "thinking" {
		t.Fatalf("expected first block to be thinking, got %#v", resp.Content[0])
	}
	if resp.Content[0].Thinking != "" {
		t.Fatalf("expected omitted thinking block to have empty content, got %#v", resp.Content[0].Thinking)
	}
	if resp.Content[1].Type != "text" || resp.Content[1].Text != "final answer" {
		t.Fatalf("expected text block to be preserved, got %#v", resp.Content[1])
	}
}

func TestToolResultsContinuationIncludesInstructionPrefix(t *testing.T) {
	req := &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "find data"},
			{Role: "assistant", ToolCalls: []ToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "fetch", Arguments: "{}"},
			}}},
			{Role: "tool", ToolCallID: "call_1", Content: "result-1"},
		},
	}

	payload := OpenAIToKiro(req, "")
	content := payload.ConversationState.CurrentMessage.UserInputMessage.Content

	if !strings.Contains(content, toolResultsContinuationPrefix) {
		t.Fatalf("expected tool continuation prefix, got %q", content)
	}
	if !strings.Contains(content, "result-1") {
		t.Fatalf("expected tool result text in continuation content, got %q", content)
	}
}
