package workflows

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
)

const (
	RepositoryBugFinderProfileSchema  = "repository-bug-finder-v1"
	RepositoryBugFinderPromptRevision = "repository-bug-finder-prompt-v2"
)

// RepositoryBugFinderProfileHashInput is the complete immutable profile
// identity shared by native campaign planning and legacy campaign recovery.
type RepositoryBugFinderProfileHashInput struct {
	Schema                 string   `json:"schema"`
	PromptRevision         string   `json:"prompt_revision"`
	AccountRef             string   `json:"account_ref"`
	Target                 string   `json:"target"`
	Focus                  string   `json:"focus"`
	ScopePolicy            string   `json:"scope_policy"`
	ScopePlanHash          string   `json:"scope_plan_hash"`
	Models                 string   `json:"models"`
	ModelGraphRevision     string   `json:"model_graph_revision"`
	EffectiveModels        []string `json:"effective_models"`
	IncludeDefaultReviewer bool     `json:"include_default_reviewer"`
	MaxContentBytes        int64    `json:"max_content_bytes"`
}

// NewRepositoryBugFinderProfileHashInput supplies the trusted schema and
// prompt revision so callers cannot silently drift from the built-in workflow.
func NewRepositoryBugFinderProfileHashInput(
	accountRef string,
	target string,
	focus string,
	scopePolicy string,
	scopePlanHash string,
	models string,
	modelGraphRevision string,
	effectiveModels []string,
	includeDefaultReviewer bool,
	maxContentBytes int64,
) RepositoryBugFinderProfileHashInput {
	effectiveModels, _ = canonicalRepositoryBugFinderEffectiveModels(effectiveModels, true)
	models = strings.Join(repositoryReviewModelNames(models), ",")
	return RepositoryBugFinderProfileHashInput{
		Schema: RepositoryBugFinderProfileSchema, PromptRevision: RepositoryBugFinderPromptRevision,
		AccountRef: strings.TrimSpace(accountRef), Target: strings.TrimSpace(target),
		Focus: focus, ScopePolicy: scopePolicy, ScopePlanHash: strings.TrimSpace(scopePlanHash),
		Models: strings.TrimSpace(models), ModelGraphRevision: strings.TrimSpace(modelGraphRevision),
		EffectiveModels: effectiveModels, IncludeDefaultReviewer: includeDefaultReviewer,
		MaxContentBytes: maxContentBytes,
	}
}

// RepositoryBugFinderProfileHash returns the exact profile identity stored by
// campaign-aware repository review plans.
func RepositoryBugFinderProfileHash(input RepositoryBugFinderProfileHashInput) (string, error) {
	if input.Schema != RepositoryBugFinderProfileSchema ||
		input.PromptRevision != RepositoryBugFinderPromptRevision ||
		input.AccountRef != strings.TrimSpace(input.AccountRef) ||
		input.Target == "" || input.Target != strings.TrimSpace(input.Target) ||
		input.ScopePlanHash == "" || input.ScopePlanHash != strings.TrimSpace(input.ScopePlanHash) ||
		input.ModelGraphRevision == "" ||
		input.ModelGraphRevision != strings.TrimSpace(input.ModelGraphRevision) ||
		(input.Models == "" && !input.IncludeDefaultReviewer) ||
		(len(input.EffectiveModels) == 0 && !input.IncludeDefaultReviewer) ||
		input.MaxContentBytes < 1 {
		return "", errors.New("invalid repository bug finder profile hash input")
	}
	canonicalModels := strings.Join(repositoryReviewModelNames(input.Models), ",")
	canonicalEffective, effectiveOK := canonicalRepositoryBugFinderEffectiveModels(
		input.EffectiveModels, false,
	)
	if canonicalModels != input.Models || !effectiveOK ||
		!slices.Equal(canonicalEffective, input.EffectiveModels) {
		return "", errors.New("noncanonical repository bug finder profile hash input")
	}
	data, _ := json.Marshal(map[string]any{
		"schema": input.Schema, "prompt_revision": input.PromptRevision,
		"account_ref": input.AccountRef, "target": input.Target, "focus": input.Focus,
		"scope_policy": input.ScopePolicy, "scope_plan_hash": input.ScopePlanHash,
		"models": input.Models, "model_graph_revision": input.ModelGraphRevision,
		"effective_models":         strings.Join(input.EffectiveModels, ","),
		"include_default_reviewer": input.IncludeDefaultReviewer,
		"max_content_bytes":        input.MaxContentBytes,
	})
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// RepositoryBugFinderLegacyResolvedProfileHash reproduces the exact
// resolver-bound profile map used by legacy native planning. It is used only
// to decide whether a historical completion checkpoint remains reusable.
func RepositoryBugFinderLegacyResolvedProfileHash(
	input RepositoryBugFinderProfileHashInput,
) (string, error) {
	if _, err := RepositoryBugFinderProfileHash(input); err != nil {
		return "", err
	}
	data, _ := json.Marshal(map[string]any{
		"schema": input.Schema, "prompt_revision": input.PromptRevision,
		"account_ref": input.AccountRef, "target": input.Target, "focus": input.Focus,
		"scope_policy": input.ScopePolicy, "scope_plan_hash": input.ScopePlanHash,
		"models":                   repositoryReviewModelNames(input.Models),
		"model_graph_revision":     input.ModelGraphRevision,
		"effective_models":         input.EffectiveModels,
		"include_default_reviewer": input.IncludeDefaultReviewer,
		"max_content_bytes":        input.MaxContentBytes,
	})
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func canonicalRepositoryBugFinderEffectiveModels(values []string, deduplicate bool) ([]string, bool) {
	canonical := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, false
		}
		if _, duplicate := seen[value]; duplicate {
			if deduplicate {
				continue
			}
			return nil, false
		}
		seen[value] = struct{}{}
		canonical = append(canonical, value)
	}
	return canonical, true
}

