package config

// PromptCacheConfig controls provider-supported caching. Empty fields inherit
// the provider setting; an omitted policy uses the provider's safe defaults.
// Explicit Gemini caching is opt-in because it creates paid storage resources.
type PromptCacheConfig struct {
	Mode string `json:"mode,omitempty" jsonschema:"description=Cache policy: auto uses supported defaults; off disables managed cache controls; explicit selects supported explicit caching,enum=auto,enum=off,enum=explicit"`
	TTL  string `json:"ttl,omitempty" jsonschema:"description=Provider-supported cache lifetime or auto for its default; unsupported lifetimes are rejected,example=5m,example=1h,example=30m,example=in_memory,example=24h"`

	// Gemini storage prices vary by model and billing plan. Requiring a quote
	// prevents an explicit cache from silently omitting its storage charge.
	StorageCostPer1MTokenHour *float64 `json:"storage_cost_per_1m_token_hour,omitempty" jsonschema:"description=USD per million cached tokens per hour; required for explicit Gemini caching,minimum=0"`
}
