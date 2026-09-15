package gateway

// One constant because it is advice that must stay true: starters_test pins
// every provider named here to a real adapter and real catalogue models. The
// earlier wording named Cerebras, which ships no models.
const FreeStarterHint = "Groq, Google AI Studio, and OpenRouter all have free tiers that need only an email"

// FreeStarterPlatforms is the machine-readable form of the hint above.
var FreeStarterPlatforms = []string{"groq", "google", "openrouter"}
