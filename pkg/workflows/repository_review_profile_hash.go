package workflows

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const (
	RepositoryBugFinderProfileSchema   = "repository-bug-finder-v1"
	RepositoryBugFinderPromptRevision = "repository-bug-finder-prompt-v2"
)

// RepositoryBugFinderProfileHashInput is the complete immutable profile
// identity shared by native campaign planning and legacy campaign recovery.
type RepositoryBugFinderProfileHashInput struct {
	Schema          string `json:"schema"`
	PromptRevision  string `json:"prompt_revision"`
	AccountRef      string `json:"account_ref"`
	Target          string `json:"target"`
	Focus           string `json:"focus"`
	ScopePolicy     string `json:"scope_policy"`
	ScopePlanHash   string `json:"scope_plan_hash"`
	Models          string `json:"models"`
	MaxContentBytes int64  `json:"max_content_bytes"`
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
	maxContentBytes int64,
) RepositoryBugFinderProfileHashInput {
	return RepositoryBugFinderProfileHashInput{
		Schema: RepositoryBugFinderProfileSchema, PromptRevision: RepositoryBugFinderPromptRevision,
		AccountRef: strings.TrimSpace(accountRef), Target: strings.TrimSpace(target),
		Focus: focus, ScopePolicy: scopePolicy, ScopePlanHash: strings.TrimSpace(scopePlanHash),
		Models: strings.TrimSpace(models), MaxContentBytes: maxContentBytes,
	}
}

// RepositoryBugFinderProfileHash returns the exact profile identity stored by
// campaign-aware repository review plans.
func RepositoryBugFinderProfileHash(input RepositoryBugFinderProfileHashInput) (string, error) {
	if input.Schema != RepositoryBugFinderProfileSchema ||
		input.PromptRevision != RepositoryBugFinderPromptRevision ||
		input.MaxContentBytes < 1 {
		return "", errors.New("invalid repository bug finder profile hash input")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func nativeRepositoryBugFinderProfileHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var input RepositoryBugFinderProfileHashInput
	if err := json.Unmarshal(data, &input); err != nil {
		return "", err
	}
	return RepositoryBugFinderProfileHash(input)
}
