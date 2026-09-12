package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/google"
	"github.com/tidwall/sjson"
)

// Fantasy v0.43 clamps every Google budget below 128, including the native
// API's supported 0 (off) and -1 (dynamic). Prowl owns budget serialization at
// the HTTP boundary instead: the encoder never receives the budget, and the
// request context carries it without mutating shared options or model state.
type googleBudgetProvider struct {
	fantasy.Provider
}

func newGoogleProvider(client *http.Client, opts ...google.Option) (fantasy.Provider, error) {
	if client == nil {
		client = http.DefaultClient
	}
	httpClient := *client
	transport := httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	httpClient.Transport = googleBudgetTransport{transport}
	opts = append(opts, google.WithHTTPClient(&httpClient))
	provider, err := google.New(opts...)
	if err != nil {
		return nil, err
	}
	return googleBudgetProvider{provider}, nil
}

func (p googleBudgetProvider) LanguageModel(ctx context.Context, modelID string) (fantasy.LanguageModel, error) {
	model, err := p.Provider.LanguageModel(ctx, modelID)
	if err != nil {
		return nil, err
	}
	return googleBudgetModel{model}, nil
}

type googleBudgetModel struct {
	fantasy.LanguageModel
}

type googleBudgetKey struct{}

func googleBudgetCall(ctx context.Context, call fantasy.Call) (context.Context, fantasy.Call, error) {
	opts, ok := call.ProviderOptions[google.Name].(*google.ProviderOptions)
	if !ok || opts.ThinkingConfig == nil || opts.ThinkingConfig.ThinkingBudget == nil {
		return ctx, call, nil
	}
	if opts.ThinkingConfig.ThinkingLevel != nil {
		return ctx, call, &fantasy.Error{
			Title:   "invalid argument",
			Message: "thinking_level and thinking_budget are mutually exclusive",
		}
	}
	budget := *opts.ThinkingConfig.ThinkingBudget
	thinking := *opts.ThinkingConfig
	thinking.ThinkingBudget = nil
	options := *opts
	options.ThinkingConfig = &thinking
	call.ProviderOptions = maps.Clone(call.ProviderOptions)
	call.ProviderOptions[google.Name] = &options
	return context.WithValue(ctx, googleBudgetKey{}, budget), call, nil
}

func (m googleBudgetModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	ctx, call, err := googleBudgetCall(ctx, call)
	if err != nil {
		return nil, err
	}
	return m.LanguageModel.Generate(ctx, call)
}

func (m googleBudgetModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	ctx, call, err := googleBudgetCall(ctx, call)
	if err != nil {
		return nil, err
	}
	return m.LanguageModel.Stream(ctx, call)
}

type googleBudgetTransport struct {
	http.RoundTripper
}

func (t googleBudgetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	budget, ok := req.Context().Value(googleBudgetKey{}).(int64)
	if !ok || (!strings.HasSuffix(req.URL.Path, ":generateContent") &&
		!strings.HasSuffix(req.URL.Path, ":streamGenerateContent")) {
		return t.RoundTripper.RoundTrip(req)
	}
	if req.Body == nil {
		return nil, fmt.Errorf("Google generation request has no body")
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read Google generation request: %w", err)
	}
	body, err = sjson.SetBytes(body, "generationConfig.thinkingConfig.thinkingBudget", budget)
	if err != nil {
		return nil, fmt.Errorf("set Google thinking budget: %w", err)
	}
	request := new(http.Request)
	*request = *req
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return t.RoundTripper.RoundTrip(request)
}
