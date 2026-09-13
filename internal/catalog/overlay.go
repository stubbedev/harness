package catalog

// overlay carries the per-provider metadata that models.dev does not
// model: the harness provider identity, the wire protocol each provider
// speaks, endpoints and key environment templates, default headers, and
// the default large/small model selection. Everything else (the model
// list itself) comes from the live sources.
type overlay struct {
	// ID is the harness provider identifier.
	ID InferenceProvider
	// Name is the display name used when the source has none.
	Name string
	// SourceID is the provider id on models.dev. Empty means the
	// provider has no models.dev entry and keeps its embedded seed
	// model list.
	SourceID string
	// Type is the wire protocol dispatched to the LLM client adapters.
	Type Type
	// APIEndpoint is the base URL template. $VAR references are
	// resolved by the config layer the same way catwalk templates were.
	APIEndpoint string
	// APIKey is the API key template, usually a $VAR reference.
	APIKey string
	// DefaultHeaders are sent with every request to this provider.
	DefaultHeaders map[string]string
	// DefaultLargeModelID and DefaultSmallModelID seed the default
	// model selection. When either is missing from the fetched model
	// list, a heuristic picks a replacement.
	DefaultLargeModelID string
	DefaultSmallModelID string
	// LiveModelSource overrides the models.dev model list for this
	// provider. Only OpenRouter uses this today: its first-party model
	// API carries reasoning effort metadata models.dev lacks.
	LiveModelSource liveSource
}

// liveSource identifies an alternative live model-list source.
type liveSource string

const (
	liveSourceOpenRouter liveSource = "openrouter"
)

