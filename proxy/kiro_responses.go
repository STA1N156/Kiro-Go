package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"kiro-go/config"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

type kiroResponsesRequest struct {
	Model      string                  `json:"model"`
	Input      []interface{}           `json:"input"`
	Tools      []kiroResponsesTool     `json:"tools,omitempty"`
	Stream     bool                    `json:"stream"`
	Origin     string                  `json:"origin"`
	ProfileArn string                  `json:"profileArn,omitempty"`
	Metadata   kiroResponsesMetadata   `json:"metadata"`
	Reasoning  *kiroResponsesReasoning `json:"reasoning,omitempty"`
}

type kiroResponsesTool struct {
	Type        string      `json:"type"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

type kiroResponsesMetadata struct {
	AgentMode           string `json:"kiro:agent_mode"`
	ModeIdentifier      string `json:"kiro:mode_identifier"`
	AgentTaskType       string `json:"kiro:agent_task_type,omitempty"`
	ConversationID      string `json:"kiro:conversation_id"`
	RootConversationID  string `json:"kiro:root_conversation_id"`
	AgentContinuationID string `json:"kiro:agent_continuation_id,omitempty"`
}

type kiroResponsesReasoning struct {
	Type    string `json:"type,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type kiroResponsesMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type kiroResponsesTextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type kiroResponsesImagePart struct {
	Type     string `json:"type"`
	ImageURL string `json:"image_url"`
}

type kiroResponsesToolOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type kiroResponsesReasoningItem struct {
	Type             string        `json:"type"`
	Summary          []interface{} `json:"summary"`
	EncryptedContent string        `json:"encrypted_content"`
}

type kiroResponsesFunctionCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func callKiroResponsesAPI(
	ctx context.Context,
	account *config.Account,
	payload *KiroPayload,
	callback *KiroStreamCallback,
	requestConfig kiroRequestConfig,
) error {
	if !payload.ResponsesAgentContinuationSet {
		payload.ConversationState.AgentContinuationId = uuid.NewString()
		payload.ResponsesAgentContinuationSet = true
	}
	body, err := buildKiroResponsesBody(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestConfig.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	applyKiroResponsesHeaders(req, account, payload.ProfileArn)
	resp, err := GetClientForProxy(ResolveAccountProxyURL(account)).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		message := strings.TrimSpace(string(raw))
		if isContextLengthError(message) {
			return errContextTooLong
		}
		return &kiroStreamError{
			code:    strconv.Itoa(resp.StatusCode),
			message: message,
			status:  resp.StatusCode,
		}
	}
	return parseKiroResponsesSSE(resp.Body, callback)
}

func buildKiroResponsesBody(payload *KiroPayload) ([]byte, error) {
	if payload == nil {
		return nil, errors.New("missing Kiro payload")
	}
	current := &payload.ConversationState.CurrentMessage.UserInputMessage
	input := make([]interface{}, 0, len(payload.ConversationState.History)*2+1)
	for i := range payload.ConversationState.History {
		entry := &payload.ConversationState.History[i]
		if entry.UserInputMessage != nil {
			input = append(input, kiroResponsesUserItems(entry.UserInputMessage)...)
		}
		if entry.AssistantResponseMessage != nil {
			input = append(input, kiroResponsesAssistantItems(entry.AssistantResponseMessage)...)
		}
	}
	input = append(input, kiroResponsesUserItems(current)...)

	tools := make([]kiroResponsesTool, 0)
	if current.UserInputMessageContext != nil {
		for _, tool := range current.UserInputMessageContext.Tools {
			spec := tool.ToolSpecification
			if strings.TrimSpace(spec.Name) == "" {
				continue
			}
			tools = append(tools, kiroResponsesTool{
				Type:        "function",
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  spec.InputSchema.JSON,
			})
		}
	}

	agentMode := payload.AgentMode
	if agentMode == "" {
		agentMode = "vibe"
	}
	conversationID := kiroResponsesSessionID(payload.ConversationState.ConversationID)
	request := kiroResponsesRequest{
		Model:      current.ModelID,
		Input:      input,
		Tools:      tools,
		Stream:     true,
		Origin:     "AI_EDITOR",
		ProfileArn: payload.ProfileArn,
		Metadata: kiroResponsesMetadata{
			AgentMode:           agentMode,
			ModeIdentifier:      agentMode,
			ConversationID:      conversationID,
			RootConversationID:  conversationID,
			AgentContinuationID: payload.ConversationState.AgentContinuationId,
		},
		Reasoning: kiroResponsesReasoningFromFields(payload.AdditionalModelRequestFields),
	}
	if payload.ConversationState.AgentTaskType == "spec" {
		request.Metadata.AgentTaskType = "spec"
	}
	return json.Marshal(request)
}

