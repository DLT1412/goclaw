package providers

import (
	"context"
	"io"
	"net/http"
	"strings"
)

type CodexRoutingDefaults struct {
	Strategy           string
	ExtraProviderNames []string
}

// CodexProvider implements Provider for the OpenAI Responses API,
// used with ChatGPT subscription via OAuth (Codex flow).
// Wire format: POST /codex/responses on chatgpt.com backend.
type CodexProvider struct {
	name            string
	apiBase         string // e.g. "https://api.openai.com/v1" or "https://chatgpt.com/backend-api"
	defaultModel    string
	client          *http.Client
	retryConfig     RetryConfig
	tokenSource     TokenSource
	routingDefaults *CodexRoutingDefaults
}

// NewCodexProvider creates a provider for the OpenAI Responses API with OAuth token.
func NewCodexProvider(name string, tokenSource TokenSource, apiBase, defaultModel string) *CodexProvider {
	if apiBase == "" {
		apiBase = "https://chatgpt.com/backend-api"
	}
	apiBase = strings.TrimRight(apiBase, "/")

	if defaultModel == "" {
		defaultModel = "gpt-5.4"
	}

	return &CodexProvider{
		name:         name,
		apiBase:      apiBase,
		defaultModel: defaultModel,
		client:       &http.Client{Timeout: DefaultHTTPTimeout},
		retryConfig:  DefaultRetryConfig(),
		tokenSource:  tokenSource,
	}
}

func (p *CodexProvider) Name() string           { return p.name }
func (p *CodexProvider) DefaultModel() string   { return p.defaultModel }
func (p *CodexProvider) SupportsThinking() bool { return true }
func (p *CodexProvider) WithRoutingDefaults(strategy string, extraProviderNames []string) *CodexProvider {
	p.routingDefaults = &CodexRoutingDefaults{
		Strategy:           strategy,
		ExtraProviderNames: append([]string(nil), extraProviderNames...),
	}
	return p
}
func (p *CodexProvider) RoutingDefaults() *CodexRoutingDefaults {
	if p.routingDefaults == nil {
		return nil
	}
	return &CodexRoutingDefaults{
		Strategy:           p.routingDefaults.Strategy,
		ExtraProviderNames: append([]string(nil), p.routingDefaults.ExtraProviderNames...),
	}
}
func (p *CodexProvider) RouteEligibility(ctx context.Context) RouteEligibility {
	if aware, ok := p.tokenSource.(RouteEligibilityAware); ok {
		return aware.RouteEligibility(ctx)
	}
	return RouteEligibility{Class: RouteEligibilityHealthy}
}

func (p *CodexProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// Codex Responses API requires stream=true; delegate to ChatStream with no chunk handler.
	return p.ChatStream(ctx, req, nil)
}

func (p *CodexProvider) ChatStream(ctx context.Context, req ChatRequest, onChunk func(StreamChunk)) (*ChatResponse, error) {
	body := p.buildRequestBody(req, true)

	respBody, err := RetryDo(ctx, p.retryConfig, func() (io.ReadCloser, error) {
		return p.doRequest(ctx, body)
	})
	if err != nil {
		return nil, err
	}
	defer respBody.Close()

	return parseResponsesStream(respBody, p.name, onChunk)
}
