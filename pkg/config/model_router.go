package config

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	ModelRouterProvider       = "model-router"
	ModelRouterBlockTypeModel = "model"
	ModelRouterBlockTypeRules = "rules"
	ModelRouterRuleContains   = "contains"
	ModelRouterRuleRegex      = "regex"
	ModelRouterRuleHasCode    = "has_code"
	ModelRouterRuleHasMedia   = "has_media"
)

type ModelRouterList []ModelRouterConfig

type ModelRouterConfig struct {
	Name    string             `json:"name,omitempty"    yaml:"name,omitempty"`
	Enabled bool               `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Entry   string             `json:"entry,omitempty"   yaml:"entry,omitempty"`
	Blocks  []ModelRouterBlock `json:"blocks,omitempty"  yaml:"blocks,omitempty"`
}

type ModelRouterBlock struct {
	ID       string            `json:"id"                 yaml:"id"`
	Type     string            `json:"type"               yaml:"type"`
	Model    string            `json:"model,omitempty"    yaml:"model,omitempty"`
	Rules    []ModelRouterRule `json:"rules,omitempty"    yaml:"rules,omitempty"`
	Fallback string            `json:"fallback,omitempty" yaml:"fallback,omitempty"`
}

type ModelRouterRule struct {
	Match  string `json:"match"           yaml:"match"`
	Value  string `json:"value,omitempty" yaml:"value,omitempty"`
	Target string `json:"target"          yaml:"target"`
}

func (r *ModelRouterConfig) Validate() error {
	return r.validate(true)
}

func (r *ModelRouterConfig) validate(requireName bool) error {
	if r == nil {
		return fmt.Errorf("model_router config is required")
	}
	if requireName && strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("model_router.name is required")
	}
	if !r.Enabled {
		return fmt.Errorf("model_router must be enabled")
	}
	entry := strings.TrimSpace(r.Entry)
	if entry == "" {
		return fmt.Errorf("model_router.entry is required")
	}
	if len(r.Blocks) == 0 {
		return fmt.Errorf("model_router.blocks must contain at least one block")
	}
	seen := make(map[string]bool, len(r.Blocks))
	for i, block := range r.Blocks {
		id := strings.TrimSpace(block.ID)
		if id == "" {
			return fmt.Errorf("model_router.blocks[%d].id is required", i)
		}
		if seen[id] {
			return fmt.Errorf("model_router.blocks[%d].id %q is duplicated", i, id)
		}
		seen[id] = true
	}
	if !seen[entry] {
		return fmt.Errorf("model_router.entry %q does not reference a block", entry)
	}
	for i, block := range r.Blocks {
		switch strings.TrimSpace(block.Type) {
		case ModelRouterBlockTypeModel:
			if strings.TrimSpace(block.Model) == "" {
				return fmt.Errorf("model_router.blocks[%d].model is required", i)
			}
		case ModelRouterBlockTypeRules:
			if len(block.Rules) == 0 {
				return fmt.Errorf("model_router.blocks[%d].rules must contain at least one rule", i)
			}
			for j, rule := range block.Rules {
				if strings.TrimSpace(rule.Target) == "" {
					return fmt.Errorf("model_router.blocks[%d].rules[%d].target is required", i, j)
				}
				if !seen[strings.TrimSpace(rule.Target)] {
					return fmt.Errorf(
						"model_router.blocks[%d].rules[%d].target %q does not reference a block",
						i,
						j,
						rule.Target,
					)
				}
				switch strings.TrimSpace(rule.Match) {
				case ModelRouterRuleContains:
					if strings.TrimSpace(rule.Value) == "" {
						return fmt.Errorf(
							"model_router.blocks[%d].rules[%d].value is required",
							i,
							j,
						)
					}
				case ModelRouterRuleRegex:
					if strings.TrimSpace(rule.Value) == "" {
						return fmt.Errorf(
							"model_router.blocks[%d].rules[%d].value is required",
							i,
							j,
						)
					}
					if _, err := regexp.Compile(rule.Value); err != nil {
						return fmt.Errorf(
							"model_router.blocks[%d].rules[%d].value regex is invalid: %w",
							i,
							j,
							err,
						)
					}
				case ModelRouterRuleHasCode, ModelRouterRuleHasMedia:
				default:
					return fmt.Errorf(
						"model_router.blocks[%d].rules[%d].match %q is unsupported",
						i,
						j,
						rule.Match,
					)
				}
			}
		default:
			return fmt.Errorf("model_router.blocks[%d].type %q is unsupported", i, block.Type)
		}
		if fallback := strings.TrimSpace(block.Fallback); fallback != "" && !seen[fallback] {
			return fmt.Errorf(
				"model_router.blocks[%d].fallback %q does not reference a block",
				i,
				block.Fallback,
			)
		}
	}
	return validateModelRouterAcyclic(entry, r.Blocks)
}

func validateModelRouterAcyclic(entry string, blocks []ModelRouterBlock) error {
	byID := make(map[string]ModelRouterBlock, len(blocks))
	for _, block := range blocks {
		byID[strings.TrimSpace(block.ID)] = block
	}
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var walk func(string) error
	walk = func(id string) error {
		id = strings.TrimSpace(id)
		if id == "" || visited[id] {
			return nil
		}
		if visiting[id] {
			return fmt.Errorf("model_router contains a cycle at block %q", id)
		}
		block, ok := byID[id]
		if !ok {
			return nil
		}
		visiting[id] = true
		for _, rule := range block.Rules {
			if err := walk(rule.Target); err != nil {
				return err
			}
		}
		if err := walk(block.Fallback); err != nil {
			return err
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	return walk(entry)
}

func (c *Config) ValidateModelRouters() error {
	if c == nil {
		return nil
	}
	modelNames := make(map[string]struct{}, len(c.ModelList))
	accountRouterNames := make(map[string]struct{}, len(c.AccountRouters))
	for _, model := range c.ModelList {
		if model == nil || model.IsModelRouter() {
			continue
		}
		if name := strings.TrimSpace(model.ModelName); name != "" {
			modelNames[name] = struct{}{}
		}
	}
	for i := range c.AccountRouters {
		if name := strings.TrimSpace(c.AccountRouters[i].Name); name != "" {
			accountRouterNames[name] = struct{}{}
		}
	}
	seen := make(map[string]int, len(c.ModelRouters))
	for i := range c.ModelRouters {
		router := &c.ModelRouters[i]
		name := strings.TrimSpace(router.Name)
		if name == "" {
			return fmt.Errorf("model_routers[%d].name is required", i)
		}
		if err := router.Validate(); err != nil {
			return fmt.Errorf("model_routers[%d]: %w", i, err)
		}
		if previous, ok := seen[name]; ok {
			return fmt.Errorf(
				"model_routers[%d].name %q duplicates model_routers[%d]",
				i,
				name,
				previous,
			)
		}
		seen[name] = i
		if _, ok := modelNames[name]; ok {
			return fmt.Errorf(
				"model_routers[%d].name %q conflicts with model_list model_name",
				i,
				name,
			)
		}
		if _, ok := accountRouterNames[name]; ok {
			return fmt.Errorf(
				"model_routers[%d].name %q conflicts with account_routers name",
				i,
				name,
			)
		}
	}
	return nil
}

func (c *Config) validateModelRouterReferences() error {
	if c == nil {
		return nil
	}
	targets := make(map[string]struct{})
	routerNames := make(map[string]int, len(c.ModelRouters))
	for _, model := range c.ModelList {
		if model == nil || model.IsModelRouter() {
			continue
		}
		if name := strings.TrimSpace(model.ModelName); name != "" {
			targets[name] = struct{}{}
		}
	}
	for i := range c.AccountRouters {
		if name := strings.TrimSpace(c.AccountRouters[i].Name); name != "" {
			targets[name] = struct{}{}
		}
	}
	for i := range c.ModelRouters {
		if name := strings.TrimSpace(c.ModelRouters[i].Name); name != "" {
			routerNames[name] = i
		}
	}
	for i := range c.ModelRouters {
		router := &c.ModelRouters[i]
		for _, block := range router.Blocks {
			if strings.TrimSpace(block.Type) != ModelRouterBlockTypeModel {
				continue
			}
			model := strings.TrimSpace(block.Model)
			if model == "" {
				continue
			}
			if routerIdx, ok := routerNames[model]; ok {
				return fmt.Errorf(
					"model_routers[%d] block %q references model router %q at model_routers[%d]",
					i,
					block.ID,
					model,
					routerIdx,
				)
			}
			if _, ok := targets[model]; !ok {
				return fmt.Errorf(
					"model_routers[%d] block %q references unknown model %q",
					i,
					block.ID,
					model,
				)
			}
		}
	}
	return nil
}

func (c *Config) MaterializeModelRouterModels() {
	if c == nil {
		return
	}
	models := c.ModelList[:0]
	for _, model := range c.ModelList {
		if model == nil || model.IsVirtual() && model.IsModelRouter() {
			continue
		}
		models = append(models, model)
	}
	c.ModelList = models
	for i := range c.ModelRouters {
		router := cloneModelRouterConfig(&c.ModelRouters[i])
		name := strings.TrimSpace(router.Name)
		if name == "" {
			continue
		}
		c.ModelList = append(c.ModelList, &ModelConfig{
			ModelName:   name,
			Provider:    ModelRouterProvider,
			Model:       name,
			ModelRouter: router,
			Enabled:     router.Enabled,
			isVirtual:   true,
		})
	}
}

func cloneModelRouterConfig(in *ModelRouterConfig) *ModelRouterConfig {
	if in == nil {
		return nil
	}
	out := *in
	out.Blocks = append([]ModelRouterBlock(nil), in.Blocks...)
	for i := range out.Blocks {
		out.Blocks[i].Rules = append([]ModelRouterRule(nil), in.Blocks[i].Rules...)
	}
	return &out
}