func kiroResponsesUserItems(message *KiroUserInputMessage) []interface{} {
	items := make([]interface{}, 0, 2)
	if message.UserInputMessageContext != nil {
		for _, result := range message.UserInputMessageContext.ToolResults {
			parts := make([]string, 0, len(result.Content))
			for _, part := range result.Content {
				parts = append(parts, part.Text)
			}
			items = append(items, kiroResponsesToolOutput{
				Type:   "function_call_output",
				CallID: result.ToolUseID,
				Output: strings.Join(parts, "\n"),
			})
		}
	}

	parts := make([]interface{}, 0, len(message.Images)+1)
	if message.Content != "" {
		parts = append(parts, kiroResponsesTextPart{Type: "input_text", Text: message.Content})
	}
	for _, image := range message.Images {
		if image.Source.Bytes == "" {
			continue
		}
		parts = append(parts, kiroResponsesImagePart{
			Type:     "input_image",
			ImageURL: "data:image/" + image.Format + ";base64," + image.Source.Bytes,
		})
	}
	if len(parts) == 0 {
		return items
	}
	if len(parts) == 1 {
		if first, ok := parts[0].(kiroResponsesTextPart); ok {
			items = append(items, kiroResponsesMessage{Role: "user", Content: first.Text})
			return items
		}
	}
	items = append(items, kiroResponsesMessage{Role: "user", Content: parts})
	return items
}

func kiroResponsesAssistantItems(message *KiroAssistantResponseMessage) []interface{} {
	items := make([]interface{}, 0, len(message.ToolUses)+2)
	if message.ReasoningContent != nil {
		envelope := make(map[string]interface{})
		switch {
		case message.ReasoningContent.ReasoningText != nil &&
			message.ReasoningContent.ReasoningText.Text != "" &&
			message.ReasoningContent.ReasoningText.Signature != "":
			envelope["reasoningText"] = message.ReasoningContent.ReasoningText
		case message.ReasoningContent.RedactedContent != "":
			envelope["redactedContent"] = message.ReasoningContent.RedactedContent
		}
		if len(envelope) > 0 {
			encoded, _ := json.Marshal(envelope)
			items = append(items, kiroResponsesReasoningItem{
				Type:             "reasoning",
				Summary:          []interface{}{},
				EncryptedContent: string(encoded),
			})
		}
	}
	if message.Content != "" {
		items = append(items, kiroResponsesMessage{Role: "assistant", Content: message.Content})
	}
	for _, tool := range message.ToolUses {
		arguments, _ := json.Marshal(tool.Input)
		items = append(items, kiroResponsesFunctionCall{
			Type:      "function_call",
			CallID:    tool.ToolUseID,
			Name:      tool.Name,
			Arguments: string(arguments),
		})
	}
	return items
}

func kiroResponsesReasoningFromFields(fields map[string]interface{}) *kiroResponsesReasoning {
	if len(fields) == 0 {
		return nil
	}
	reasoning := &kiroResponsesReasoning{}
	if thinking, ok := fields["thinking"].(map[string]interface{}); ok {
		reasoning.Type, _ = thinking["type"].(string)
		reasoning.Summary, _ = thinking["display"].(string)
	}
	for _, key := range []string{"output_config", "reasoning"} {
		if config, ok := fields[key].(map[string]interface{}); ok {
			if effort, ok := config["effort"].(string); ok && effort != "" {
				reasoning.Effort = effort
				break
			}
		}
	}
	if reasoning.Type == "" && reasoning.Effort == "" && reasoning.Summary == "" {
		return nil
	}
	return reasoning
}

func kiroResponsesSessionID(raw string) string {
	bare := strings.TrimPrefix(strings.TrimSpace(raw), "sess_")
	parsed, err := uuid.Parse(bare)
	if err != nil {
		return "sess_" + bare
	}
	bytes := parsed
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return "sess_" + bytes.String()
}

