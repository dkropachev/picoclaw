package protocoltypes

// openAICompatibleHTTPProviders is the shared provider-family classification
// used by the generic HTTP factory and protocol-specific feature gates. It
// identifies wire compatibility; an individual upstream still has to expose
// the requested OpenAI resource and support the selected model.
var openAICompatibleHTTPProviders = map[string]struct{}{
	"openai":         {},
	"litellm":        {},
	"lmstudio":       {},
	"gpt4free":       {},
	"openrouter":     {},
	"groq":           {},
	"zhipu":          {},
	"nvidia":         {},
	"venice":         {},
	"nearai":         {},
	"ollama":         {},
	"moonshot":       {},
	"shengsuanyun":   {},
	"siliconflow":    {},
	"deepseek":       {},
	"cerebras":       {},
	"vivgrid":        {},
	"volcengine":     {},
	"vllm":           {},
	"qwen-portal":    {},
	"qwen-intl":      {},
	"qwen-us":        {},
	"mistral":        {},
	"avian":          {},
	"minimax":        {},
	"longcat":        {},
	"modelscope":     {},
	"novita":         {},
	"alibaba-coding": {},
	"zai":            {},
	"mimo":           {},
}

// UsesOpenAICompatibleHTTPTransport reports whether provider belongs to the
// registered generic OpenAI-compatible HTTP family. Unknown providers fail
// closed even when their spelling resembles a supported provider.
func UsesOpenAICompatibleHTTPTransport(provider string) bool {
	provider = NormalizeProvider(provider)
	_, ok := openAICompatibleHTTPProviders[provider]
	return ok
}