// defaultLargeModelID and defaultSmallModelID on the overlay below were
// seeded from the catalog that shipped with the catwalk dependency at
// v0.52.30 and are refreshed by hand only when a provider's lineup
// changes in a way the heuristic cannot handle.
var overlays = []overlay{
	{
		ID:                  "openai",
		Name:                "OpenAI",
		SourceID:            "openai",
		Type:                TypeOpenAI,
		APIEndpoint:         "$OPENAI_API_ENDPOINT",
		APIKey:              "$OPENAI_API_KEY",
		DefaultLargeModelID: "gpt-5.6-sol",
		DefaultSmallModelID: "gpt-5.6-luna",
	},
	{
		ID:                  "anthropic",
		Name:                "Anthropic",
		SourceID:            "anthropic",
		Type:                TypeAnthropic,
		APIEndpoint:         "$ANTHROPIC_API_ENDPOINT",
		APIKey:              "$ANTHROPIC_API_KEY",
		DefaultLargeModelID: "claude-sonnet-4-6",
		DefaultSmallModelID: "claude-haiku-4-5-20251001",
	},
	{
		ID:                  "gemini",
		Name:                "Google",
		SourceID:            "google",
		Type:                TypeGoogle,
		APIEndpoint:         "$GEMINI_API_ENDPOINT",
		APIKey:              "$GEMINI_API_KEY",
		DefaultLargeModelID: "gemini-3.1-pro-preview-customtools",
		DefaultSmallModelID: "gemini-3-flash-preview",
	},
	{
		ID:                  "azure",
		Name:                "Azure",
		SourceID:            "azure",
		Type:                TypeAzure,
		APIEndpoint:         "$AZURE_OPENAI_API_ENDPOINT",
		APIKey:              "$AZURE_OPENAI_API_KEY",
		DefaultLargeModelID: "gpt-5",
		DefaultSmallModelID: "gpt-5-mini",
	},
	{
		ID:                  "bedrock",
		Name:                "Amazon Bedrock",
		SourceID:            "amazon-bedrock",
		Type:                TypeBedrock,
		DefaultLargeModelID: "us.anthropic.claude-opus-5",
		DefaultSmallModelID: "us.anthropic.claude-haiku-4-5-20251001-v1:0",
	},
	{
		ID:                  "vertexai",
		Name:                "Google Vertex AI",
		SourceID:            "google-vertex",
		Type:                TypeVertexAI,
		DefaultLargeModelID: "gemini-3.1-pro-preview",
		DefaultSmallModelID: "gemini-3-flash-preview",
	},
	{
		ID:                  "copilot",
		Name:                "GitHub Copilot",
		SourceID:            "github-copilot",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.githubcopilot.com",
		DefaultLargeModelID: "claude-sonnet-5",
		DefaultSmallModelID: "claude-haiku-4.5",
	},
	{
		ID:                  "openrouter",
		Name:                "OpenRouter",
		SourceID:            "openrouter",
		Type:                TypeOpenRouter,
		APIEndpoint:         "https://openrouter.ai/api/v1",
		APIKey:              "$OPENROUTER_API_KEY",
		DefaultHeaders:      map[string]string{"HTTP-Referer": "https://harness.dev", "X-Title": "Harness"},
		DefaultLargeModelID: "anthropic/claude-sonnet-4.6",
		DefaultSmallModelID: "anthropic/claude-haiku-4.5",
		LiveModelSource:     liveSourceOpenRouter,
	},
	{
		ID:                  "vercel",
		Name:                "Vercel",
		SourceID:            "vercel",
		Type:                TypeVercel,
		APIEndpoint:         "https://ai-gateway.vercel.sh/v1",
		APIKey:              "$VERCEL_API_KEY",
		DefaultHeaders:      map[string]string{"http-referer": "https://harness.dev", "x-title": "Harness"},
		DefaultLargeModelID: "anthropic/claude-sonnet-4",
		DefaultSmallModelID: "anthropic/claude-haiku-4.5",
	},
	{
		ID:                  "xai",
		Name:                "xAI",
		SourceID:            "xai",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.x.ai/v1",
		APIKey:              "$XAI_API_KEY",
		DefaultLargeModelID: "grok-4.5",
		DefaultSmallModelID: "grok-4.5",
	},
	{
		ID:                  "zai",
		Name:                "Z.AI",
		SourceID:            "zai",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.z.ai/api/coding/paas/v4",
		APIKey:              "$ZAI_API_KEY",
		DefaultLargeModelID: "glm-5.2",
		DefaultSmallModelID: "glm-5-turbo",
	},
	{
		ID:                  "zhipu",
		Name:                "Zhipu",
		SourceID:            "zhipuai",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://open.bigmodel.cn/api/paas/v4",
		APIKey:              "$ZHIPU_API_KEY",
		DefaultLargeModelID: "glm-4.7",
		DefaultSmallModelID: "glm-4.7-flash",
	},
	{
		ID:                  "zhipu-coding",
		Name:                "Zhipu Coding",
		SourceID:            "zhipuai-coding-plan",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://open.bigmodel.cn/api/coding/paas/v4",
		APIKey:              "$ZHIPU_API_KEY",
		DefaultLargeModelID: "glm-5.2",
		DefaultSmallModelID: "glm-5-turbo",
	},
	{
		ID:                  "groq",
		Name:                "Groq",
		SourceID:            "groq",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.groq.com/openai/v1",
		APIKey:              "$GROQ_API_KEY",
		DefaultLargeModelID: "moonshotai/kimi-k2-instruct-0905",
		DefaultSmallModelID: "qwen/qwen3-32b",
	},
	{
		ID:                  "cerebras",
		Name:                "Cerebras",
		SourceID:            "cerebras",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.cerebras.ai/v1",
		APIKey:              "$CEREBRAS_API_KEY",
		DefaultHeaders:      map[string]string{"X-Cerebras-3rd-Party-Integration": "harness"},
		DefaultLargeModelID: "gpt-oss-120b",
		DefaultSmallModelID: "qwen-3.8-27b",
	},
	{
		ID:                  "venice",
		Name:                "Venice",
		SourceID:            "venice",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.venice.ai/api/v1",
		APIKey:              "$VENICE_API_KEY",
		DefaultLargeModelID: "claude-fable-5",
		DefaultSmallModelID: "deepseek-v4-flash",
	},
	{
		ID:                  "chutes",
		Name:                "Chutes",
		SourceID:            "chutes",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://llm.chutes.ai/v1",
		APIKey:              "$CHUTES_API_KEY",
		DefaultLargeModelID: "moonshotai/Kimi-K2.6-TEE",
		DefaultSmallModelID: "google/gemma-4-31B-turbo-TEE",
	},
	{
		ID:                  "huggingface",
		Name:                "Hugging Face",
		SourceID:            "huggingface",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://router.huggingface.co/v1",
		APIKey:              "$HF_TOKEN",
		DefaultHeaders:      map[string]string{"HTTP-Referer": "https://harness.dev", "X-Title": "Harness"},
		DefaultLargeModelID: "zai-org/GLM-5.2:fireworks-ai",
		DefaultSmallModelID: "google/gemma-4-31B-it:cerebras",
	},
	{
		ID:                  "aihubmix",
		Name:                "AIHubMix",
		SourceID:            "aihubmix",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://aihubmix.com/v1",
		APIKey:              "$AIHUBMIX_API_KEY",
		DefaultHeaders:      map[string]string{"APP-Code": "IUFF7106"},
		DefaultLargeModelID: "gpt-5",
		DefaultSmallModelID: "gpt-5-nano",
	},
	{
		ID:                  "kimi-coding",
		Name:                "Kimi for Coding",
		SourceID:            "kimi-for-coding",
		Type:                TypeAnthropic,
		APIEndpoint:         "https://api.kimi.com/coding",
		APIKey:              "$KIMI_CODING_API_KEY",
		DefaultLargeModelID: "k3",
		DefaultSmallModelID: "kimi-for-coding",
	},
	{
		ID:                  "cortecs",
		Name:                "Cortecs",
		SourceID:            "cortecs",
		Type:                TypeOpenAI,
		APIEndpoint:         "https://api.cortecs.ai/v1",
		APIKey:              "$CORTECS_API_KEY",
		DefaultLargeModelID: "qwen3-coder-30b-a3b-instruct",
		DefaultSmallModelID: "glm-4.7-flash",
	},
	{
		ID:                  "minimax",
		Name:                "MiniMax",
		SourceID:            "minimax",
		Type:                TypeAnthropic,
		APIEndpoint:         "https://api.minimax.io/anthropic",
		APIKey:              "$MINIMAX_API_KEY",
		DefaultLargeModelID: "MiniMax-M2.7",
		DefaultSmallModelID: "MiniMax-M2.7",
	},
	{
		ID:                  "minimax-china",
		Name:                "MiniMax China",
		SourceID:            "minimax-cn",
		Type:                TypeAnthropic,
		APIEndpoint:         "https://api.minimaxi.com/anthropic",
		APIKey:              "$MINIMAX_API_KEY",
		DefaultLargeModelID: "MiniMax-M2.7",
		DefaultSmallModelID: "MiniMax-M2.7",
	},
	{
		ID:                  "ionet",
		Name:                "io.net",
		SourceID:            "",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.intelligence.io.solutions/api/v1",
		APIKey:              "$IONET_API_KEY",
		DefaultLargeModelID: "moonshotai/Kimi-K2.5",
		DefaultSmallModelID: "zai-org/GLM-4.7-Flash",
	},
	{
		ID:                  "qiniucloud",
		Name:                "Qiniu Cloud",
		SourceID:            "qiniu-ai",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.qnaigc.com/v1",
		APIKey:              "$QINIUCLOUD_API_KEY",
		DefaultLargeModelID: "minimax/minimax-m2.5",
		DefaultSmallModelID: "glm-4.5",
	},
	{
		ID:                  "avian",
		Name:                "Avian",
		SourceID:            "",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.avian.io/v1",
		APIKey:              "$AVIAN_API_KEY",
		DefaultLargeModelID: "moonshotai/kimi-k2.5",
		DefaultSmallModelID: "deepseek/deepseek-v3.2",
	},
	{
		ID:                  "nebius",
		Name:                "Nebius",
		SourceID:            "nebius",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.tokenfactory.nebius.com/v1",
		APIKey:              "$NEBIUS_API_KEY",
		DefaultLargeModelID: "moonshotai/Kimi-K2.5",
		DefaultSmallModelID: "nvidia/NVIDIA-Nemotron-3-Nano-30B-A3B",
	},
	{
		ID:                  "neuralwatt",
		Name:                "NeuralWatt",
		SourceID:            "neuralwatt",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.neuralwatt.com/v1",
		APIKey:              "$NEURALWATT_API_KEY",
		DefaultLargeModelID: "glm-5.2",
		DefaultSmallModelID: "glm-5.2-fast",
	},
	{
		ID:                  "opencode-zen",
		Name:                "OpenCode Zen",
		SourceID:            "opencode",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://opencode.ai/zen/v1",
		APIKey:              "$OPENCODE_API_KEY",
		DefaultLargeModelID: "deepseek-v4-flash-free",
		DefaultSmallModelID: "deepseek-v4-flash-free",
	},
	{
		ID:                  "opencode-go",
		Name:                "OpenCode Go",
		SourceID:            "opencode-go",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://opencode.ai/zen/go/v1",
		APIKey:              "$OPENCODE_API_KEY",
		DefaultLargeModelID: "minimax-m2.7",
		DefaultSmallModelID: "minimax-m2.7",
	},
	{
		ID:                  "alibaba-singapore",
		Name:                "Alibaba Singapore",
		SourceID:            "alibaba",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		APIKey:              "$ALIBABA_SINGAPORE_API_KEY",
		DefaultLargeModelID: "qwen3.8-max",
		DefaultSmallModelID: "qwen3.8-flash",
	},
	{
		ID:                  "alibaba-us",
		Name:                "Alibaba US",
		SourceID:            "",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://dashscope-us.aliyuncs.com/compatible-mode/v1",
		APIKey:              "$ALIBABA_US_API_KEY",
		DefaultLargeModelID: "deepseek-v4-pro-us",
		DefaultSmallModelID: "deepseek-v4-flash-us",
	},
	{
		ID:                  "fireworks",
		Name:                "Fireworks",
		SourceID:            "fireworks-ai",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.fireworks.ai/inference/v1",
		APIKey:              "$FIREWORKS_API_KEY",
		DefaultLargeModelID: "accounts/fireworks/models/deepseek-v4-pro-0813",
		DefaultSmallModelID: "accounts/fireworks/models/deepseek-v4-flash-0731",
	},
	{
		ID:                  "baseten",
		Name:                "Baseten",
		SourceID:            "baseten",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://inference.baseten.co/v1",
		APIKey:              "$BASETEN_API_KEY",
		DefaultLargeModelID: "deepseek-ai/DeepSeek-V4-Pro",
		DefaultSmallModelID: "openai/gpt-oss-120b",
	},
	{
		ID:                  "moonshot",
		Name:                "Moonshot AI",
		SourceID:            "moonshotai",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.moonshot.ai/v1",
		APIKey:              "$MOONSHOT_API_KEY",
		DefaultLargeModelID: "kimi-k3",
		DefaultSmallModelID: "kimi-k2.7-code",
	},
	{
		ID:                  "atlascloud",
		Name:                "AtlasCloud",
		SourceID:            "",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.atlascloud.ai/v1",
		APIKey:              "$ATLASCLOUD_API_KEY",
		DefaultLargeModelID: "zai-org/glm-5.2",
		DefaultSmallModelID: "deepseek-ai/deepseek-v4-flash",
	},
	{
		ID:                  "coralbricks",
		Name:                "Coral Bricks",
		SourceID:            "coralbricks",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://inference.coralbricks.ai/v1",
		APIKey:              "$CORALBRICKS_API_KEY",
		DefaultLargeModelID: "glm-5.3-fp4",
		DefaultSmallModelID: "gpt-oss-120b",
	},
	{
		ID:                  "pioneer",
		Name:                "Pioneer",
		SourceID:            "pioneer",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.pioneer.ai/v1",
		APIKey:              "$PIONEER_API_KEY",
		DefaultLargeModelID: "claude-opus-4-6",
		DefaultSmallModelID: "Qwen/Qwen3.5-9B",
	},
	{
		ID:                  "scaleway",
		Name:                "Scaleway",
		SourceID:            "scaleway",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.scaleway.ai/v1",
		APIKey:              "$SCW_SECRET_KEY",
		DefaultLargeModelID: "qwen3.6-35b-a3b",
		DefaultSmallModelID: "gemma-4-26b-a4b-it",
	},
	{
		ID:                  "deepseek",
		Name:                "DeepSeek",
		SourceID:            "deepseek",
		Type:                TypeOpenAICompat,
		APIEndpoint:         "https://api.deepseek.com/v1",
		APIKey:              "$DEEPSEEK_API_KEY",
		DefaultLargeModelID: "deepseek-v4-pro",
		DefaultSmallModelID: "deepseek-v4-flash",
	},
}

// overlayFor returns the overlay entry for a harness provider id.