// RepositoryBugFinderEffectiveMaxContentBytes applies the resolver clamp used
// by workflow admission and legacy recovery.
func RepositoryBugFinderEffectiveMaxContentBytes(requested int64, resolvedMaximum int) (int64, error) {
	if resolvedMaximum < 1 {
		return 0, errors.New("invalid repository bug finder content bound")
	}
	if requested <= 0 || requested > int64(resolvedMaximum) {
		return int64(resolvedMaximum), nil
	}
	return requested, nil
}

func nativeRepositoryBugFinderProfileHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var profile map[string]any
	if decodeErr := json.Unmarshal(data, &profile); decodeErr != nil {
		return "", decodeErr
	}
	models, err := nativeRepositoryBugFinderProfileNames(profile["models"])
	if err != nil {
		return "", err
	}
	effectiveModels, err := nativeRepositoryBugFinderProfileNames(profile["effective_models"])
	if err != nil {
		return "", err
	}
	profile["models"] = strings.Join(models, ",")
	profile["effective_models"] = effectiveModels
	data, err = json.Marshal(profile)
	if err != nil {
		return "", err
	}
	var input RepositoryBugFinderProfileHashInput
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return "", err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errors.New("invalid repository bug finder profile hash input")
	}
	if input.Schema != RepositoryBugFinderProfileSchema ||
		input.PromptRevision != RepositoryBugFinderPromptRevision {
		return "", errors.New("invalid repository bug finder profile hash input")
	}
	return RepositoryBugFinderProfileHash(NewRepositoryBugFinderProfileHashInput(
		input.AccountRef, input.Target, input.Focus, input.ScopePolicy,
		input.ScopePlanHash, input.Models, input.ModelGraphRevision,
		input.EffectiveModels, input.IncludeDefaultReviewer, input.MaxContentBytes,
	))
}

func nativeRepositoryBugFinderProfileNames(value any) ([]string, error) {
	var raw []string
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		raw = strings.FieldsFunc(typed, func(r rune) bool {
			return r == ',' || r == '\n' || r == ';'
		})
	case []any:
		raw = make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, errors.New("invalid repository bug finder profile model list")
			}
			raw = append(raw, text)
		}
	default:
		return nil, errors.New("invalid repository bug finder profile model list")
	}
	names := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		name := strings.TrimSpace(item)
		if name == "" {
			return nil, errors.New("invalid repository bug finder profile model list")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, errors.New("invalid repository bug finder profile model list")
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names, nil
}
