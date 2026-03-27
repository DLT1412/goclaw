package providers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// parseResponsesStream reads OpenAI Responses API SSE events from a reader.
// Calls onChunk for text deltas. Returns the final ChatResponse with usage.
// Shared by CodexProvider and OpenAIProvider (Responses API mode).
func parseResponsesStream(reader io.Reader, providerName string, onChunk func(StreamChunk)) (*ChatResponse, error) {
	result := &ChatResponse{FinishReason: "stop"}
	toolCalls := make(map[string]*responsesToolCallAcc)
	var toolCallOrder []string // preserves insertion order for deterministic output

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, SSEScanBufInit), SSEScanBufMax)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimPrefix(data, " ")
		if data == "[DONE]" {
			break
		}

		var event responsesSSEEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		processResponsesSSEEvent(&event, result, toolCalls, &toolCallOrder, onChunk)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s: stream read error: %w", providerName, err)
	}

	// Build tool calls from accumulators in insertion order (tracked by orderedKeys)
	for _, key := range toolCallOrder {
		acc := toolCalls[key]
		if acc == nil || acc.name == "" {
			continue
		}
		args := make(map[string]any)
		_ = json.Unmarshal([]byte(acc.rawArgs), &args)
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID:        acc.callID,
			Name:      acc.name,
			Arguments: args,
		})
	}

	if len(result.ToolCalls) > 0 {
		result.FinishReason = "tool_calls"
	}

	if onChunk != nil {
		onChunk(StreamChunk{Done: true})
	}

	return result, nil
}

// parseResponsesBody parses a non-streaming OpenAI Responses API response.
func parseResponsesBody(reader io.Reader, providerName string) (*ChatResponse, error) {
	var resp responsesAPIResponse
	if err := json.NewDecoder(reader).Decode(&resp); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", providerName, err)
	}

	result := &ChatResponse{FinishReason: "stop"}

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" {
					result.Content += c.Text
				}
			}
			if item.Phase != "" {
				result.Phase = item.Phase
			}

		case "function_call":
			args := make(map[string]any)
			_ = json.Unmarshal([]byte(item.Arguments), &args)
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: args,
			})

		case "reasoning":
			for _, s := range item.Summary {
				if s.Text != "" {
					result.Thinking += s.Text
				}
			}
		}
	}

	if len(result.ToolCalls) > 0 {
		result.FinishReason = "tool_calls"
	}

	switch resp.Status {
	case "incomplete":
		result.FinishReason = "length"
	case "failed":
		result.FinishReason = "error"
	}

	if resp.Usage != nil {
		u := resp.Usage
		result.Usage = &Usage{
			PromptTokens:     u.InputTokens,
			CompletionTokens: u.OutputTokens,
			TotalTokens:      u.TotalTokens,
		}
		if u.OutputTokensDetails != nil {
			result.Usage.ThinkingTokens = u.OutputTokensDetails.ReasoningTokens
		}
	}

	return result, nil
}

// processResponsesSSEEvent handles a single Responses API SSE event.
func processResponsesSSEEvent(event *responsesSSEEvent, result *ChatResponse, toolCalls map[string]*responsesToolCallAcc, toolCallOrder *[]string, onChunk func(StreamChunk)) {
	switch event.Type {
	case "response.output_text.delta":
		if event.Delta != "" {
			result.Content += event.Delta
			if onChunk != nil {
				onChunk(StreamChunk{Content: event.Delta})
			}
		}

	case "response.function_call_arguments.delta":
		if event.ItemID != "" {
			acc := toolCalls[event.ItemID]
			if acc == nil {
				acc = &responsesToolCallAcc{}
				toolCalls[event.ItemID] = acc
				*toolCallOrder = append(*toolCallOrder, event.ItemID)
			}
			acc.rawArgs += event.Delta
		}

	case "response.output_item.done":
		if event.Item != nil {
			switch event.Item.Type {
			case "message":
				if event.Item.Phase != "" {
					result.Phase = event.Item.Phase
				}
			case "function_call":
				acc := toolCalls[event.Item.ID]
				if acc == nil {
					acc = &responsesToolCallAcc{}
					*toolCallOrder = append(*toolCallOrder, event.Item.ID)
				}
				acc.callID = event.Item.CallID
				acc.name = event.Item.Name
				if event.Item.Arguments != "" {
					acc.rawArgs = event.Item.Arguments
				}
				toolCalls[event.Item.ID] = acc
			case "reasoning":
				for _, s := range event.Item.Summary {
					if s.Text != "" {
						result.Thinking += s.Text
						if onChunk != nil {
							onChunk(StreamChunk{Thinking: s.Text})
						}
					}
				}
			}
		}

	case "response.completed", "response.incomplete", "response.failed":
		if event.Response != nil {
			if event.Response.Usage != nil {
				u := event.Response.Usage
				result.Usage = &Usage{
					PromptTokens:     u.InputTokens,
					CompletionTokens: u.OutputTokens,
					TotalTokens:      u.TotalTokens,
				}
				if u.OutputTokensDetails != nil {
					result.Usage.ThinkingTokens = u.OutputTokensDetails.ReasoningTokens
				}
			}
			switch event.Response.Status {
			case "incomplete":
				result.FinishReason = "length"
			case "failed":
				result.FinishReason = "error"
			}
		}
	}
}
