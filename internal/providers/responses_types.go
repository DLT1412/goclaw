package providers

// Wire types for the OpenAI Responses API.
// Used by both CodexProvider (OAuth flow) and OpenAIProvider (API key flow).

type responsesAPIResponse struct {
	ID     string          `json:"id"`
	Object string          `json:"object"`
	Model  string          `json:"model"`
	Output []responsesItem `json:"output"`
	Usage  *responsesUsage `json:"usage,omitempty"`
	Status string          `json:"status"`
}

type responsesItem struct {
	ID        string             `json:"id"`
	Type      string             `json:"type"` // "message", "function_call", "reasoning"
	Role      string             `json:"role,omitempty"`
	Phase     string             `json:"phase,omitempty"` // gpt-5.3-codex: "commentary" or "final_answer"
	Content   []responsesContent `json:"content,omitempty"`
	CallID    string             `json:"call_id,omitempty"`
	Name      string             `json:"name,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
	Summary   []responsesSummary `json:"summary,omitempty"`
}

type responsesContent struct {
	Type string `json:"type"` // "output_text"
	Text string `json:"text"`
}

type responsesSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesUsage struct {
	InputTokens         int                    `json:"input_tokens"`
	OutputTokens        int                    `json:"output_tokens"`
	TotalTokens         int                    `json:"total_tokens"`
	OutputTokensDetails *responsesTokenDetails `json:"output_tokens_details,omitempty"`
}

type responsesTokenDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// SSE streaming types

type responsesSSEEvent struct {
	Type     string                `json:"type"`
	Delta    string                `json:"delta,omitempty"`
	ItemID   string                `json:"item_id,omitempty"`
	Item     *responsesItem        `json:"item,omitempty"`
	Response *responsesAPIResponse `json:"response,omitempty"`
}

type responsesToolCallAcc struct {
	callID  string
	name    string
	rawArgs string
}
