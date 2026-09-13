package agent

import (
	"bytes"
	"io"
	"math"
	"sync"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"github.com/tidwall/gjson"
)

type providerUsageKey struct{}

// providerUsage retains counters the SDK may omit, including cache writes and
// usage received before a stream is cancelled. It contains no response text.
type providerUsage struct {
	sessionID string
	policy    promptCachePolicy
	mu        sync.Mutex

	protocol          string
	input             int64
	output            int64
	read              int64
	write             int64
	reasoning         int64
	write1h           int64
	total             int64
	hasInput          bool
	hasOutput         bool
	hasRead           bool
	hasWrite          bool
	hasReasoning      bool
	hasWrite1h        bool
	cost              float64
	hasCost           bool
	unknownWriteTTL   bool
	captureIncomplete bool
	observedWire      bool
}

func (u *providerUsage) observe(data []byte) bool {
	if !gjson.ValidBytes(data) {
		return false
	}
	root := gjson.ParseBytes(data)
	usage := root.Get("usage")
	if !usage.Exists() {
		usage = root.Get("response.usage")
	}
	if !usage.Exists() {
		usage = root.Get("message.usage")
	}
	if !usage.Exists() {
		usage = root.Get("usageMetadata")
	}
	if !usage.IsObject() {
		return true
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	capture := func(path string, dst *int64, present *bool) {
		value := usage.Get(path)
		if value.Type == gjson.Number && value.Int() >= 0 {
			*dst = max(*dst, value.Int())
			*present = true
		}
	}
	switch u.protocol {
	case anthropic.Name, bedrock.Name:
		capture("input_tokens", &u.input, &u.hasInput)
		capture("output_tokens", &u.output, &u.hasOutput)
		capture("cache_read_input_tokens", &u.read, &u.hasRead)
		capture("cache_creation_input_tokens", &u.write, &u.hasWrite)
		capture("cache_creation.ephemeral_1h_input_tokens", &u.write1h, &u.hasWrite1h)
	case google.Name, "google-vertex":
		capture("promptTokenCount", &u.input, &u.hasInput)
		capture("candidatesTokenCount", &u.output, &u.hasOutput)
		capture("cachedContentTokenCount", &u.read, &u.hasRead)
		capture("thoughtsTokenCount", &u.reasoning, &u.hasReasoning)
	default:
		capture("input_tokens", &u.input, &u.hasInput)
		capture("prompt_tokens", &u.input, &u.hasInput)
		capture("output_tokens", &u.output, &u.hasOutput)
		capture("completion_tokens", &u.output, &u.hasOutput)
		capture("input_tokens_details.cached_tokens", &u.read, &u.hasRead)
		capture("prompt_tokens_details.cached_tokens", &u.read, &u.hasRead)
		capture("input_tokens_details.cache_write_tokens", &u.write, &u.hasWrite)
		capture("prompt_tokens_details.cache_write_tokens", &u.write, &u.hasWrite)
		capture("output_tokens_details.reasoning_tokens", &u.reasoning, &u.hasReasoning)
		capture("completion_tokens_details.reasoning_tokens", &u.reasoning, &u.hasReasoning)
	}
	if total := usage.Get("total_tokens"); total.Type == gjson.Number {
		u.total = max(u.total, total.Int())
	}
	if total := usage.Get("totalTokenCount"); total.Type == gjson.Number {
		u.total = max(u.total, total.Int())
	}
	if cost := usage.Get("cost"); cost.Type == gjson.Number {
		value := cost.Float()
		if value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
			u.cost, u.hasCost = value, true
		}
	}
	return true
}

func (u *providerUsage) normalize(usage fantasy.Usage) fantasy.Usage {
	if u == nil {
		return usage
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.hasRead {
		usage.CacheReadTokens = u.read
	}
	if u.hasWrite {
		usage.CacheCreationTokens = u.write
	}
	if u.hasReasoning {
		usage.ReasoningTokens = u.reasoning
	}
	if u.hasInput {
		usage.InputTokens = u.input
		if u.protocol != anthropic.Name && u.protocol != bedrock.Name {
			usage.InputTokens = max(0, u.input-usage.CacheReadTokens-usage.CacheCreationTokens)
		}
	}
	if u.hasOutput {
		usage.OutputTokens = u.output
		if u.protocol == google.Name || u.protocol == "google-vertex" {
			usage.OutputTokens += usage.ReasoningTokens
		}
	}
	if u.hasInput || u.hasOutput || u.hasRead || u.hasWrite {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheCreationTokens
	} else {
		usage.TotalTokens = max(usage.TotalTokens, u.total)
	}
	return usage
}

// usageBody observes complete JSON events without draining or delaying the
// underlying response. The ordinary single-line SSE case uses the caller's
// read buffer directly. Only split lines or non-streaming bodies are retained.
type usageBody struct {
	io.ReadCloser
	mu          sync.Mutex
	usage       *providerUsage
	stream      bool
	line        []byte
	event       []byte
	discardLine bool
	closed      bool
}

const maxUsageEventBytes = 16 << 20

func (r *usageBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return n, err
	}
	if r.stream {
		r.observeStream(p[:n])
	} else if len(r.line)+n <= maxUsageEventBytes {
		r.line = append(r.line, p[:n]...)
	} else {
		r.discardLine = true
		r.markIncomplete()
	}
	if err == io.EOF {
		r.finish()
	}
	return n, err
}