func parseKiroResponsesSSE(body io.Reader, callback *KiroStreamCallback) error {
	if callback == nil {
		callback = &KiroStreamCallback{}
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	tools := make(map[string]*toolUseState)
	toolOrder := make([]string, 0, 2)
	currentToolID := ""
	sawToolDelta := make(map[string]bool)
	var emitted, refusalDelta bool
	var inputTokens, outputTokens int
	var stopReason string

	finishTools := func() error {
		for _, id := range toolOrder {
			if state := tools[id]; state != nil {
				emitted = true
				if err := finishToolUse(state, callback); err != nil {
					return err
				}
			}
		}
		tools = make(map[string]*toolUseState)
		toolOrder = toolOrder[:0]
		currentToolID = ""
		sawToolDelta = make(map[string]bool)
		return nil
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		kind, _ := event["type"].(string)
		delta, _ := event["delta"].(string)
		switch kind {
		case "kiro:keep_alive":
		case "response.output_text.delta":
			if delta != "" && callback.OnText != nil {
				emitted = true
				callback.OnText(delta, false)
			}
		case "response.reasoning_text.delta", "response.reasoning.delta":
			if delta != "" && callback.OnText != nil {
				emitted = true
				callback.OnText(delta, true)
			}
		case "response.refusal.delta":
			if delta != "" && callback.OnText != nil {
				emitted, refusalDelta = true, true
				callback.OnText(delta, false)
			}
		case "response.refusal.done":
			if !refusalDelta {
				if text, _ := event["refusal"].(string); text != "" && callback.OnText != nil {
					emitted = true
					callback.OnText(text, false)
				}
			}
		case "response.output_item.added":
			item := mapValue(event["item"])
			if stringValue(item, "type") == "function_call" {
				id := stringValue(item, "call_id")
				if id == "" {
					id = stringValue(item, "id")
				}
				if id != "" {
					tools[id] = &toolUseState{ToolUseID: id, Name: stringValue(item, "name")}
					toolOrder = append(toolOrder, id)
					currentToolID = id
				}
			}
		case "response.function_call_arguments.delta":
			if state := tools[currentToolID]; state != nil && delta != "" {
				state.InputBuffer.WriteString(delta)
				sawToolDelta[currentToolID] = true
			}
		case "response.function_call_arguments.done":
			if state := tools[currentToolID]; state != nil && !sawToolDelta[currentToolID] {
				state.InputBuffer.WriteString(stringValue(event, "arguments"))
			}
		case "response.output_item.done":
			item := mapValue(event["item"])
			switch stringValue(item, "type") {
			case "function_call":
				id := stringValue(item, "call_id")
				if id == "" {
					id = stringValue(item, "id")
				}
				state := tools[id]
				if state != nil && !sawToolDelta[id] && state.InputBuffer.Len() == 0 {
					state.InputBuffer.WriteString(stringValue(item, "arguments"))
				}
			case "reasoning":
				parseKiroReasoningEnvelope(stringValue(item, "encrypted_content"), callback, &emitted)
			}
		case "response.completed", "response.incomplete":
			response := mapValue(event["response"])
			incomplete := kind == "response.incomplete" || stringValue(response, "status") == "incomplete"
			if incomplete {
				reason := stringValue(mapValue(response["incomplete_details"]), "reason")
				switch reason {
				case "max_output_tokens":
					stopReason = "MAX_TOKENS"
				case "content_filter":
					stopReason = "CONTENT_FILTERED"
				case "compaction_required_for_model_handoff":
					stopReason = "COMPACTION_REQUIRED_FOR_MODEL_HANDOFF"
				default:
					stopReason = "END_TURN"
				}
			} else {
				stopReason = "END_TURN"
			}
			if incomplete {
				tools = make(map[string]*toolUseState)
				toolOrder = toolOrder[:0]
				currentToolID = ""
				sawToolDelta = make(map[string]bool)
			} else if err := finishTools(); err != nil {
				return err
			}
			usage := mapValue(response["usage"])
			inputTokens, outputTokens = responseUsageTokens(usage)
			if callback.OnContextUsage != nil {
				if percentage, ok := numberValue(usage["kiro:context_usage_percentage"]); ok {
					callback.OnContextUsage(percentage)
				}
			}
			if callback.OnCredits != nil {
				if credits, ok := numberValue(mapValue(usage["kiro:credits"])["value"]); ok {
					callback.OnCredits(credits)
				}
			}
		case "error", "response.failed":
			errorValue := event
			if kind == "response.failed" {
				errorValue = mapValue(mapValue(event["response"])["error"])
			} else if nested := mapValue(event["error"]); len(nested) > 0 {
				errorValue = nested
			}
			return parseKiroStreamError(kiroStreamHeaders{eventType: "error"}, errorValue, []byte(payload))
		}
		if kind == "response.output_item.done" {
			// Keep completed calls until response.completed, matching the client:
			// an incomplete call must not be emitted as executable.
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := finishTools(); err != nil {
		return err
	}
	if !emitted {
		return errEmptyKiroStream
	}
	if stopReason == "" {
		stopReason = "END_TURN"
	}
	if callback.OnStopReason != nil {
		callback.OnStopReason(stopReason)
	}
	if callback.OnComplete != nil {
		callback.OnComplete(inputTokens, outputTokens)
	}
	return nil
}

func parseKiroReasoningEnvelope(raw string, callback *KiroStreamCallback, emitted *bool) {
	var envelope map[string]interface{}
	if json.Unmarshal([]byte(raw), &envelope) != nil {
		return
	}
	if redacted, _ := envelope["redactedContent"].(string); redacted != "" {
		if callback.OnRedactedReasoning != nil {
			*emitted = true
			callback.OnRedactedReasoning(redacted)
		}
		return
	}
	reasoningText := mapValue(envelope["reasoningText"])
	signature := stringValue(reasoningText, "signature")
	text := stringValue(reasoningText, "text")
	if signature != "" && text != "" && callback.OnReasoningSignature != nil {
		*emitted = true
		callback.OnReasoningSignature(signature)
	}
}

func responseUsageTokens(usage map[string]interface{}) (int, int) {
	input, _ := numberValue(usage["input_tokens"])
	output, _ := numberValue(usage["output_tokens"])
	return int(input), int(output)
}

func mapValue(value interface{}) map[string]interface{} {
	result, _ := value.(map[string]interface{})
	return result
}

func stringValue(values map[string]interface{}, key string) string {
	value, _ := values[key].(string)
	return value
}

func numberValue(value interface{}) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}
