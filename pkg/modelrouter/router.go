package modelrouter

import (
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type Input struct {
	UserMessage string
	Messages    []providers.Message
	HasMedia    bool
}

type Selection struct {
	RouterName string
	Target     string
	BlockID    string
	RuleIndex  int
}

type Router struct {
	Name   string
	Config config.ModelRouterConfig
}

func New(name string, routerConfig *config.ModelRouterConfig) *Router {
	if routerConfig == nil || strings.TrimSpace(name) == "" {
		return nil
	}
	cfg := *routerConfig
	cfg.Blocks = append([]config.ModelRouterBlock(nil), routerConfig.Blocks...)
	for i := range cfg.Blocks {
		cfg.Blocks[i].Rules = append([]config.ModelRouterRule(nil), routerConfig.Blocks[i].Rules...)
	}
	return &Router{Name: strings.TrimSpace(name), Config: cfg}
}

func (r *Router) Select(input Input) Selection {
	selection := Selection{RouterName: r.Name, RuleIndex: -1}
	if r == nil || !r.Config.Enabled {
		return selection
	}
	byID := make(map[string]config.ModelRouterBlock, len(r.Config.Blocks))
	for _, block := range r.Config.Blocks {
		byID[strings.TrimSpace(block.ID)] = block
	}
	target, blockID, ruleIndex := r.expand(byID, strings.TrimSpace(r.Config.Entry), input, map[string]bool{})
	selection.Target = target
	selection.BlockID = blockID
	selection.RuleIndex = ruleIndex
	return selection
}

func (r *Router) expand(
	byID map[string]config.ModelRouterBlock,
	blockID string,
	input Input,
	seen map[string]bool,
) (target string, selectedBlock string, ruleIndex int) {
	blockID = strings.TrimSpace(blockID)
	if blockID == "" || seen[blockID] {
		return "", "", -1
	}
	block, ok := byID[blockID]
	if !ok {
		return "", "", -1
	}
	seen[blockID] = true
	switch strings.TrimSpace(block.Type) {
	case config.ModelRouterBlockTypeModel:
		return strings.TrimSpace(block.Model), blockID, -1
	case config.ModelRouterBlockTypeRules:
		for i, rule := range block.Rules {
			if ruleMatches(rule, input) {
				target, selected, _ := r.expand(byID, rule.Target, input, seen)
				if target != "" {
					return target, selected, i
				}
			}
		}
	}
	return r.expand(byID, block.Fallback, input, seen)
}

func ruleMatches(rule config.ModelRouterRule, input Input) bool {
	userMessage := strings.TrimSpace(input.UserMessage)
	if userMessage == "" && len(input.Messages) > 0 {
		userMessage = latestUserMessage(input.Messages)
	}
	switch strings.TrimSpace(rule.Match) {
	case config.ModelRouterRuleContains:
		return strings.Contains(strings.ToLower(userMessage), strings.ToLower(strings.TrimSpace(rule.Value)))
	case config.ModelRouterRuleRegex:
		re, err := regexp.Compile(rule.Value)
		return err == nil && re.MatchString(userMessage)
	case config.ModelRouterRuleHasCode:
		return strings.Contains(userMessage, "```")
	case config.ModelRouterRuleHasMedia:
		return input.HasMedia || messagesHaveMedia(input.Messages)
	default:
		return false
	}
}

func latestUserMessage(messages []providers.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(messages[i].Role, "user") {
			return messages[i].Content
		}
	}
	return ""
}

func messagesHaveMedia(messages []providers.Message) bool {
	for _, msg := range messages {
		if len(msg.Media) > 0 {
			return true
		}
	}
	return false
}