func (r *usageBody) observeStream(data []byte) {
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			r.appendLine(data)
			return
		}
		if len(r.line) == 0 && !r.discardLine {
			r.observeLine(data[:end])
		} else {
			r.appendLine(data[:end])
			if !r.discardLine {
				r.observeLine(r.line)
			}
		}
		r.line = r.line[:0]
		r.discardLine = false
		data = data[end+1:]
	}
}

func (r *usageBody) appendLine(data []byte) {
	if r.discardLine {
		return
	}
	if len(r.line)+len(data) > maxUsageEventBytes {
		r.line = nil
		r.discardLine = true
		r.markIncomplete()
		return
	}
	r.line = append(r.line, data...)
}

func (r *usageBody) observeLine(line []byte) {
	if len(line) > maxUsageEventBytes {
		r.markIncomplete()
		return
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		if len(r.event) > 0 {
			r.usage.observe(r.event)
			r.event = r.event[:0]
		}
		return
	}
	data, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return
	}
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	if len(r.event) == 0 && r.usage.observe(data) {
		return
	}
	if len(r.event)+len(data)+1 > maxUsageEventBytes {
		r.event = nil
		r.markIncomplete()
		return
	}
	r.event = append(r.event, data...)
	r.event = append(r.event, '\n')
}

func (r *usageBody) markIncomplete() {
	r.usage.mu.Lock()
	r.usage.captureIncomplete = true
	r.usage.mu.Unlock()
}

func (r *usageBody) finish() {
	if r.stream {
		if len(r.line) > 0 && !r.discardLine {
			r.observeLine(r.line)
		}
		if len(r.event) > 0 {
			r.usage.observe(r.event)
		}
	} else if !r.discardLine {
		r.usage.observe(r.line)
	}
	r.line = nil
	r.event = nil
}

func (r *usageBody) Close() error {
	err := r.ReadCloser.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.finish()
		r.closed = true
	}
	return err
}

func (u *providerUsage) billing(model Model, usage fantasy.Usage, override *float64) (cost, equivalent float64, source string, captureComplete bool) {
	if u == nil {
		cost, equivalent, source = modelUsageCost(model, usage, override)
		return cost, equivalent, source, true
	}
	u.mu.Lock()
	policy, protocol := u.policy, u.protocol
	write1h, hasWrite1h := u.write1h, u.hasWrite1h
	unknownWriteTTL := u.unknownWriteTTL
	observedWire, hasCost, providerCost := u.observedWire, u.hasCost, u.cost
	captureComplete = !u.captureIncomplete
	u.mu.Unlock()
	if protocol == "openrouter" && observedWire {
		// The SDK's zero-valued float cannot distinguish a missing price from
		// a genuinely free request. Only the wire field can establish zero.
		override = nil
		if hasCost {
			override = &providerCost
		}
	}
	if (policy.openAIWrites || policy.protocol == "openai" && openAIModelAtLeast(policy.modelID, 5, 6)) && model.CatwalkCfg.CostPer1MInCached == 0 {
		model.CatwalkCfg.CostPer1MInCached = model.CatwalkCfg.CostPer1MIn * 1.25
	}
	cost, equivalent, source = modelUsageCost(model, usage, override)
	if policy.anthropicMarkers && policy.ttl == "1h" && !hasWrite1h {
		write1h = usage.CacheCreationTokens
	}
	write1h = min(write1h, usage.CacheCreationTokens)
	if write1h > 0 {
		correction := float64(write1h) / 1e6 * (model.CatwalkCfg.CostPer1MIn*2 - model.CatwalkCfg.CostPer1MInCached)
		equivalent += correction
		if source == "catalog" {
			cost += correction
		}
	}
	if unknownWriteTTL && !hasWrite1h && usage.CacheCreationTokens > 0 && source != "provider" {
		captureComplete = false
	}
	return cost, equivalent, source, captureComplete
}
