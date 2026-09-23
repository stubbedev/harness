// Package catalog provides the provider and model catalog used to
// configure LLM providers. The struct shape is intentionally identical to
// the former charm.land/catwalk dependency so existing callers keep
// working, but the data is sourced live from models.dev (supplemented
// with OpenRouter's model API) and cached in the local SQLite database
// instead of a vendor catalog service.
package catalog

// Type represents the type of AI provider.
type Type string

// All the supported AI provider types.
const (
	TypeOpenAI       Type = "openai"
	TypeOpenAICompat Type = "openai-compat"
	TypeOpenRouter   Type = "openrouter"
	TypeVercel       Type = "vercel"
	TypeAnthropic    Type = "anthropic"
	TypeGoogle       Type = "google"
	TypeAzure        Type = "azure"
	TypeBedrock      Type = "bedrock"
	TypeVertexAI     Type = "google-vertex"
)

// InferenceProvider represents the inference provider identifier.
type InferenceProvider string

// All the inference providers supported by the system. The values are
// models.dev catalog ids: an id that models.dev does not publish cannot
// be adopted, so a constant here that has no entry upstream is dead
// weight. See known.go for the ids that changed spelling when the
// catalog moved off catwalk.
const (
	InferenceProviderOpenAI           InferenceProvider = "openai"
	InferenceProviderAnthropic        InferenceProvider = "anthropic"
	InferenceProviderGemini           InferenceProvider = "google"
	InferenceProviderAzure            InferenceProvider = "azure"
	InferenceProviderBedrock          InferenceProvider = "amazon-bedrock"
	InferenceProviderVertexAI         InferenceProvider = "google-vertex"
	InferenceProviderXAI              InferenceProvider = "xai"
	InferenceProviderZAI              InferenceProvider = "zai"
	InferenceProviderZAICoding        InferenceProvider = "zai-coding-plan"
	InferenceProviderZhipu            InferenceProvider = "zhipuai"
	InferenceProviderZhipuCoding      InferenceProvider = "zhipuai-coding-plan"
	InferenceProviderGROQ             InferenceProvider = "groq"
	InferenceProviderOpenRouter       InferenceProvider = "openrouter"
	InferenceProviderCerebras         InferenceProvider = "cerebras"
	InferenceProviderDeepSeek         InferenceProvider = "deepseek"
	InferenceProviderDeepInfra        InferenceProvider = "deepinfra"
	InferenceProviderVenice           InferenceProvider = "venice"
	InferenceProviderChutes           InferenceProvider = "chutes"
	InferenceProviderHuggingFace      InferenceProvider = "huggingface"
	InferenceProviderMistral          InferenceProvider = "mistral"
	InferenceProviderCohere           InferenceProvider = "cohere"
	InferenceProviderPerplexity       InferenceProvider = "perplexity"
	InferenceProviderTogetherAI       InferenceProvider = "togetherai"
	InferenceAIHubMix                 InferenceProvider = "aihubmix"
	InferenceKimiCoding               InferenceProvider = "kimi-for-coding"
	InferenceProviderCopilot          InferenceProvider = "github-copilot"
	InferenceProviderCortecs          InferenceProvider = "cortecs"
	InferenceProviderVercel           InferenceProvider = "vercel"
	InferenceProviderMiniMax          InferenceProvider = "minimax"
	InferenceProviderMiniMaxChina     InferenceProvider = "minimax-cn"
	InferenceProviderIoNet            InferenceProvider = "io-net"
	InferenceProviderQiniuCloud       InferenceProvider = "qiniu-ai"
	InferenceProviderNebius           InferenceProvider = "nebius"
	InferenceProviderNeuralwatt       InferenceProvider = "neuralwatt"
	InferenceProviderOpenCodeZen      InferenceProvider = "opencode"
	InferenceProviderOpenCodeGo       InferenceProvider = "opencode-go"
	InferenceProviderAlibabaSingapore InferenceProvider = "alibaba"
	InferenceProviderFireworks        InferenceProvider = "fireworks-ai"
	InferenceProviderBaseten          InferenceProvider = "baseten"
	InferenceProviderMoonshot         InferenceProvider = "moonshotai"
)

// Provider represents an AI provider configuration.
type Provider struct {
	Name                string            `json:"name"`
	ID                  InferenceProvider `json:"id"`
	APIKey              string            `json:"api_key,omitempty"`
	APIEndpoint         string            `json:"api_endpoint,omitempty"`
	Type                Type              `json:"type,omitempty"`
	DefaultLargeModelID string            `json:"default_large_model_id,omitempty"`
	DefaultSmallModelID string            `json:"default_small_model_id,omitempty"`
	Models              []Model           `json:"models,omitempty"`
	DefaultHeaders      map[string]string `json:"default_headers,omitempty"`
}

// ModelOptions stores extra options for models.
type ModelOptions struct {
	Temperature      *float64       `json:"temperature,omitempty"`
	TopP             *float64       `json:"top_p,omitempty"`
	TopK             *int64         `json:"top_k,omitempty"`
	FrequencyPenalty *float64       `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64       `json:"presence_penalty,omitempty"`
	ProviderOptions  map[string]any `json:"provider_options,omitempty"`
}

// Model represents an AI model configuration.
type Model struct {
	ID                 string       `json:"id"`
	Name               string       `json:"name"`
	CostPer1MIn        float64      `json:"cost_per_1m_in"`
	CostPer1MOut       float64      `json:"cost_per_1m_out"`
	CostPer1MInCached  float64      `json:"cost_per_1m_in_cached"`
	CostPer1MOutCached float64      `json:"cost_per_1m_out_cached"`
	ContextWindow      int64        `json:"context_window"`
	ReleaseDate        string       `json:"release_date,omitempty"`
	DefaultMaxTokens   int64        `json:"default_max_tokens"`
	CanReason          bool         `json:"can_reason"`
	ReasoningLevels    []string     `json:"reasoning_levels,omitempty"`
	SupportsImages     bool         `json:"supports_attachments"`
	Options            ModelOptions `json:"options,omitzero"`
}

// KnownProviderTypes returns all the known inference providers types.
func KnownProviderTypes() []Type {
	return []Type{
		TypeOpenAI,
		TypeOpenAICompat,
		TypeOpenRouter,
		TypeVercel,
		TypeAnthropic,
		TypeGoogle,
		TypeAzure,
		TypeBedrock,
		TypeVertexAI,
	}
}
