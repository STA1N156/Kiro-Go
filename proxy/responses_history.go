package proxy

import (
	"context"
	"errors"
)

// expandPreviousResponseHistory rebuilds the conversation history that led up
// to prev. It walks the previous_response_id chain backwards (oldest → newest)
// and emits OpenAI messages for both stored inputs and stored outputs of every
// ancestor, so a multi-turn /v1/responses session preserves full context.
//
// Missing history must fail explicitly rather than silently send partial context.
func expandPreviousResponseHistory(ctx context.Context, prev *ResponsesObject) ([]OpenAIMessage, error) {
	if prev == nil {
		return nil, nil
	}

	chain, err := collectAncestorChain(ctx, prev)
	if err != nil {
		return nil, err
	}

	messages := make([]OpenAIMessage, 0)
	for _, node := range chain {
		// Inject the instructions stored on the ancestor as a system message
		// so it remains in scope for downstream turns. Without this, an early
		// system prompt set on response A would be lost the moment a new
		// turn omits it.
		if node.Instructions != "" {
			messages = append(messages, OpenAIMessage{
				Role:    "system",
				Content: node.Instructions,
			})
		}
		prior, err := parseResponsesInput(node.StoredInput)
		if err != nil {
			return nil, errors.New("历史对话无法读取，请重新发送完整对话")
		}
		messages = append(messages, prior...)
		messages = append(messages, outputToMessages(node.Output)...)
	}

	return messages, nil
}

// collectAncestorChain walks previous_response_id backwards, returning the
// chain in oldest-first order: [root, ..., parent, prev]. The walker is
// guarded by cancellation and a visited-set to detect cycles in stored data.
func collectAncestorChain(ctx context.Context, prev *ResponsesObject) ([]*ResponsesObject, error) {
	stack := []*ResponsesObject{prev}
	visited := map[string]bool{prev.ID: true}

	cursor := prev
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if cursor.PreviousResponseID == "" {
			break
		}
		if visited[cursor.PreviousResponseID] {
			return nil, errors.New("历史对话引用异常，请重新发送完整对话")
		}
		ancestor, err := loadResponse(cursor.PreviousResponseID)
		if err != nil || ancestor == nil {
			return nil, errors.New("历史对话已过期或缺失，请重新发送完整对话")
		}
		visited[ancestor.ID] = true
		stack = append(stack, ancestor)
		cursor = ancestor
	}

	// Reverse to oldest-first.
	for i, j := 0, len(stack)-1; i < j; i, j = i+1, j-1 {
		stack[i], stack[j] = stack[j], stack[i]
	}
	return stack, nil
}

func outputToMessages(items []ResponseOutputItem) []OpenAIMessage {
	if len(items) == 0 {
		return nil
	}
	out := make([]OpenAIMessage, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case "message":
			text := joinTextParts(item.Content)
			role := item.Role
			if role == "" {
				role = "assistant"
			}
			if text == "" && role == "assistant" {
				continue
			}
			out = append(out, OpenAIMessage{Role: role, Content: text})
		case "function_call":
			tc := ToolCall{ID: item.CallID, Type: "function"}
			if tc.ID == "" {
				tc.ID = item.ID
			}
			tc.Function.Name = item.Name
			tc.Function.Arguments = item.Arguments
			out = append(out, OpenAIMessage{
				Role:      "assistant",
				Content:   "",
				ToolCalls: []ToolCall{tc},
			})
		}
	}
	return out
}

func joinTextParts(parts []ResponseContentPart) string {
	if len(parts) == 0 {
		return ""
	}
	out := ""
	for _, p := range parts {
		if p.Type == "output_text" || p.Type == "text" || p.Type == "input_text" {
			out += p.Text
		}
	}
	return out
}
