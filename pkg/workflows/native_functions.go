package workflows

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/url"
	"os"
	osexec "os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

const (
	workflowStateDir                   = "workflow_state"
	workflowArtifactsDir               = "workflow_artifacts"
	maxNativeGitDiffFiles              = 4096
	maxNativeGitDiffFileBytes          = 128 << 10
	maxNativeGitDiffAggregateBytes     = 512 << 10
	maxNativeGitInventoryBytes         = 64 << 20
	maxNativeGitInventoryFiles         = 100_000
	maxNativeGitContentFileBytes       = 512 << 10
	maxNativeGitContentAggregateBytes  = 64 << 20
	maxNativeGitRemoteBytes            = 2048
	maxNativeGitErrorOutputBytes       = 64 << 10
	nativeGitDiffContextLines          = 80
	nativeGitDiffInterHunkContextLines = 16
)

var safeStorageSegmentPattern = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

type nativeFrozenGitScopeEntry struct {
	runID       string
	workflowRef string
	scope       any
	bytes       int
	expiresAt   time.Time
}

var nativeFrozenGitScopes = struct {
	sync.Mutex
	entries map[string]nativeFrozenGitScopeEntry
	bytes   int
}{entries: make(map[string]nativeFrozenGitScopeEntry)}

const (
	maxNativeFrozenGitScopes      = 8
	maxNativeFrozenGitScopeBytes  = 72 << 20
	maxNativeFrozenGitGlobalBytes = 288 << 20
	nativeFrozenGitScopeTTL       = 30 * time.Minute
)

var nativeFunctionNames = map[string]struct{}{
	"workflow.state":    {},
	"workflow.artifact": {},
	"git.inventory":     {},
	"git.diff":          {},
	"git.filter":        {},
	"review.repository": {},
	"evaluation.corpus": {},
}

// NativeFunctionNames returns a sorted copy of the workflow functions
// implemented by the runtime. Callers may mutate the returned slice.
func NativeFunctionNames() []string {
	names := make([]string, 0, len(nativeFunctionNames))
	for name := range nativeFunctionNames {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// IsNativeFunction reports whether name is implemented by the workflow
// runtime without an embedding FunctionRunner.
func IsNativeFunction(name string) bool {
	_, ok := nativeFunctionNames[strings.TrimSpace(name)]
	return ok
}

var nativeCodeExtensions = map[string]struct{}{
	".c":       {},
	".cc":      {},
	".cpp":     {},
	".cs":      {},
	".dart":    {},
	".ex":      {},
	".exs":     {},
	".erl":     {},
	".fs":      {},
	".go":      {},
	".graphql": {},
	".gql":     {},
	".h":       {},
	".hs":      {},
	".hpp":     {},
	".java":    {},
	".js":      {},
	".jsx":     {},
	".kt":      {},
	".lua":     {},
	".mjs":     {},
	".php":     {},
	".pl":      {},
	".proto":   {},
	".py":      {},
	".rb":      {},
	".rs":      {},
	".scala":   {},
	".sh":      {},
	".sql":     {},
	".svelte":  {},
	".swift":   {},
	".tf":      {},
	".ts":      {},
	".tsx":     {},
	".vue":     {},
	".zig":     {},
}

var nativeConfigExtensions = map[string]struct{}{
	".json": {},
	".toml": {},
	".yaml": {},
	".yml":  {},
}

var nativeBinaryExtensions = map[string]struct{}{
	".7z": {}, ".a": {}, ".avi": {}, ".bin": {}, ".bmp": {}, ".class": {},
	".dll": {}, ".dylib": {}, ".eot": {}, ".exe": {}, ".gif": {}, ".gz": {},
	".ico": {}, ".jar": {}, ".jpeg": {}, ".jpg": {}, ".mov": {}, ".mp3": {},
	".mp4": {}, ".o": {}, ".otf": {}, ".pdf": {}, ".png": {}, ".so": {},
	".tar": {}, ".ttf": {}, ".wav": {}, ".webm": {}, ".webp": {}, ".woff": {},
	".woff2": {}, ".zip": {},
}

var nativeTestMarkers = []string{"test", "tests", "spec", "__tests__", "__mocks__"}

var nativeExcludePatterns = []string{
	"**/.git/**",
	"**/node_modules/**",
	"**/vendor/**",
	"**/dist/**",
	"**/build/**",
	"**/target/**",
	"**/coverage/**",
	"*.lock",
	"go.sum",
	"package-lock.json",
	"pnpm-lock.yaml",
	"yarn.lock",
}

type nativeStateEnvelope struct {
	Key       string    `json:"key"`
	Value     any       `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

type nativeGitFile struct {
	Path      string `json:"path"`
	Mode      string `json:"mode"`
	BlobHash  string `json:"blobHash"`
	SizeBytes int64  `json:"sizeBytes"`
}

type nativeGitWorkspaceRef struct {
	ID          string
	RepoID      string
	RemoteURL   string
	UpstreamURL string
	Ref         string
	Path        string
}

// RunNativeFunction executes PicoClaw built-ins available to workflow
// `function/...` steps. The boolean reports whether name is a native function.
func RunNativeFunction(
	ctx context.Context,
	name string,
	args map[string]any,
	exec ExecutionContext,
) (map[string]any, bool, error) {
	name = strings.TrimSpace(name)
	if !IsNativeFunction(name) {
		return nil, false, nil
	}
	switch name {
	case "workflow.state":
		out, err := nativeWorkflowState(ctx, args, exec)
		return out, true, err
	case "workflow.artifact":
		out, err := nativeWorkflowArtifact(ctx, args, exec)
		return out, true, err
	case "git.inventory":
		out, err := nativeGitInventory(ctx, args, exec)
		return out, true, err
	case "git.diff":
		out, err := nativeGitDiff(ctx, args, exec)
		return out, true, err
	case "git.filter":
		out, err := nativeGitFilter(ctx, args, exec)
		return out, true, err
	case "review.repository":
		out, err := nativeRepositoryReview(ctx, args, exec)
		return out, true, err
	case "evaluation.corpus":
		out, err := nativeRepositoryEvaluationCorpus(ctx, args, exec)
		return out, true, err
	default:
		return nil, true, fmt.Errorf("unsupported native function %q", name)
	}
}

func nativeRepositoryReview(
	ctx context.Context,
	args map[string]any,
	exec ExecutionContext,
) (map[string]any, error) {
	action := strings.ToLower(strings.TrimSpace(nativeString(args, "action")))
	store := repoaudit.NewSQLiteStore(nativeWorkspace(exec))
	switch action {
	case "plan":
		files, err := nativeRepositoryReviewFiles(args["files"])
		if err != nil {
			return nil, fmt.Errorf("review repository files: %w", err)
		}
		commit, err := nativeBindRepositoryReviewInventory(ctx, args, exec, files)
		if err != nil {
			return nil, err
		}
		campaignID := strings.TrimSpace(nativeStringAny(args, "campaign_id", "campaignId"))
		if campaignID != "" && (exec.WorkflowRef != RepositoryBugFinderWorkflowRef ||
			!repoaudit.ValidRepositoryReviewCampaignID(campaignID)) {
			return nil, errors.New("repository review campaign authority is unavailable")
		}
		var profileHash string
		if campaignID == "" {
			profileDigest, hashErr := nativeStableHash(
				firstNonNil(args["profile"], "repository-bug-finder-v1"),
			)
			if hashErr != nil {
				return nil, hashErr
			}
			profileHash = "sha256:" + profileDigest
		} else {
			profileHash, err = nativeRepositoryBugFinderProfileHash(args["profile"])
			if err != nil {
				return nil, err
			}
		}
		var assignmentCatalog []repoaudit.RepositoryReviewAssignment
		if campaignID != "" {
			resolvedReviewers := repositoryReviewModelNames(args["resolved_reviewer_models"])
			includeDefaultReviewer := nativeBoolAny(args, "include_default_reviewer")
			if len(resolvedReviewers) == 0 && !includeDefaultReviewer {
				_, reviewerErr := RepositoryReviewRequiredAssignments(0)
				return nil, reviewerErr
			}
			assignmentCatalog, err = RepositoryBugFinderAssignmentCatalog(
				resolvedReviewers,
				includeDefaultReviewer,
				RepositoryBugFinderPromptRevision,
				profileHash,
			)
			if err != nil {
				return nil, err
			}
		}
		repository, err := nativeRepositoryReviewIdentity(ctx, args, exec)
		if err != nil {
			return nil, err
		}
		maximumPending := int(nativeInt64Any(args, "max_files", "maxFiles"))
		if maximumPending <= 0 || maximumPending > 128 {
			maximumPending = 24
		}
		resolvedReviewers := nativeStringSlice(args["resolved_reviewer_models"])
		includeDefaultReviewer := nativeBoolAny(args, "include_default_reviewer")
		reviewerCount := len(resolvedReviewers)
		if includeDefaultReviewer {
			reviewerCount++
		}
		if reviewerCount < 1 {
			reviewerCount = 1
		}
		inventoryHash := nativeStringAny(args, "inventory_hash", "inventoryHash")
		authoritative := nativeBoolAny(args, "authoritative")
		var plan repoaudit.Plan
		if campaignID == "" {
			maximumPending = nativeRepositoryReviewPendingLimit(maximumPending, reviewerCount)
			plan, err = store.PlanWithProfileLimitAuthoritative(
				ctx, repository, commit, inventoryHash, profileHash, files,
				nativeBoolAny(args, "force"), maximumPending, authoritative,
			)
		} else {
			plan, err = store.PlanAssignmentsForCampaign(
				ctx, repository, commit, inventoryHash, profileHash,
				campaignID, assignmentCatalog, files,
				nativeBoolAny(args, "force"), maximumPending, authoritative,
			)
		}
		if err != nil {
			return nil, err
		}
		targetIsDefault := true
		if _, declared := args["target_is_default"]; declared {
			targetIsDefault = nativeBoolAny(args, "target_is_default")
		} else if _, declared = args["targetIsDefault"]; declared {
			targetIsDefault = nativeBoolAny(args, "targetIsDefault")
		}
		plan, err = repoaudit.BindPlanBranch(
			plan,
			nativeStringAny(args, "target_branch", "targetBranch"),
			nativeStringAny(args, "advertised_default_branch", "advertisedDefaultBranch"),
			targetIsDefault,
		)
		if err != nil {
			return nil, err
		}
		if campaignID != "" && len(plan.AssignmentPlans) > 0 {
			plan, err = BindRepositoryBugFinderAssignmentTasks(plan)
			if err != nil {
				return nil, err
			}
		}
		output, err := nativeRepositoryReviewPlanOutput(
			plan, args["files"], nativeBoolAny(args, "compact_output", "compactOutput"),
		)
		if err != nil {
			return nil, err
		}
		_, workspace, err := nativeResolveGitWorkspace(exec, args)
		if err != nil {
			return nil, err
		}
		for _, file := range output["pendingFiles"].([]map[string]any) {
			if nativeMapValue(file["source"]) != nil {
				continue
			}
			source, sourceErr := nativeGitFileSource(workspace, nativeAnyString(file["path"]))
			if sourceErr != nil {
				return nil, sourceErr
			}
			file["source"] = source
		}
		output["reviewerModels"] = firstNonNil(args["resolved_reviewer_models"], args["reviewer_models"])
		output["accountRef"] = nativeStringAny(args, "resolved_account_ref")
		output["includeDefaultReviewer"] = nativeBoolAny(args, "include_default_reviewer")
		output["maxContentBytes"] = int(nativeInt64Any(args, "resolved_max_content_bytes"))
		output["maxFiles"] = maximumPending
		return output, nil
	case "begin":
		plan, err := nativeRepositoryReviewPlan(args["plan"])
		if err != nil {
			return nil, err
		}
		if len(plan.AssignmentCatalog) == 0 {
			return map[string]any{"stateVersion": plan.StateVersion}, nil
		}
		reviewable, err := nativeRepositoryReviewFiles(args["files"])
		if err != nil {
			return nil, fmt.Errorf("reviewable repository files: %w", err)
		}
		state, err := store.BeginRepositoryReviewRun(
			ctx,
			repoaudit.BeginRepositoryReviewRunRequest{
				Plan: plan,
				RunID: strings.TrimSpace(firstNonEmpty(
					nativeStringAny(args, "run_id", "runId"), exec.RunID,
				)),
				ReviewableFiles: reviewable,
			},
		)
		if err != nil {
			return nil, err
		}
		return map[string]any{"stateVersion": state.Version}, nil
	case "freeze":
		hydrationArgs := args
		if maximumFileBytes := int(nativeInt64Any(
			args, "max_file_content_bytes", "maxFileContentBytes",
		)); maximumFileBytes > 0 {
			hydrationArgs = cloneMap(args)
			hydrationArgs["max_content_bytes"] = maximumFileBytes
		}
		scope, err := hydrateImmutableGitScope(ctx, args["files"], hydrationArgs, exec)
		if err != nil {
			return nil, err
		}
		maximumGroupFiles := int(nativeInt64Any(args, "max_group_files", "maxGroupFiles"))
		if maximumGroupFiles <= 0 {
			maximumGroupFiles = 3
		}
		groupContentBytes := int(nativeInt64Any(
			args, "max_group_content_bytes", "maxGroupContentBytes",
		))
		if groupContentBytes <= 0 {
			groupContentBytes = int(nativeInt64Any(args, "max_content_bytes", "maxContentBytes"))
		}
		reviewableScope, unsupportedFiles, unavailableCount, err := nativeReviewableFrozenGitScope(
			scope, maximumGroupFiles, groupContentBytes,
		)
		if err != nil {
			return nil, err
		}
		token, err := storeNativeFrozenGitScope(exec, reviewableScope)
		if err != nil {
			return nil, err
		}
		output := map[string]any{
			"token":            token,
			"files":            nativeFrozenGitScopeReferences(reviewableScope),
			"reviewableCount":  len(reviewableScope),
			"unsupportedFiles": unsupportedFiles,
			"unavailableFiles": nativeRepositoryReviewUnavailableScopeFiles(scope),
			"unavailableCount": unavailableCount,
		}
		if copies := int(nativeInt64Any(args, "copies")); copies > 1 {
			if copies != 2 {
				discardNativeFrozenGitScope(token)
				return nil, errors.New("frozen Git scope copies must be one or two")
			}
			secondary, secondaryErr := storeNativeFrozenGitScope(exec, reviewableScope)
			if secondaryErr != nil {
				discardNativeFrozenGitScope(token)
				return nil, secondaryErr
			}
			output["secondaryToken"] = secondary
		}
		return output, nil
	case "record":
		plan, err := nativeRepositoryReviewPlan(args["plan"])
		if err != nil {
			return nil, err
		}
		unsupportedFiles := nativeRepositoryReviewUnsupportedFiles(args["managed_children"])
		unsupportedFiles = mergeNativeRepositoryUnsupportedFiles(
			unsupportedFiles,
			nativeRepositoryReviewUnsupportedScopeFiles(args["unsupported_files"]),
		)
		runID := strings.TrimSpace(
			firstNonEmpty(nativeStringAny(args, "run_id", "runId"), exec.RunID),
		)
		var result repoaudit.RecordResult
		if len(plan.AssignmentCatalog) > 0 {
			result, err = store.FinalizeRepositoryReviewRun(
				ctx,
				repoaudit.FinalizeRepositoryReviewRunRequest{
					Plan: plan, RunID: runID,
					UnsupportedFiles: unsupportedFiles,
					ExcludedFiles:    int(nativeInt64Any(args, "excluded_count", "excludedCount")),
				},
			)
		} else {
			var evidence nativeRepositoryReviewRecordEvidenceResult
			evidence, err = nativeRepositoryReviewRecordEvidence(args, plan)
			if err == nil {
				result, err = store.Record(ctx, repoaudit.RecordRequest{
					Plan: plan, RunID: runID,
					Observations:            evidence.Observations,
					ReviewEvidence:          evidence.ReviewEvidence,
					InspectedFiles:          evidence.InspectedFiles,
					CompletedFiles:          evidence.CompletedFiles,
					UnsupportedFiles:        unsupportedFiles,
					ExcludedFiles:           int(nativeInt64Any(args, "excluded_count", "excludedCount")),
					TargetBranch:            plan.TargetBranch,
					AdvertisedDefaultBranch: plan.AdvertisedDefaultBranch,
					TargetIsDefault:         plan.TargetIsDefault,
				})
			}
		}
		if err != nil {
			return nil, err
		}
		run, err := nativeJSONMap(result.Run)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"run":                run,
			"acceptedFindingIds": append([]string(nil), result.AcceptedFindingIDs...),
			"stateVersion":       result.State.Version,
		}, nil
	case "result":
		plan, err := nativeRepositoryReviewPlan(args["plan"])
		if err != nil {
			return nil, err
		}
		recorded := nativeMapValue(args["recorded"])
		run := nativeMapValue(recorded["run"])
		findingIDs := recorded["acceptedFindingIds"]
		summary := strings.TrimSpace(nativeAnyString(nativeMapValue(args["review"])["summary"]))
		if run == nil {
			if len(plan.PendingFiles)+len(plan.DeferredFiles) == 0 && plan.Authoritative {
				if _, finalizeErr := store.FinalizeNoopPlan(
					plan, int(nativeInt64Any(args, "excluded_count", "excludedCount")),
				); finalizeErr != nil &&
					!errors.Is(finalizeErr, repoaudit.ErrConflict) {
					return nil, finalizeErr
				}
			}
			run = map[string]any{
				"reviewed_files": 0, "unreviewed_files": 0,
				"inspected_files":   0,
				"unsupported_files": len(plan.UnsupportedFiles),
				"remaining_files":   len(plan.PendingFiles) + len(plan.DeferredFiles),
				"skipped_files":     len(plan.UnchangedFiles),
				"excluded_files":    int(nativeInt64Any(args, "excluded_count", "excludedCount")),
			}
			findingIDs = []string{}
		}
		if summary == "" {
			if len(plan.PendingFiles)+len(plan.DeferredFiles) == 0 {
				summary = "No changed reviewable files required model review."
			} else {
				summary = "Repository review batch completed."
			}
		}
		return map[string]any{"summary": summary, "findingIds": findingIDs, "run": run}, nil
	default:
		return nil, fmt.Errorf("unsupported review.repository action %q", action)
	}
}

func nativeRepositoryReviewPendingLimit(requested, reviewerCount int) int {
	requested = min(128, max(1, requested))
	reviewerCount = max(1, reviewerCount)
	return min(requested, max(1, 3*32/(4*reviewerCount)))
}

func storeNativeFrozenGitScope(exec ExecutionContext, scope any) (string, error) {
	if strings.TrimSpace(exec.RunID) == "" || strings.TrimSpace(exec.WorkflowRef) == "" {
		return "", errors.New("frozen Git scope requires a workflow run identity")
	}
	scopeBytes, err := nativeFrozenGitScopeMemoryBytes(scope)
	if err != nil {
		return "", err
	}
	if scopeBytes > maxNativeFrozenGitScopeBytes {
		return "", errors.New("frozen Git scope exceeds its size limit")
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	token := hex.EncodeToString(random)
	now := time.Now().UTC()
	nativeFrozenGitScopes.Lock()
	defer nativeFrozenGitScopes.Unlock()
	for key, entry := range nativeFrozenGitScopes.entries {
		if !now.Before(entry.expiresAt) {
			delete(nativeFrozenGitScopes.entries, key)
			nativeFrozenGitScopes.bytes -= entry.bytes
		}
	}
	if len(nativeFrozenGitScopes.entries) >= maxNativeFrozenGitScopes ||
		nativeFrozenGitScopes.bytes > maxNativeFrozenGitGlobalBytes-scopeBytes {
		return "", errors.New("frozen Git scope cache is at capacity")
	}
	nativeFrozenGitScopes.entries[token] = nativeFrozenGitScopeEntry{
		runID: exec.RunID, workflowRef: exec.WorkflowRef, scope: cloneJSONValue(scope),
		bytes: scopeBytes, expiresAt: now.Add(nativeFrozenGitScopeTTL),
	}
	nativeFrozenGitScopes.bytes += scopeBytes
	if exec.workspaceCleanup != nil {
		exec.workspaceCleanup.trackFrozen(token)
	}
	return token, nil
}

func nativeFrozenGitScopeMemoryBytes(scope any) (int, error) {
	items, wrapper, err := nativeScopeItems(scope)
	if err != nil {
		return 0, err
	}
	estimated := 2
	if wrapper != nil {
		metadata := cloneMap(wrapper)
		delete(metadata, "items")
		encoded, marshalErr := json.Marshal(metadata)
		if marshalErr != nil {
			return 0, marshalErr
		}
		estimated += len(encoded)
	}
	for _, item := range items {
		mapped, ok := item.(map[string]any)
		if !ok {
			return 0, errors.New("frozen Git scope item must be an object")
		}
		metadata := cloneMap(mapped)
		content, _ := metadata["content"].(string)
		delete(metadata, "content")
		encoded, marshalErr := json.Marshal(metadata)
		if marshalErr != nil {
			return 0, marshalErr
		}
		estimated += len(encoded) + len(content)
		if estimated > maxNativeFrozenGitScopeBytes {
			return estimated, nil
		}
	}
	return estimated, nil
}

func consumeNativeFrozenGitScope(exec ExecutionContext, token string) (any, error) {
	token = strings.TrimSpace(token)
	if len(token) != 64 {
		return nil, errors.New("frozen Git scope token is invalid")
	}
	nativeFrozenGitScopes.Lock()
	defer nativeFrozenGitScopes.Unlock()
	entry, ok := nativeFrozenGitScopes.entries[token]
	if !ok || entry.runID != exec.RunID || entry.workflowRef != exec.WorkflowRef ||
		!time.Now().UTC().Before(entry.expiresAt) {
		return nil, errors.New("frozen Git scope is unavailable")
	}
	delete(nativeFrozenGitScopes.entries, token)
	nativeFrozenGitScopes.bytes -= entry.bytes
	return entry.scope, nil
}

func discardNativeFrozenGitScope(token string) {
	nativeFrozenGitScopes.Lock()
	defer nativeFrozenGitScopes.Unlock()
	if entry, ok := nativeFrozenGitScopes.entries[token]; ok {
		delete(nativeFrozenGitScopes.entries, token)
		nativeFrozenGitScopes.bytes -= entry.bytes
	}
}

func nativeFrozenGitScopeReferences(scope any) []map[string]any {
	items, _, err := nativeScopeItems(scope)
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ref := make(map[string]any)
		for _, key := range []string{
			"path", "fileHash", "sizeBytes", "category", "mode", "selected",
			"contentBytes", "contentPromptBytes", "contentComplete", "contentUnavailable",
		} {
			if value, exists := item[key]; exists {
				ref[key] = value
			}
		}
		out = append(out, ref)
	}
	return out
}

func nativeReviewableFrozenGitScope(
	scope any,
	maximumGroupFiles int,
	maximumGroupBytes int,
) ([]map[string]any, []map[string]any, int, error) {
	items, _, err := nativeScopeItems(scope)
	if err != nil {
		return nil, nil, 0, err
	}
	reviewable := make([]map[string]any, 0, len(items))
	unsupported := make([]map[string]any, 0)
	unavailable := 0
	for index, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, nil, 0, fmt.Errorf("frozen Git scope item %d must be an object", index)
		}
		if complete, _ := item["contentComplete"].(bool); complete {
			reviewable = append(reviewable, item)
			continue
		}
		unavailable++
		reason := strings.TrimSpace(nativeAnyString(item["contentUnavailable"]))
		if reason == "binary" || reason == "file_too_large" {
			unsupported = append(unsupported, nativeFrozenGitScopeReferences([]map[string]any{item})[0])
		}
	}
	return nativeGroupFrozenReviewScope(
		reviewable, maximumGroupFiles, maximumGroupBytes,
	), unsupported, unavailable, nil
}

func nativeGroupFrozenReviewScope(
	files []map[string]any,
	maximumGroupSize int,
	maximumGroupBytes int,
) []map[string]any {
	if maximumGroupSize < 2 || len(files) < 2 {
		return files
	}
	remaining := append([]map[string]any(nil), files...)
	sort.Slice(remaining, func(i, j int) bool {
		return nativeAnyString(remaining[i]["path"]) < nativeAnyString(remaining[j]["path"])
	})
	out := make([]map[string]any, 0, len(files))
	groupNumber := 0
	for len(remaining) > 0 {
		groupNumber++
		seed := remaining[0]
		remaining = remaining[1:]
		group := []map[string]any{seed}
		groupBytes := int(nativeInt64Any(seed, "contentPromptBytes", "contentBytes", "sizeBytes", "size_bytes"))
		for len(group) < maximumGroupSize && len(remaining) > 0 {
			bestIndex, bestScore := -1, -1
			for index, candidate := range remaining {
				candidateBytes := int(
					nativeInt64Any(candidate, "contentPromptBytes", "contentBytes", "sizeBytes", "size_bytes"),
				)
				if maximumGroupBytes > 0 && groupBytes+candidateBytes > maximumGroupBytes {
					continue
				}
				score := 0
				for _, member := range group {
					score += nativeReviewRelationshipScore(member, candidate)
				}
				if score > bestScore {
					bestIndex, bestScore = index, score
				}
			}
			if bestIndex < 0 {
				break
			}
			group = append(group, remaining[bestIndex])
			groupBytes += int(
				nativeInt64Any(remaining[bestIndex], "contentPromptBytes", "contentBytes", "sizeBytes", "size_bytes"),
			)
			remaining = append(remaining[:bestIndex], remaining[bestIndex+1:]...)
		}
		groupID := fmt.Sprintf("review-group-%04d", groupNumber)
		for _, member := range group {
			member["reviewGroup"] = groupID
		}
		out = append(out, group...)
	}
	return out
}

func nativeReviewRelationshipScore(left, right map[string]any) int {
	leftPath := filepath.ToSlash(strings.TrimSpace(nativeAnyString(left["path"])))
	rightPath := filepath.ToSlash(strings.TrimSpace(nativeAnyString(right["path"])))
	score := 0
	if path.Dir(leftPath) == path.Dir(rightPath) {
		score += 8
	}
	leftStem := strings.TrimSuffix(path.Base(leftPath), path.Ext(leftPath))
	rightStem := strings.TrimSuffix(path.Base(rightPath), path.Ext(rightPath))
	if leftStem == rightStem {
		score += 6
	}
	leftContent, _ := left["content"].(string)
	rightContent, _ := right["content"].(string)
	if len(rightStem) >= 3 && strings.Contains(strings.ToLower(leftContent), strings.ToLower(rightStem)) {
		score += 4
	}
	if len(leftStem) >= 3 && strings.Contains(strings.ToLower(rightContent), strings.ToLower(leftStem)) {
		score += 4
	}
	return score
}

func nativeRepositoryReviewUnsupportedFiles(value any) []repoaudit.UnsupportedFile {
	children, err := nativeOptionalMapSlice(value)
	if err != nil {
		return nil
	}
	byPath := make(map[string]repoaudit.UnsupportedFile)
	for _, child := range children {
		items, itemErr := nativeOptionalMapSlice(child["scope"])
		if itemErr != nil {
			continue
		}
		for _, item := range items {
			reason := strings.TrimSpace(nativeAnyString(item["contentUnavailable"]))
			if reason != "binary" && reason != "file_too_large" {
				continue
			}
			complete, declared := item["contentComplete"].(bool)
			if reason == "" || declared && complete {
				continue
			}
			files, fileErr := nativeRepositoryReviewFiles([]map[string]any{item})
			if fileErr != nil || len(files) != 1 {
				continue
			}
			byPath[files[0].Path] = repoaudit.UnsupportedFile{FileRef: files[0], Reason: reason}
		}
	}
	out := make([]repoaudit.UnsupportedFile, 0, len(byPath))
	for _, unsupported := range byPath {
		out = append(out, unsupported)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func nativeRepositoryReviewUnsupportedScopeFiles(value any) []repoaudit.UnsupportedFile {
	items, err := nativeOptionalMapSlice(value)
	if err != nil {
		return nil
	}
	out := make([]repoaudit.UnsupportedFile, 0, len(items))
	for _, item := range items {
		reason := strings.TrimSpace(nativeAnyString(item["contentUnavailable"]))
		if reason != "binary" && reason != "file_too_large" {
			continue
		}
		files, fileErr := nativeRepositoryReviewFiles([]map[string]any{item})
		if fileErr == nil && len(files) == 1 {
			out = append(out, repoaudit.UnsupportedFile{FileRef: files[0], Reason: reason})
		}
	}
	return out
}

func nativeRepositoryReviewUnavailableScopeFiles(value any) []map[string]any {
	items, _, err := nativeScopeItems(value)
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(nativeAnyString(item["contentUnavailable"])) != "aggregate_limit" {
			continue
		}
		files, fileErr := nativeRepositoryReviewFiles([]map[string]any{item})
		if fileErr != nil || len(files) != 1 {
			continue
		}
		out = append(out, nativeRepositoryReviewFileMaps(files)[0])
	}
	sort.Slice(out, func(i, j int) bool {
		return nativeAnyString(out[i]["path"]) < nativeAnyString(out[j]["path"])
	})
	return out
}

func mergeNativeRepositoryUnsupportedFiles(groups ...[]repoaudit.UnsupportedFile) []repoaudit.UnsupportedFile {
	byPath := make(map[string]repoaudit.UnsupportedFile)
	for _, group := range groups {
		for _, file := range group {
			byPath[file.Path] = file
		}
	}
	out := make([]repoaudit.UnsupportedFile, 0, len(byPath))
	for _, file := range byPath {
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func nativeBindRepositoryReviewInventory(
	ctx context.Context,
	args map[string]any,
	exec ExecutionContext,
	files []repoaudit.FileRef,
) (string, error) {
	repo, _, err := nativeResolveGitWorkspace(exec, args)
	if err != nil {
		return "", err
	}
	commit, err := nativeResolveCommit(ctx, repo, nativeStringAny(args, "commit", "commit_sha", "commitSha"))
	if err != nil {
		return "", err
	}
	inventory, err := nativeCollectInventory(ctx, repo, commit)
	if err != nil {
		return "", err
	}
	inventoryHash, err := nativeStableHash(inventory)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(nativeStringAny(args, "inventory_hash", "inventoryHash")) != inventoryHash {
		return "", errors.New("repository review inventory hash does not match the exact commit")
	}
	trusted := make(map[string]nativeGitFile, len(inventory))
	for _, file := range inventory {
		trusted[file.Path] = file
	}
	for _, file := range files {
		entry, ok := trusted[file.Path]
		if !ok || entry.BlobHash != file.BlobSHA || entry.SizeBytes != file.SizeBytes ||
			file.Mode != "" && entry.Mode != file.Mode {
			return "", fmt.Errorf("repository review file %q does not match the exact commit", file.Path)
		}
	}
	return commit, nil
}

func nativeRepositoryReviewIdentity(
	ctx context.Context,
	args map[string]any,
	exec ExecutionContext,
) (string, error) {
	repo, workspace, err := nativeResolveGitWorkspace(exec, args)
	if err != nil {
		return "", fmt.Errorf("resolve repository review workspace: %w", err)
	}
	remote := ""
	if output, remoteErr := nativeGit(ctx, repo, "remote", "get-url", "origin"); remoteErr == nil {
		remote = strings.TrimSpace(output)
	}
	if workspace.UpstreamURL != "" && filepath.IsAbs(workspace.RemoteURL) {
		preservedUpstream, preservedErr := nativeGit(ctx, repo, "remote", "get-url", "picoclaw-upstream")
		remotePath, remoteErr := filepath.Abs(remote)
		sourcePath, sourceErr := filepath.Abs(workspace.RemoteURL)
		if preservedErr == nil && strings.TrimSpace(preservedUpstream) == workspace.UpstreamURL &&
			remoteErr == nil && sourceErr == nil && filepath.Clean(remotePath) == filepath.Clean(sourcePath) {
			remote = workspace.UpstreamURL
		}
	}
	derived := nativeGitHubRepositoryIdentity(remote)
	explicit := strings.TrimSpace(nativeString(args, "repository"))
	if explicit == "auto" {
		explicit = ""
	} else if explicit != "" {
		explicit = repoaudit.CanonicalRepositoryIdentity(explicit)
	}
	if derived != "" {
		if explicit != "" && explicit != derived {
			return "", fmt.Errorf(
				"repository review identity %q does not match acquired GitHub repository %q",
				explicit,
				derived,
			)
		}
		return derived, nil
	}
	sourceIdentity := repoaudit.CanonicalRepositoryIdentity(
		nativeRepositorySourceIdentity(remote, repo),
	)
	if explicit != "" && explicit != sourceIdentity {
		return "", errors.New("a publishable repository identity requires a matching acquired GitHub origin")
	}
	return sourceIdentity, nil
}

func nativeRepositorySourceIdentity(remote, fallbackRepo string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return fallbackRepo
	}
	if strings.Contains(remote, "@") && strings.Contains(remote, ":") &&
		!strings.Contains(remote, "://") {
		identity, pathValue, ok := strings.Cut(remote, ":")
		_, host, hasUser := strings.Cut(identity, "@")
		if ok && hasUser && host != "" && pathValue != "" {
			return "ssh://" + strings.ToLower(host) + "/" + strings.TrimPrefix(pathValue, "/")
		}
	}
	if parsed, err := url.Parse(remote); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	}
	localRemote := remote
	if !filepath.IsAbs(localRemote) {
		localRemote = filepath.Join(fallbackRepo, localRemote)
	}
	if absolute, err := filepath.Abs(localRemote); err == nil {
		if evaluated, evalErr := filepath.EvalSymlinks(absolute); evalErr == nil {
			return evaluated
		}
		return filepath.Clean(absolute)
	}
	return fallbackRepo
}

func nativeGitHubRepositoryIdentity(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	var repositoryPath string
	switch {
	case strings.Contains(remote, "@") && strings.Contains(remote, ":") && !strings.Contains(remote, "://"):
		identity, pathValue, ok := strings.Cut(remote, ":")
		_, host, hasUser := strings.Cut(identity, "@")
		if !ok || !hasUser || !strings.EqualFold(host, "github.com") {
			return ""
		}
		repositoryPath = pathValue
	default:
		parsed, err := url.Parse(remote)
		if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") ||
			parsed.RawQuery != "" || parsed.Fragment != "" ||
			(parsed.Scheme != "https" && parsed.Scheme != "ssh" && parsed.Scheme != "git") {
			return ""
		}
		repositoryPath = strings.TrimPrefix(parsed.Path, "/")
	}
	repositoryPath = strings.TrimSuffix(repositoryPath, ".git")
	owner, repository, ok := strings.Cut(repositoryPath, "/")
	if !ok || owner == "" || repository == "" || strings.Contains(repository, "/") ||
		!nativeValidGitHubName(owner, false) || !nativeValidGitHubName(repository, true) {
		return ""
	}
	return strings.ToLower(owner + "/" + repository)
}

func nativeValidGitHubName(value string, repository bool) bool {
	if len(value) > 100 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' ||
			repository && (character == '_' || character == '.') {
			continue
		}
		return false
	}
	return true
}

func nativeRepositoryReviewFiles(value any) ([]repoaudit.FileRef, error) {
	items, err := nativeMapSlice(value)
	if err != nil {
		return nil, err
	}
	files := make([]repoaudit.FileRef, 0, len(items))
	for index, item := range items {
		pathValue := strings.TrimSpace(nativeAnyString(item["path"]))
		hash := strings.TrimSpace(nativeAnyString(item["fileHash"]))
		if hash == "" {
			hash = strings.TrimSpace(nativeAnyString(item["blob_sha"]))
		}
		size := nativeInt64Any(item, "sizeBytes", "size_bytes")
		if pathValue == "" || hash == "" || size < 0 {
			return nil, fmt.Errorf("item %d is not an exact Git file reference", index)
		}
		files = append(files, repoaudit.FileRef{
			Path: pathValue, BlobSHA: hash, SizeBytes: size,
			Category: strings.TrimSpace(nativeAnyString(item["category"])),
			Mode:     strings.TrimSpace(nativeAnyString(item["mode"])),
		})
	}
	return files, nil
}

func nativeRepositoryReviewPlan(value any) (repoaudit.Plan, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return repoaudit.Plan{}, err
	}
	var plan repoaudit.Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return repoaudit.Plan{}, err
	}
	return plan, nil
}

func nativeRepositoryReviewPlanOutput(
	plan repoaudit.Plan,
	originalValue any,
	compact bool,
) (map[string]any, error) {
	value, err := nativeJSONMap(plan)
	if err != nil {
		return nil, err
	}
	originals, err := nativeMapSlice(originalValue)
	if err != nil {
		return nil, err
	}
	pending := nativeRepositoryReviewBoundFileMaps(plan.PendingFiles, originals)
	output := map[string]any{
		"plan":               value,
		"planId":             plan.ID,
		"pendingFiles":       pending,
		"pendingCount":       len(plan.PendingFiles),
		"deferredCount":      len(plan.DeferredFiles),
		"unsupportedCount":   len(plan.UnsupportedFiles),
		"unchangedCount":     len(plan.UnchangedFiles),
		"stateVersion":       plan.StateVersion,
		"previouslyReviewed": plan.PreviouslyReviewed,
	}
	if len(plan.AssignmentPlans) > 0 {
		output["assignmentPlans"] = value["assignment_plans"]
	}
	if !compact {
		output["deferredFiles"] = nativeRepositoryReviewBoundFileMaps(plan.DeferredFiles, originals)
		output["unsupportedFiles"] = plan.UnsupportedFiles
		output["unchangedFiles"] = nativeRepositoryReviewBoundFileMaps(plan.UnchangedFiles, originals)
	}
	return output, nil
}

func nativeRepositoryReviewBoundFileMaps(
	files []repoaudit.FileRef,
	originals []map[string]any,
) []map[string]any {
	byPath := make(map[string]map[string]any, len(originals))
	for _, original := range originals {
		byPath[strings.TrimSpace(nativeAnyString(original["path"]))] = original
	}
	out := make([]map[string]any, 0, len(files))
	for _, file := range files {
		if original := byPath[file.Path]; original != nil {
			bound := cloneMap(original)
			bound["path"] = file.Path
			bound["fileHash"] = file.BlobSHA
			bound["sizeBytes"] = file.SizeBytes
			out = append(out, bound)
			continue
		}
		out = append(out, nativeRepositoryReviewFileMaps([]repoaudit.FileRef{file})[0])
	}
	return out
}

func nativeRepositoryReviewFileMaps(files []repoaudit.FileRef) []map[string]any {
	out := make([]map[string]any, 0, len(files))
	for _, file := range files {
		out = append(out, map[string]any{
			"path": file.Path, "fileHash": file.BlobSHA, "sizeBytes": file.SizeBytes,
			"category": file.Category, "mode": file.Mode,
		})
	}
	return out
}

type nativeRepositoryReviewRecordEvidenceResult struct {
	Observations   []repoaudit.Observation
	ReviewEvidence []repoaudit.RepositoryReviewEvidence
	InspectedFiles []repoaudit.FileRef
	CompletedFiles []repoaudit.FileRef
}

func nativeRepositoryReviewRecordEvidence(
	args map[string]any,
	plan repoaudit.Plan,
) (nativeRepositoryReviewRecordEvidenceResult, error) {
	if plan.CampaignID != "" {
		return nativeRepositoryReviewCampaignEvidence(args, plan)
	}
	observations, completed, err := nativeRepositoryReviewLegacyObservations(args, plan)
	return nativeRepositoryReviewRecordEvidenceResult{
		Observations: observations, CompletedFiles: completed,
	}, err
}

func nativeRepositoryReviewObservations(
	args map[string]any,
	plan repoaudit.Plan,
) ([]repoaudit.Observation, []repoaudit.FileRef, error) {
	evidence, err := nativeRepositoryReviewRecordEvidence(args, plan)
	return evidence.Observations, evidence.CompletedFiles, err
}

func nativeRepositoryReviewCampaignEvidence(
	args map[string]any,
	plan repoaudit.Plan,
) (nativeRepositoryReviewRecordEvidenceResult, error) {
	children, err := nativeOptionalMapSlice(args["managed_children"])
	if err != nil {
		return nativeRepositoryReviewRecordEvidenceResult{}, fmt.Errorf("managed children: %w", err)
	}
	// Current runtime children carry a trusted, contiguous execution index and
	// an explicit required/optional classification. Preserve those admission
	// checks before handing the evidence to the shared strict decoder, whose
	// legacy recovery input intentionally accepts older missing metadata.
	for ordinal, child := range children {
		index, indexOK := nativeRepositoryReviewChildIndex(child["index"])
		if !indexOK || index != ordinal+1 {
			return nativeRepositoryReviewRecordEvidenceResult{}, fmt.Errorf(
				"managed child %d has an invalid runtime index", ordinal,
			)
		}
		if _, requiredDeclared := child["required"].(bool); !requiredDeclared {
			return nativeRepositoryReviewRecordEvidenceResult{}, fmt.Errorf(
				"managed child %d has no required classification", ordinal,
			)
		}
	}
	unavailableItems, unavailableErr := nativeOptionalMapSlice(args["unavailable_files"])
	if unavailableErr != nil {
		return nativeRepositoryReviewRecordEvidenceResult{}, fmt.Errorf(
			"unavailable files: %w", unavailableErr,
		)
	}
	unavailableFiles, unavailableErr := nativeRepositoryReviewFiles(unavailableItems)
	if unavailableErr != nil {
		return nativeRepositoryReviewRecordEvidenceResult{}, fmt.Errorf(
			"unavailable files: %w", unavailableErr,
		)
	}
	sort.Slice(unavailableFiles, func(i, j int) bool { return unavailableFiles[i].Path < unavailableFiles[j].Path })
	unsupported := nativeRepositoryReviewUnsupportedFiles(args["managed_children"])
	unsupported = mergeNativeRepositoryUnsupportedFiles(
		unsupported,
		nativeRepositoryReviewUnsupportedScopeFiles(args["unsupported_files"]),
	)
	terminalPaths := make(map[string]struct{}, len(unsupported))
	for _, file := range unsupported {
		terminalPaths[file.Path] = struct{}{}
	}
	combined := make([]map[string]any, 0, len(children)+len(unavailableFiles)*plan.RequiredAssignments)
	for _, child := range children {
		scopeFiles, scopeErr := nativeRepositoryReviewFiles(child["scope"])
		allTerminal := scopeErr == nil && len(scopeFiles) > 0
		for _, file := range scopeFiles {
			if _, terminal := terminalPaths[file.Path]; !terminal {
				allTerminal = false
				break
			}
		}
		if !allTerminal {
			combined = append(combined, child)
		}
	}
	for _, file := range unavailableFiles {
		for slot := 1; slot <= plan.RequiredAssignments; slot++ {
			combined = append(combined, map[string]any{
				"index": len(combined) + 1, "required": true, "valid": false,
				"scope": nativeRepositoryReviewFileMaps([]repoaudit.FileRef{file}),
				"run_error": fmt.Sprintf(
					"aggregate_limit:%s:%03d", file.Path, slot,
				),
			})
		}
	}
	terminalUnsupported := make([]repoaudit.FileRef, 0, len(unsupported))
	for _, file := range unsupported {
		terminalUnsupported = append(terminalUnsupported, file.FileRef)
	}
	decoded, err := DecodeRepositoryReviewManagedEvidence(
		combined,
		plan,
		RepositoryReviewManagedEvidenceOptions{
			TerminalUnsupportedFiles: terminalUnsupported,
			RequiredAssignments:      plan.RequiredAssignments,
		},
	)
	if err != nil {
		return nativeRepositoryReviewRecordEvidenceResult{}, err
	}
	return nativeRepositoryReviewRecordEvidenceResult{
		// Record distinguishes an exact empty projection from absent campaign
		// evidence. Keep every decoded slice explicitly present for zero-progress
		// and all-unsupported batches.
		Observations:   append([]repoaudit.Observation{}, decoded.Observations...),
		ReviewEvidence: append([]repoaudit.RepositoryReviewEvidence{}, decoded.Children...),
		InspectedFiles: append([]repoaudit.FileRef{}, decoded.InspectedFiles...),
		CompletedFiles: append([]repoaudit.FileRef{}, decoded.CompletedFiles...),
	}, nil
}

func nativeRepositoryReviewChildIndex(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, typed > 0
	case int64:
		return int(typed), typed > 0 && int64(int(typed)) == typed
	case float64:
		return int(typed), typed > 0 && typed == math.Trunc(typed) && float64(int(typed)) == typed
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil && parsed > 0 && int64(int(parsed)) == parsed
	default:
		return 0, false
	}
}

func nativeRepositoryReviewLegacyObservations(
	args map[string]any,
	plan repoaudit.Plan,
) ([]repoaudit.Observation, []repoaudit.FileRef, error) {
	children, err := nativeOptionalMapSlice(args["managed_children"])
	if err != nil {
		return nil, nil, fmt.Errorf("managed children: %w", err)
	}
	_, boundedReviewDeclared := args["reviewable_count"]
	if len(children) == 0 && boundedReviewDeclared {
		return nil, []repoaudit.FileRef{}, nil
	}
	observations := make([]repoaudit.Observation, 0, len(children))
	totalCoverage := make(map[string]int)
	successfulCoverage := make(map[string]int)
	fileRefs := make(map[string]repoaudit.FileRef)
	for index, child := range children {
		scopeFiles, scopeErr := nativeRepositoryReviewFiles(child["scope"])
		if scopeErr != nil {
			return nil, nil, fmt.Errorf("managed child %d scope: %w", index, scopeErr)
		}
		required, declared := child["required"].(bool)
		if !declared {
			required = true
		}
		for _, file := range scopeFiles {
			if required {
				totalCoverage[file.Path]++
			}
			fileRefs[file.Path] = file
		}
		structured := nativeMapValue(child["structured"])
		valid, _ := child["valid"].(bool)
		_, runFailed := child["run_error"]
		if structured == nil || !valid || runFailed {
			continue
		}
		completeFiles := nativeRepositoryReviewCompletedScopePaths(child["scope"])
		reviewedPaths, reviewErr := nativeRepositoryReviewAcknowledgedPaths(
			structured, scopeFiles, completeFiles,
		)
		if reviewErr != nil {
			continue
		}
		for _, file := range scopeFiles {
			if required && reviewedPaths[file.Path] {
				successfulCoverage[file.Path]++
			}
		}
		provenance, provenanceErr := nativeRepositoryReviewManagedChildProvenance(child, true)
		if provenanceErr != nil {
			return nil, nil, fmt.Errorf("managed child %d: %w", index, provenanceErr)
		}
		observation, parseErr := nativeRepositoryReviewObservationWithProvenance(
			structured,
			child["scope"],
			provenance,
			strings.TrimSpace(nativeAnyString(child["label"])),
			strings.TrimSpace(nativeAnyString(child["text"])),
		)
		if parseErr != nil {
			return nil, nil, fmt.Errorf("managed child %d: %w", index, parseErr)
		}
		observations = append(observations, observation)
	}
	if len(children) > 0 {
		completed := make([]repoaudit.FileRef, 0, len(fileRefs))
		for path, total := range totalCoverage {
			if total > 0 && successfulCoverage[path] == total {
				completed = append(completed, fileRefs[path])
			}
		}
		sort.Slice(completed, func(i, j int) bool { return completed[i].Path < completed[j].Path })
		return observations, completed, nil
	}
	structured := nativeMapValue(args["review"])
	if structured == nil {
		return nil, nil, errors.New("review.repository record requires structured review evidence")
	}
	model := strings.TrimSpace(nativeString(args, "model"))
	if model == "" {
		model = "default"
	}
	scopeValue := firstNonNil(args["scope"], nativeRepositoryReviewFileMaps(plan.PendingFiles))
	scopeFiles, err := nativeRepositoryReviewFiles(scopeValue)
	if err != nil {
		return nil, nil, err
	}
	reviewedPaths, err := nativeRepositoryReviewAcknowledgedPaths(
		structured, scopeFiles, nativeRepositoryReviewCompletedScopePaths(scopeValue),
	)
	if err != nil {
		return nil, nil, err
	}
	observation, err := nativeRepositoryReviewObservation(
		structured,
		scopeValue,
		model,
		"single review",
		strings.TrimSpace(nativeString(args, "text")),
	)
	if err != nil {
		return nil, nil, err
	}
	completed := make([]repoaudit.FileRef, 0, len(scopeFiles))
	for _, file := range scopeFiles {
		if reviewedPaths[file.Path] {
			completed = append(completed, file)
		}
	}
	return []repoaudit.Observation{observation}, completed, nil
}

func nativeRepositoryReviewAcknowledgedPaths(
	structured map[string]any,
	scopeFiles []repoaudit.FileRef,
	completeFiles map[string]bool,
) (map[string]bool, error) {
	if _, declared := structured["reviewedFiles"]; !declared {
		return nil, errors.New("reviewedFiles is required")
	}
	allowed := make(map[string]bool, len(scopeFiles))
	for _, file := range scopeFiles {
		allowed[file.Path] = completeFiles[file.Path]
	}
	acknowledged := make(map[string]bool, len(scopeFiles))
	for _, raw := range nativeStringSlice(structured["reviewedFiles"]) {
		pathValue := strings.TrimSpace(filepath.ToSlash(raw))
		if !allowed[pathValue] {
			return nil, fmt.Errorf("path %q is not readable assigned evidence", pathValue)
		}
		if acknowledged[pathValue] {
			return nil, fmt.Errorf("path %q is duplicated", pathValue)
		}
		acknowledged[pathValue] = true
	}
	return acknowledged, nil
}

func nativeRepositoryReviewCompletedScopePaths(value any) map[string]bool {
	items, err := nativeMapSlice(value)
	if err != nil {
		return nil
	}
	completed := make(map[string]bool, len(items))
	for _, item := range items {
		pathValue := strings.TrimSpace(nativeAnyString(item["path"]))
		value, declared := item["contentComplete"].(bool)
		completed[pathValue] = !declared || value
	}
	return completed
}

func nativeRepositoryReviewObservation(
	structured map[string]any,
	scopeValue any,
	model string,
	reviewer string,
	raw string,
) (repoaudit.Observation, error) {
	return nativeRepositoryReviewObservationWithProvenance(
		structured,
		scopeValue,
		nativeRepositoryReviewProvenance{Model: model},
		reviewer,
		raw,
	)
}

type nativeRepositoryReviewProvenance struct {
	Model      string
	ModelAlias string
	Account    string
}

func nativeRepositoryReviewManagedChildProvenance(
	child map[string]any,
	allowLegacy bool,
) (nativeRepositoryReviewProvenance, error) {
	modelMeta := nativeMapValue(child["model"])
	modelAlias := strings.TrimSpace(nativeAnyString(modelMeta["selected"]))
	if modelAlias == "" {
		modelAlias = strings.TrimSpace(nativeAnyString(modelMeta["default"]))
	}
	actualValue, actualDeclared := modelMeta["actual"]
	accountValue, accountDeclared := modelMeta["account"]
	model := strings.TrimSpace(nativeAnyString(actualValue))
	account := strings.TrimSpace(nativeAnyString(accountValue))
	if !actualDeclared && !accountDeclared && allowLegacy {
		// Managed outputs written before exact source capture contain one
		// ambiguous model value. Preserve it without claiming exact provenance.
		return nativeRepositoryReviewProvenance{Model: modelAlias}, nil
	}
	if !actualDeclared || !accountDeclared || model == "" || modelAlias == "" || account == "" {
		return nativeRepositoryReviewProvenance{}, errors.New(
			"managed child has incomplete model provenance",
		)
	}
	return nativeRepositoryReviewProvenance{
		Model: model, ModelAlias: modelAlias, Account: account,
	}, nil
}

func nativeRepositoryReviewObservationWithProvenance(
	structured map[string]any,
	scopeValue any,
	provenance nativeRepositoryReviewProvenance,
	reviewer string,
	raw string,
) (repoaudit.Observation, error) {
	if err := nativeValidateRepositoryReviewOutputFields(structured); err != nil {
		return repoaudit.Observation{}, err
	}
	scope, err := nativeRepositoryReviewFiles(scopeValue)
	if err != nil {
		return repoaudit.Observation{}, fmt.Errorf("scope: %w", err)
	}
	findingsRaw, err := nativeOptionalMapSlice(structured["findings"])
	if err != nil {
		return repoaudit.Observation{}, fmt.Errorf("findings: %w", err)
	}
	findings := make([]repoaudit.FindingCandidate, 0, len(findingsRaw))
	completedPaths := nativeRepositoryReviewCompletedScopePaths(scopeValue)
	reviewedPaths := nativeStringSet(nativeStringSlice(structured["reviewedFiles"]))
	_, reviewedFilesDeclared := structured["reviewedFiles"]
	for index, rawFinding := range findingsRaw {
		data, marshalErr := json.Marshal(rawFinding)
		if marshalErr != nil {
			return repoaudit.Observation{}, marshalErr
		}
		var finding repoaudit.FindingCandidate
		if unmarshalErr := json.Unmarshal(data, &finding); unmarshalErr != nil {
			return repoaudit.Observation{}, fmt.Errorf("finding %d: %w", index, unmarshalErr)
		}
		findingPath := strings.TrimSpace(filepath.ToSlash(finding.File))
		if !completedPaths[findingPath] || reviewedFilesDeclared && !reviewedPaths[findingPath] {
			continue
		}
		findings = append(findings, finding)
	}
	digest := sha256.Sum256([]byte(raw))
	return repoaudit.Observation{
		Model: provenance.Model, ModelAlias: provenance.ModelAlias, Account: provenance.Account,
		Reviewer: reviewer, ScopeFiles: scope, Findings: findings,
		Summary:   strings.TrimSpace(nativeAnyString(structured["summary"])),
		RawDigest: "sha256:" + hex.EncodeToString(digest[:]),
	}, nil
}

func nativeValidateRepositoryReviewOutputFields(structured map[string]any) error {
	if err := nativeValidateRepositoryReviewObjectFields(
		structured,
		map[string]struct{}{
			"summary": {}, "reviewedFiles": {}, "findings": {}, "residualRisks": {},
		},
		"output",
	); err != nil {
		return err
	}
	for _, field := range []string{"summary", "reviewedFiles", "findings", "residualRisks"} {
		if _, exists := structured[field]; !exists {
			return fmt.Errorf("repository review output is missing required field %q", field)
		}
	}
	if _, ok := structured["summary"].(string); !ok {
		return errors.New("repository review output summary is invalid")
	}
	for _, field := range []string{"reviewedFiles", "residualRisks"} {
		if err := nativeValidateRepositoryReviewStringArray(structured[field]); err != nil {
			return fmt.Errorf("repository review output %s is invalid: %w", field, err)
		}
	}
	findings, err := nativeMapSlice(structured["findings"])
	if err != nil {
		return fmt.Errorf("findings: %w", err)
	}
	for index, finding := range findings {
		location := fmt.Sprintf("finding %d", index)
		if err := nativeValidateRepositoryReviewObjectFields(
			finding,
			map[string]struct{}{
				"severity": {}, "title": {}, "symbol": {}, "file": {}, "line": {},
				"message": {}, "evidence": {}, "impact": {}, "validation": {},
				"match_hints": {}, "fix_effort": {},
			},
			location,
		); err != nil {
			return err
		}
		for _, field := range []string{
			"severity", "title", "symbol", "file", "message", "evidence", "impact", "validation",
			"match_hints", "fix_effort",
		} {
			if _, exists := finding[field]; !exists {
				return fmt.Errorf("repository review %s is missing required field %q", location, field)
			}
		}
		validation, ok := finding["validation"].(map[string]any)
		if !ok {
			return fmt.Errorf("repository review %s validation is invalid", location)
		}
		if err := nativeValidateRepositoryReviewObjectFields(
			validation,
			map[string]struct{}{"status": {}, "summary": {}, "checks": {}},
			location+" validation",
		); err != nil {
			return err
		}
		for _, field := range []string{"status", "summary", "checks"} {
			if _, exists := validation[field]; !exists {
				return fmt.Errorf(
					"repository review %s validation is missing required field %q",
					location, field,
				)
			}
		}
		if err := nativeValidateGeneratedFindingEnrichment(finding, location); err != nil {
			return err
		}
	}
	return nil
}

func nativeValidateRepositoryReviewStringArray(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var stringsValue []string
	if err := json.Unmarshal(data, &stringsValue); err != nil || stringsValue == nil {
		if err == nil {
			err = errors.New("array is null")
		}
		return err
	}
	return nil
}

func nativeValidateGeneratedFindingEnrichment(finding map[string]any, location string) error {
	matchHints, ok := finding["match_hints"].(map[string]any)
	if !ok {
		return fmt.Errorf("repository review %s match_hints is invalid", location)
	}
	matchHintFields := map[string]struct{}{
		"component": {}, "operation": {}, "failure_mode": {}, "trigger": {},
		"violated_invariant": {}, "observable_outcome": {}, "related_symbols": {},
		"source_anchors": {}, "distinguishing_facts": {},
	}
	if err := nativeValidateRepositoryReviewObjectFields(
		matchHints, matchHintFields, location+" match_hints",
	); err != nil {
		return err
	}
	for field := range matchHintFields {
		if _, exists := matchHints[field]; !exists {
			return fmt.Errorf(
				"repository review %s match_hints is missing required field %q",
				location,
				field,
			)
		}
	}

	fixEffort, ok := finding["fix_effort"].(map[string]any)
	if !ok {
		return fmt.Errorf("repository review %s fix_effort is invalid", location)
	}
	if err := nativeValidateRepositoryReviewObjectFields(
		fixEffort,
		map[string]struct{}{"quick": {}, "quality": {}},
		location+" fix_effort",
	); err != nil {
		return err
	}
	for _, estimateName := range []string{"quick", "quality"} {
		rawEstimate, exists := fixEffort[estimateName]
		if !exists {
			return fmt.Errorf(
				"repository review %s fix_effort is missing required field %q",
				location,
				estimateName,
			)
		}
		estimate, ok := rawEstimate.(map[string]any)
		if !ok {
			return fmt.Errorf(
				"repository review %s fix_effort %s is invalid",
				location,
				estimateName,
			)
		}
		estimateFields := map[string]struct{}{
			"loc_min": {}, "loc_max": {}, "class": {}, "rationale": {},
		}
		if err := nativeValidateRepositoryReviewObjectFields(
			estimate, estimateFields, location+" fix_effort "+estimateName,
		); err != nil {
			return err
		}
		for field := range estimateFields {
			if _, exists := estimate[field]; !exists {
				return fmt.Errorf(
					"repository review %s fix_effort %s is missing required field %q",
					location,
					estimateName,
					field,
				)
			}
		}
	}

	data, err := json.Marshal(finding)
	if err != nil {
		return fmt.Errorf("repository review %s is invalid: %w", location, err)
	}
	var candidate repoaudit.FindingCandidate
	if err := json.Unmarshal(data, &candidate); err != nil {
		return fmt.Errorf("repository review %s is invalid: %w", location, err)
	}
	if err := repoaudit.ValidateGeneratedFindingCandidate(candidate); err != nil {
		return fmt.Errorf("repository review %s is invalid: %w", location, err)
	}
	return nil
}

func nativeValidateRepositoryReviewObjectFields(
	object map[string]any,
	allowed map[string]struct{},
	location string,
) error {
	unknown := make([]string, 0)
	for field := range object {
		if _, ok := allowed[field]; !ok {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf(
		"repository review %s contains field %q outside the diagnosis-only contract",
		location,
		unknown[0],
	)
}

func nativeOptionalMapSlice(value any) ([]map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	return nativeMapSlice(value)
}

func nativeJSONMap(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func nativeInt64Any(values map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch value := values[key].(type) {
		case int:
			return int64(value)
		case int64:
			return value
		case float64:
			return int64(value)
		case json.Number:
			parsed, _ := value.Int64()
			return parsed
		case string:
			parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			return parsed
		}
	}
	return 0
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func nativeWorkflowState(ctx context.Context, args map[string]any, exec ExecutionContext) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	action := strings.ToLower(strings.TrimSpace(nativeString(args, "action")))
	if action == "" {
		action = "get"
	}
	namespace := nativeNamespace(args, exec)
	key := strings.TrimSpace(nativeString(args, "key"))
	switch action {
	case "get":
		if key == "" {
			return nil, fmt.Errorf("key is required")
		}
		value, exists, err := readNativeStateValueContext(ctx, exec, namespace, key)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"namespace": namespace,
			"key":       key,
			"exists":    exists,
			"value":     value,
		}, nil
	case "set":
		if key == "" {
			return nil, fmt.Errorf("key is required")
		}
		value, ok := args["value"]
		if !ok {
			return nil, fmt.Errorf("value is required")
		}
		if err := writeNativeStateValue(ctx, exec, namespace, key, value); err != nil {
			return nil, err
		}
		return map[string]any{
			"namespace": namespace,
			"key":       key,
			"value":     value,
			"updated":   true,
		}, nil
	case "delete":
		if key == "" {
			return nil, fmt.Errorf("key is required")
		}
		deleted, err := deleteNativeStateValue(ctx, exec, namespace, key)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"namespace": namespace,
			"key":       key,
			"deleted":   deleted,
		}, nil
	case "list":
		includeValues := nativeBoolAny(args, "include_values", "includeValues")
		keys, values, err := listNativeStateValuesContext(ctx, exec, namespace, includeValues)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"namespace": namespace,
			"keys":      keys,
			"values":    values,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported workflow.state action %q", action)
	}
}

func nativeWorkflowArtifact(ctx context.Context, args map[string]any, exec ExecutionContext) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	action := strings.ToLower(strings.TrimSpace(nativeString(args, "action")))
	if action == "" {
		if _, ok := args["content"]; ok {
			action = "write"
		} else if _, ok := args["value"]; ok {
			action = "write"
		} else {
			action = "list"
		}
	}
	namespace := nativeNamespace(args, exec)
	runID := strings.TrimSpace(nativeStringAny(args, "run_id", "runId"))
	if runID == "" {
		runID = exec.RunID
	}
	switch action {
	case "write":
		name := strings.TrimSpace(nativeString(args, "name"))
		content, err := nativeArtifactContent(args)
		if err != nil {
			return nil, err
		}
		if name == "" {
			name = defaultArtifactName(args)
		}
		return writeNativeArtifact(exec, namespace, runID, name, content)
	case "read":
		name := strings.TrimSpace(nativeString(args, "name"))
		if name == "" {
			return nil, fmt.Errorf("name is required")
		}
		return readNativeArtifact(exec, namespace, runID, name)
	case "list":
		return listNativeArtifacts(exec, namespace, runID)
	default:
		return nil, fmt.Errorf("unsupported workflow.artifact action %q", action)
	}
}

func nativeGitInventory(ctx context.Context, args map[string]any, exec ExecutionContext) (map[string]any, error) {
	repo, workspace, commit, inventory, inventoryHash, err := nativeGitInventoryData(ctx, args, exec)
	if err != nil {
		return nil, err
	}
	target := normalizeFileTarget(nativeStringDefault(args, "target", "all"))
	files, err := nativeGitInventoryOutputFiles(workspace, inventory, target, true)
	if err != nil {
		return nil, err
	}
	selected := make([]map[string]any, 0, len(files))
	excluded := 0
	for _, file := range files {
		if file["selected"] == true {
			selected = append(selected, file)
		} else {
			excluded++
		}
	}
	output := map[string]any{
		"workingDirectory": repo,
		"workspace":        workspace.Map(),
		"commit":           commit,
		"target":           target,
		"inventoryHash":    inventoryHash,
		"counts": map[string]any{
			"totalFiles":         len(files),
			"totalSelectedFiles": len(selected),
			"filesExcluded":      excluded,
		},
	}
	if nativeBoolAny(args, "compact") {
		compactSelected := make([]map[string]any, 0, len(selected))
		for _, file := range selected {
			ref := cloneMap(file)
			delete(ref, "source")
			delete(ref, "selected")
			compactSelected = append(compactSelected, ref)
		}
		output["selectedFiles"] = compactSelected
	} else {
		output["files"] = files
		output["selectedFiles"] = selected
	}
	return output, nil
}

type nativeGitDiffEntry struct {
	Status       string `json:"status"`
	Path         string `json:"path"`
	PreviousPath string `json:"previousPath,omitempty"`
}

type nativeGitSelectedDiff struct {
	Entry       nativeGitDiffEntry `json:"entry"`
	UnifiedDiff string             `json:"unifiedDiff"`
}

func nativeGitDiff(
	ctx context.Context,
	args map[string]any,
	exec ExecutionContext,
) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo, workspace, err := nativeResolveGitWorkspace(exec, args)
	if err != nil {
		return nil, err
	}
	baseRevision := strings.TrimSpace(nativeStringAny(args, "base", "base_commit", "baseCommit"))
	if baseRevision == "" {
		return nil, fmt.Errorf("base commit is required")
	}
	headRevision := strings.TrimSpace(nativeStringAny(args, "head", "head_commit", "headCommit"))
	mode := strings.ToLower(strings.TrimSpace(nativeString(args, "mode")))
	if mode == "" {
		mode = "direct"
	}
	baseCommit, headCommit, comparisonBaseCommit, err := nativeGitDiffCommits(
		ctx,
		repo,
		workspace,
		baseRevision,
		headRevision,
		mode,
		args,
		exec,
	)
	if err != nil {
		return nil, err
	}
	raw, err := nativeGit(
		ctx,
		repo,
		"diff",
		"--name-status",
		"-z",
		"--find-renames",
		comparisonBaseCommit,
		headCommit,
		"--",
	)
	if err != nil {
		return nil, err
	}
	entries, err := parseNativeGitDiffEntries(raw)
	if err != nil {
		return nil, err
	}
	if len(entries) > maxNativeGitDiffFiles {
		return nil, fmt.Errorf(
			"git diff contains %d paths; maximum is %d",
			len(entries),
			maxNativeGitDiffFiles,
		)
	}

	headInventory, err := nativeCollectInventory(ctx, repo, headCommit)
	if err != nil {
		return nil, err
	}
	headFiles := make(map[string]nativeGitFile, len(headInventory))
	for _, file := range headInventory {
		headFiles[file.Path] = file
	}
	comparisonBaseInventory, err := nativeCollectInventory(ctx, repo, comparisonBaseCommit)
	if err != nil {
		return nil, err
	}
	comparisonBaseFiles := make(map[string]nativeGitFile, len(comparisonBaseInventory))
	for _, file := range comparisonBaseInventory {
		comparisonBaseFiles[file.Path] = file
	}
	target := normalizeFileTarget(nativeStringDefault(args, "target", "code"))
	files := make([]map[string]any, 0, len(entries))
	selected := make([]map[string]any, 0, len(entries))
	selectedDiffs := make([]nativeGitSelectedDiff, 0, len(entries))
	deletedPaths := make([]string, 0)
	totalDiffBytes := 0
	for _, entry := range entries {
		var inventoryFile nativeGitFile
		var exists bool
		if strings.HasPrefix(entry.Status, "D") {
			deletedPaths = append(deletedPaths, entry.Path)
			inventoryFile, exists = comparisonBaseFiles[entry.Path]
		} else {
			inventoryFile, exists = headFiles[entry.Path]
		}
		if !exists {
			return nil, fmt.Errorf(
				"changed path %q is absent from its trusted comparison inventory",
				entry.Path,
			)
		}
		projected, projectErr := nativeGitInventoryOutputFiles(
			workspace,
			[]nativeGitFile{inventoryFile},
			target,
			true,
		)
		if projectErr != nil {
			return nil, projectErr
		}
		file := projected[0]
		delete(file, "source")
		file["changeStatus"] = entry.Status
		if entry.PreviousPath != "" {
			file["previousPath"] = entry.PreviousPath
		}
		if file["selected"] == true {
			diffText, diffErr := nativeGitUnifiedDiff(
				ctx,
				repo,
				comparisonBaseCommit,
				headCommit,
				entry,
			)
			if diffErr != nil {
				return nil, diffErr
			}
			if len(diffText) > maxNativeGitDiffFileBytes {
				return nil, fmt.Errorf(
					"unified diff for %q is %d bytes; per-file maximum is %d",
					entry.Path,
					len(diffText),
					maxNativeGitDiffFileBytes,
				)
			}
			if totalDiffBytes > maxNativeGitDiffAggregateBytes-len(diffText) {
				return nil, fmt.Errorf(
					"selected unified diffs exceed aggregate maximum of %d bytes",
					maxNativeGitDiffAggregateBytes,
				)
			}
			totalDiffBytes += len(diffText)
			file["unifiedDiff"] = diffText
			file["diffBytes"] = len(diffText)
			selected = append(selected, file)
			selectedDiffs = append(selectedDiffs, nativeGitSelectedDiff{
				Entry:       entry,
				UnifiedDiff: diffText,
			})
		}
		files = append(files, file)
	}
	diffHash, err := nativeStableHash(map[string]any{
		"baseCommit":           baseCommit,
		"headCommit":           headCommit,
		"comparisonBaseCommit": comparisonBaseCommit,
		"entries":              entries,
		"selectedDiffs":        selectedDiffs,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"workingDirectory":     repo,
		"workspace":            workspace.Map(),
		"mode":                 mode,
		"baseCommit":           baseCommit,
		"headCommit":           headCommit,
		"comparisonBaseCommit": comparisonBaseCommit,
		"diffHash":             diffHash,
		"diffBytes":            totalDiffBytes,
		"files":                files,
		"selectedFiles":        selected,
		"deletedPaths":         deletedPaths,
		"counts": map[string]any{
			"totalChangedFiles":  len(entries),
			"totalSelectedFiles": len(selected),
			"deletedFiles":       len(deletedPaths),
		},
	}, nil
}

func nativeGitDiffCommits(
	ctx context.Context,
	repo string,
	workspace nativeGitWorkspaceRef,
	baseRevision string,
	headRevision string,
	mode string,
	args map[string]any,
	exec ExecutionContext,
) (string, string, string, error) {
	switch mode {
	case "direct":
		baseCommit, err := nativeResolveCommit(ctx, repo, baseRevision+"^{commit}")
		if err != nil {
			return "", "", "", fmt.Errorf("resolve base commit: %w", err)
		}
		if headRevision == "" {
			headRevision = "HEAD"
		}
		headCommit, err := nativeResolveCommit(ctx, repo, headRevision+"^{commit}")
		if err != nil {
			return "", "", "", fmt.Errorf("resolve head commit: %w", err)
		}
		return baseCommit, headCommit, baseCommit, nil
	case "pull_request":
		if !nativeValidGitObjectID(baseRevision) {
			return "", "", "", fmt.Errorf("pull-request base must be an exact Git object ID")
		}
		if !nativeValidGitObjectID(headRevision) {
			return "", "", "", fmt.Errorf("pull-request head must be an exact Git object ID")
		}
		if workspace.Ref != "" && strings.TrimSpace(workspace.Ref) != headRevision {
			return "", "", "", fmt.Errorf(
				"workspace ref %q does not match pull-request head %q",
				workspace.Ref,
				headRevision,
			)
		}
		headCommit, err := nativeResolveCommit(ctx, repo, headRevision+"^{commit}")
		if err != nil {
			return "", "", "", fmt.Errorf("resolve pull-request head commit: %w", err)
		}
		if headCommit != headRevision {
			return "", "", "", fmt.Errorf(
				"resolved pull-request head %q does not match requested commit %q",
				headCommit,
				headRevision,
			)
		}
		checkedOutHead, err := nativeResolveCommit(ctx, repo, "HEAD^{commit}")
		if err != nil {
			return "", "", "", fmt.Errorf("resolve checked-out workspace head: %w", err)
		}
		if checkedOutHead != headCommit {
			return "", "", "", fmt.Errorf(
				"checked-out workspace head %q does not match pull-request head %q",
				checkedOutHead,
				headCommit,
			)
		}
		baseRepository, err := nativePullRequestBaseRepository(
			exec,
			nativeStringAny(
				args,
				"base_repository",
				"baseRepository",
				"base_repository_url",
				"baseRepositoryURL",
			),
		)
		if err != nil {
			return "", "", "", err
		}
		if fetchErr := nativeFetchExactGitCommit(
			ctx,
			repo,
			baseRepository,
			baseRevision,
			exec,
		); fetchErr != nil {
			return "", "", "", fetchErr
		}
		baseCommit, err := nativeResolveCommit(ctx, repo, baseRevision+"^{commit}")
		if err != nil {
			return "", "", "", fmt.Errorf("resolve fetched pull-request base commit: %w", err)
		}
		if baseCommit != baseRevision {
			return "", "", "", fmt.Errorf(
				"fetched pull-request base %q does not match requested commit %q",
				baseCommit,
				baseRevision,
			)
		}
		mergeBase, err := nativeGit(ctx, repo, "merge-base", baseCommit, headCommit)
		if err != nil {
			return "", "", "", fmt.Errorf("resolve pull-request merge base: %w", err)
		}
		mergeBase = strings.TrimSpace(mergeBase)
		if !nativeValidGitObjectID(mergeBase) || strings.ContainsAny(mergeBase, "\r\n") {
			return "", "", "", fmt.Errorf("pull-request merge base is not one exact Git object ID")
		}
		mergeBase, err = nativeResolveCommit(ctx, repo, mergeBase+"^{commit}")
		if err != nil {
			return "", "", "", fmt.Errorf("resolve pull-request merge-base commit: %w", err)
		}
		return baseCommit, headCommit, mergeBase, nil
	default:
		return "", "", "", fmt.Errorf("unsupported git.diff mode %q", mode)
	}
}

func nativeFetchExactGitCommit(
	ctx context.Context,
	repo string,
	remote string,
	commit string,
	exec ExecutionContext,
) error {
	tempRoot := strings.TrimSpace(exec.WorkspaceDir)
	if tempRoot == "" {
		tempRoot = "."
	}
	validationRepo, mkdirErr := os.MkdirTemp(tempRoot, ".picoclaw-git-fetch-")
	if mkdirErr != nil {
		return fmt.Errorf("create pull-request base validation repository: %w", mkdirErr)
	}
	defer func() {
		_ = os.RemoveAll(validationRepo)
	}()
	if _, err := nativeGit(ctx, validationRepo, "init", "--quiet", "--bare"); err != nil {
		return fmt.Errorf("initialize pull-request base validation repository: %w", err)
	}
	if _, err := nativeGit(
		ctx,
		validationRepo,
		"fetch",
		"--quiet",
		"--no-tags",
		"--no-write-fetch-head",
		"--no-recurse-submodules",
		"--no-auto-maintenance",
		"--",
		remote,
		commit,
	); err != nil {
		return fmt.Errorf("fetch exact pull-request base commit: %w", err)
	}
	validatedCommit, err := nativeResolveCommit(ctx, validationRepo, commit+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve validated pull-request base commit: %w", err)
	}
	if validatedCommit != commit {
		return fmt.Errorf(
			"validated pull-request base %q does not match requested commit %q",
			validatedCommit,
			commit,
		)
	}
	const validationRef = "refs/picoclaw/validated-base"
	if _, err := nativeGit(ctx, validationRepo, "update-ref", validationRef, validatedCommit); err != nil {
		return fmt.Errorf("pin validated pull-request base commit: %w", err)
	}
	if _, err := nativeGit(
		ctx,
		repo,
		"fetch",
		"--quiet",
		"--no-tags",
		"--no-write-fetch-head",
		"--no-recurse-submodules",
		"--no-auto-maintenance",
		"--",
		validationRepo,
		validationRef,
	); err != nil {
		return fmt.Errorf("import validated pull-request base commit: %w", err)
	}
	return nil
}

func nativeValidGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if char >= '0' && char <= '9' || char >= 'a' && char <= 'f' {
			continue
		}
		return false
	}
	return true
}

func nativePullRequestBaseRepository(exec ExecutionContext, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("base repository is required in pull-request mode")
	}
	if len(value) > maxNativeGitRemoteBytes || !utf8.ValidString(value) {
		return "", fmt.Errorf("base repository is invalid")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("base repository is invalid")
	}
	if parsed.Scheme == "" {
		repo, resolveErr := nativeResolveRepo(exec, value)
		if resolveErr != nil {
			return "", fmt.Errorf("resolve local base repository: %w", resolveErr)
		}
		return repo, nil
	}
	if parsed.Scheme != "https" ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.Opaque != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		strings.Trim(parsed.EscapedPath(), "/") == "" {
		return "", fmt.Errorf(
			"base repository must be an HTTPS URL or a local repository inside the workflow workspace",
		)
	}
	return value, nil
}

func nativeGitUnifiedDiff(
	ctx context.Context,
	repo string,
	baseCommit string,
	headCommit string,
	entry nativeGitDiffEntry,
) (string, error) {
	args := []string{
		"--literal-pathspecs",
		"-c", "core.quotePath=true",
		"-c", "diff.noprefix=false",
		"-c", "diff.mnemonicPrefix=false",
		"diff",
		"--no-ext-diff",
		"--no-textconv",
		"--no-color",
		"--find-renames",
		"--full-index",
		"--diff-algorithm=histogram",
		fmt.Sprintf("--unified=%d", nativeGitDiffContextLines),
		fmt.Sprintf("--inter-hunk-context=%d", nativeGitDiffInterHunkContextLines),
		"--src-prefix=a/",
		"--dst-prefix=b/",
		baseCommit,
		headCommit,
		"--",
	}
	if entry.PreviousPath != "" {
		args = append(args, entry.PreviousPath)
	}
	args = append(args, entry.Path)
	diffText, exceeded, err := nativeGitBoundedOutput(
		ctx,
		repo,
		maxNativeGitDiffFileBytes,
		args...,
	)
	if err != nil {
		return "", fmt.Errorf("generate unified diff for %q: %w", entry.Path, err)
	}
	if exceeded {
		return "", fmt.Errorf(
			"unified diff for %q exceeds per-file maximum of %d bytes",
			entry.Path,
			maxNativeGitDiffFileBytes,
		)
	}
	if diffText == "" {
		return "", fmt.Errorf("unified diff for %q is empty", entry.Path)
	}
	if !utf8.ValidString(diffText) {
		return "", fmt.Errorf("unified diff for %q is not valid UTF-8", entry.Path)
	}
	return diffText, nil
}

func parseNativeGitDiffEntries(raw string) ([]nativeGitDiffEntry, error) {
	if raw == "" {
		return nil, nil
	}
	fields := strings.Split(raw, "\x00")
	if fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	entries := make([]nativeGitDiffEntry, 0, len(fields)/2)
	for index := 0; index < len(fields); {
		status := fields[index]
		index++
		if status == "" {
			return nil, fmt.Errorf("git diff contains an empty status")
		}
		statusKind := status[0]
		switch statusKind {
		case 'A', 'M', 'D', 'T', 'U', 'X', 'B':
			if index >= len(fields) {
				return nil, fmt.Errorf("git diff status %q is missing its path", status)
			}
			filePath, err := nativeCleanRepoFilePath(fields[index])
			if err != nil {
				return nil, err
			}
			index++
			entries = append(entries, nativeGitDiffEntry{
				Status: status,
				Path:   filePath,
			})
		case 'R', 'C':
			if index+1 >= len(fields) {
				return nil, fmt.Errorf("git diff status %q is missing rename paths", status)
			}
			previousPath, err := nativeCleanRepoFilePath(fields[index])
			if err != nil {
				return nil, err
			}
			filePath, err := nativeCleanRepoFilePath(fields[index+1])
			if err != nil {
				return nil, err
			}
			index += 2
			entries = append(entries, nativeGitDiffEntry{
				Status:       status,
				Path:         filePath,
				PreviousPath: previousPath,
			})
		default:
			return nil, fmt.Errorf("git diff contains unsupported status %q", status)
		}
	}
	return entries, nil
}

func nativeGitInventoryData(
	ctx context.Context,
	args map[string]any,
	exec ExecutionContext,
) (string, nativeGitWorkspaceRef, string, []nativeGitFile, string, error) {
	repo, workspace, err := nativeResolveGitWorkspace(exec, args)
	if err != nil {
		return "", nativeGitWorkspaceRef{}, "", nil, "", err
	}
	commit, err := nativeResolveCommit(ctx, repo, nativeString(args, "commit"))
	if err != nil {
		return "", nativeGitWorkspaceRef{}, "", nil, "", err
	}
	inventory, err := nativeCollectInventory(ctx, repo, commit)
	if err != nil {
		return "", nativeGitWorkspaceRef{}, "", nil, "", err
	}
	hash, err := nativeStableHash(inventory)
	if err != nil {
		return "", nativeGitWorkspaceRef{}, "", nil, "", err
	}
	return repo, workspace, commit, inventory, hash, nil
}

func nativeResolveGitWorkspace(exec ExecutionContext, args map[string]any) (string, nativeGitWorkspaceRef, error) {
	if workspaceValue, ok := args["workspace"]; ok && workspaceValue != nil {
		workspaceMap := nativeMapValue(workspaceValue)
		workspace := nativeGitWorkspaceRefFromMap(workspaceMap)
		if workspace.Path == "" && workspaceMap == nil {
			workspace.Path = strings.TrimSpace(nativeAnyString(workspaceValue))
		}
		if strings.TrimSpace(workspace.Path) == "" {
			return "", nativeGitWorkspaceRef{}, fmt.Errorf("workspace.path is required")
		}
		repo, err := nativeResolveRepo(exec, workspace.Path)
		if err != nil {
			return "", nativeGitWorkspaceRef{}, err
		}
		workspace.Path = repo
		return repo, workspace, nil
	}
	repo, err := nativeResolveRepo(exec, nativeStringAny(args, "working_directory", "workingDirectory"))
	if err != nil {
		return "", nativeGitWorkspaceRef{}, err
	}
	return repo, nativeGitWorkspaceRef{Path: repo}, nil
}

func nativeGitWorkspaceRefFromMap(value map[string]any) nativeGitWorkspaceRef {
	if value == nil {
		return nativeGitWorkspaceRef{}
	}
	return nativeGitWorkspaceRef{
		ID:          strings.TrimSpace(nativeAnyString(value["id"])),
		RepoID:      strings.TrimSpace(nativeAnyString(value["repo_id"])),
		RemoteURL:   strings.TrimSpace(nativeAnyString(value["remote_url"])),
		UpstreamURL: strings.TrimSpace(nativeAnyString(value["upstream_url"])),
		Ref:         strings.TrimSpace(nativeAnyString(value["ref"])),
		Path:        strings.TrimSpace(nativeAnyString(value["path"])),
	}
}

func (w nativeGitWorkspaceRef) Map() map[string]any {
	out := make(map[string]any)
	if strings.TrimSpace(w.ID) != "" {
		out["id"] = strings.TrimSpace(w.ID)
	}
	if strings.TrimSpace(w.RepoID) != "" {
		out["repo_id"] = strings.TrimSpace(w.RepoID)
	}
	if strings.TrimSpace(w.RemoteURL) != "" {
		out["remote_url"] = strings.TrimSpace(w.RemoteURL)
	}
	if strings.TrimSpace(w.UpstreamURL) != "" {
		out["upstream_url"] = strings.TrimSpace(w.UpstreamURL)
	}
	if strings.TrimSpace(w.Ref) != "" {
		out["ref"] = strings.TrimSpace(w.Ref)
	}
	if strings.TrimSpace(w.Path) != "" {
		out["path"] = strings.TrimSpace(w.Path)
	}
	return out
}

func nativeGitFileSource(workspace nativeGitWorkspaceRef, filePath string) (map[string]any, error) {
	cleanPath, err := nativeCleanRepoFilePath(filePath)
	if err != nil {
		return nil, err
	}
	source := map[string]any{
		"type":     "workspace_file",
		"filePath": cleanPath,
	}
	if strings.TrimSpace(workspace.ID) != "" {
		source["workspaceId"] = strings.TrimSpace(workspace.ID)
	}
	workspacePath := strings.TrimSpace(workspace.Path)
	if workspacePath != "" {
		source["workspacePath"] = workspacePath
		source["path"] = filepath.Join(workspacePath, filepath.FromSlash(cleanPath))
	}
	return source, nil
}

func nativeCleanRepoFilePath(value string) (string, error) {
	clean := path.Clean(strings.TrimSpace(filepath.ToSlash(value)))
	if clean == "." || clean == "" {
		return "", fmt.Errorf("file path is required")
	}
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("file path %q must stay inside repository", value)
	}
	return clean, nil
}

func nativeResolveRepo(exec ExecutionContext, value string) (string, error) {
	workspace := strings.TrimSpace(exec.WorkspaceDir)
	if workspace == "" {
		workspace = "."
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = "."
	}
	var candidate string
	if filepath.IsAbs(value) {
		candidate = value
	} else {
		candidate = filepath.Join(workspaceAbs, filepath.FromSlash(value))
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	if err := nativeEnsureInside(workspaceAbs, candidateAbs); err != nil {
		return "", fmt.Errorf("working_directory must stay inside workflow workspace: %w", err)
	}
	if _, err := os.Stat(filepath.Join(candidateAbs, ".git")); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("working_directory is not a git repo: %s", candidateAbs)
		}
		return "", err
	}
	return candidateAbs, nil
}

func nativeEnsureInside(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rootEval, err := evalWorkflowPathPrefix(rootAbs)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if rootEval == "" {
		rootEval = rootAbs
	}
	targetEval, err := evalWorkflowPathPrefix(targetAbs)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if targetEval == "" {
		targetEval = targetAbs
	}
	rel, err := filepath.Rel(rootEval, targetEval)
	if err != nil {
		return err
	}
	if rel == "." || rel == "" {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("path escapes workspace")
	}
	return nil
}

func nativeResolveCommit(ctx context.Context, repo, commit string) (string, error) {
	commit = strings.TrimSpace(commit)
	if commit != "" {
		out, err := nativeGit(ctx, repo, "rev-parse", "--verify", commit)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(out), nil
	}
	for _, candidate := range []string{"HEAD", "main", "master"} {
		out, err := nativeGit(ctx, repo, "rev-parse", "--verify", candidate)
		if err != nil {
			continue
		}
		if strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out), nil
		}
	}
	return "", fmt.Errorf("could not resolve default commit HEAD/main/master")
}

func nativeCollectInventory(ctx context.Context, repo, commit string) ([]nativeGitFile, error) {
	output, exceeded, err := nativeGitBoundedOutput(
		ctx,
		repo,
		maxNativeGitInventoryBytes,
		"ls-tree", "-r", "-l", "-z", "--full-tree", commit,
	)
	if err != nil {
		return nil, err
	}
	if exceeded {
		return nil, fmt.Errorf("git inventory exceeds %d bytes", maxNativeGitInventoryBytes)
	}
	return nativeParseInventory(output)
}

func nativeCollectInventoryPaths(
	ctx context.Context,
	repo string,
	commit string,
	paths []string,
) ([]nativeGitFile, error) {
	if len(paths) == 0 || len(paths) > 128 {
		return nil, errors.New("exact Git inventory path batch is invalid")
	}
	args := []string{"ls-tree", "-r", "-l", "-z", "--full-tree", commit, "--"}
	seen := make(map[string]struct{}, len(paths))
	for _, filePath := range paths {
		clean, err := nativeCleanRepoFilePath(filePath)
		if err != nil || clean != filePath {
			return nil, errors.New("exact Git inventory contains an invalid path")
		}
		if _, duplicate := seen[clean]; duplicate {
			return nil, errors.New("exact Git inventory contains a duplicate path")
		}
		seen[clean] = struct{}{}
		args = append(args, ":(literal)"+clean)
	}
	output, exceeded, err := nativeGitBoundedOutput(
		ctx,
		repo,
		min(maxNativeGitInventoryBytes, len(paths)*(4096+256)),
		args...,
	)
	if err != nil {
		return nil, err
	}
	if exceeded {
		return nil, errors.New("exact Git inventory output exceeds its bound")
	}
	return nativeParseInventory(output)
}

func nativeParseInventory(output string) ([]nativeGitFile, error) {
	inventory := make([]nativeGitFile, 0)
	for _, line := range strings.Split(output, "\x00") {
		if strings.TrimSpace(line) == "" || !strings.Contains(line, "\t") {
			continue
		}
		head, filePath, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		parts := strings.Fields(head)
		if len(parts) < 4 || parts[1] != "blob" {
			continue
		}
		size := int64(0)
		if parts[3] != "-" {
			parsed, err := strconv.ParseInt(parts[3], 10, 64)
			if err != nil {
				return nil, err
			}
			size = parsed
		}
		inventory = append(inventory, nativeGitFile{
			Path:      filePath,
			Mode:      parts[0],
			BlobHash:  parts[2],
			SizeBytes: size,
		})
		if len(inventory) > maxNativeGitInventoryFiles {
			return nil, fmt.Errorf("git inventory exceeds %d files", maxNativeGitInventoryFiles)
		}
	}
	sort.Slice(inventory, func(i, j int) bool {
		return inventory[i].Path < inventory[j].Path
	})
	return inventory, nil
}

func nativeGit(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := osexec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", nativeGitError{err: err, output: strings.TrimSpace(string(out)), args: args}
	}
	return string(out), nil
}

type nativeBoundedBuffer struct {
	data     []byte
	limit    int
	exceeded bool
}

func (buffer *nativeBoundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - len(buffer.data)
	if len(value) > remaining {
		buffer.exceeded = true
	}
	if remaining > 0 {
		if remaining > len(value) {
			remaining = len(value)
		}
		buffer.data = append(buffer.data, value[:remaining]...)
	}
	return len(value), nil
}

func nativeGitBoundedOutput(
	ctx context.Context,
	repo string,
	maxStdoutBytes int,
	args ...string,
) (string, bool, error) {
	stdout := nativeBoundedBuffer{limit: maxStdoutBytes}
	stderr := nativeBoundedBuffer{limit: maxNativeGitErrorOutputBytes}
	cmd := osexec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_PAGER=cat")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		output := strings.TrimSpace(string(stderr.data))
		if output == "" {
			output = strings.TrimSpace(string(stdout.data))
		}
		return "", stdout.exceeded, nativeGitError{err: err, output: output, args: args}
	}
	return string(stdout.data), stdout.exceeded, nil
}

type nativeGitBlobRequest struct {
	ObjectID     string
	ExpectedSize int64
	RetainBytes  int64
}

// nativeGitBatchReadBlobs reads exact object contents through one long-lived
// cat-file process. The callback receives at most RetainBytes for each object;
// the rest is streamed to io.Discard, so both process count and resident memory
// stay bounded for large repositories.
func nativeGitBatchReadBlobs(
	ctx context.Context,
	repo string,
	requests []nativeGitBlobRequest,
	consume func(int, []byte) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(requests) == 0 {
		return nil
	}
	if len(requests) > maxNativeGitInventoryFiles || consume == nil {
		return errors.New("invalid immutable Git blob batch")
	}
	var input strings.Builder
	input.Grow(len(requests) * 65)
	for _, request := range requests {
		if !nativeValidGitObjectID(request.ObjectID) || request.ExpectedSize < 0 ||
			request.RetainBytes < 0 || request.RetainBytes > request.ExpectedSize ||
			request.ExpectedSize > maxNativeGitContentFileBytes {
			return errors.New("invalid immutable Git blob batch request")
		}
		input.WriteString(request.ObjectID)
		input.WriteByte('\n')
	}

	stderr := nativeBoundedBuffer{limit: maxNativeGitErrorOutputBytes}
	cmd := osexec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_PAGER=cat")
	cmd.Stdin = strings.NewReader(input.String())
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return nativeGitError{
			err: err, output: strings.TrimSpace(string(stderr.data)), args: []string{"cat-file", "--batch"},
		}
	}
	fail := func(cause error) error {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if output := strings.TrimSpace(string(stderr.data)); output != "" {
			return fmt.Errorf("git cat-file --batch: %w: %s", cause, output)
		}
		return fmt.Errorf("git cat-file --batch: %w", cause)
	}
	reader := bufio.NewReaderSize(stdout, 64<<10)
	for index, request := range requests {
		header, headerErr := reader.ReadString('\n')
		if headerErr != nil {
			return fail(fmt.Errorf("read immutable Git blob header: %w", headerErr))
		}
		fields := strings.Fields(strings.TrimSuffix(header, "\n"))
		if len(fields) != 3 || !strings.EqualFold(fields[0], request.ObjectID) || fields[1] != "blob" {
			return fail(fmt.Errorf("immutable Git object %s is missing or not a blob", request.ObjectID))
		}
		size, parseErr := strconv.ParseInt(fields[2], 10, 64)
		if parseErr != nil || size != request.ExpectedSize {
			return fail(fmt.Errorf("immutable Git blob %s size mismatch with inventory", request.ObjectID))
		}
		retained := make([]byte, int(request.RetainBytes))
		if _, contentErr := io.ReadFull(reader, retained); contentErr != nil {
			return fail(fmt.Errorf("read immutable Git blob %s: %w", request.ObjectID, contentErr))
		}
		if remaining := size - request.RetainBytes; remaining > 0 {
			if _, drainErr := io.CopyN(io.Discard, reader, remaining); drainErr != nil {
				return fail(fmt.Errorf("drain immutable Git blob %s: %w", request.ObjectID, drainErr))
			}
		}
		delimiter, delimiterErr := reader.ReadByte()
		if delimiterErr != nil || delimiter != '\n' {
			return fail(fmt.Errorf("immutable Git blob %s has an invalid batch delimiter", request.ObjectID))
		}
		if consumeErr := consume(index, retained); consumeErr != nil {
			return fail(consumeErr)
		}
	}
	if err := cmd.Wait(); err != nil {
		return nativeGitError{
			err: err, output: strings.TrimSpace(string(stderr.data)), args: []string{"cat-file", "--batch"},
		}
	}
	return nil
}

type nativeGitError struct {
	err    error
	output string
	args   []string
}

func (e nativeGitError) Error() string {
	if e.output != "" {
		return e.output
	}
	return fmt.Sprintf("git %s failed: %v", strings.Join(e.args, " "), e.err)
}

func nativeGitInventoryOutputFiles(
	workspace nativeGitWorkspaceRef,
	inventory []nativeGitFile,
	target string,
	includeModes bool,
) ([]map[string]any, error) {
	files := make([]map[string]any, 0, len(inventory))
	for _, entry := range inventory {
		category := nativeCategorizePath(entry.Path)
		selected := nativeTargetSelects(target, category)
		source, err := nativeGitFileSource(workspace, entry.Path)
		if err != nil {
			return nil, err
		}
		file := map[string]any{
			"path":      entry.Path,
			"fileHash":  entry.BlobHash,
			"category":  category,
			"selected":  selected,
			"sizeBytes": entry.SizeBytes,
			"source":    source,
		}
		if includeModes {
			file["mode"] = entry.Mode
		}
		files = append(files, file)
	}
	return files, nil
}

func nativeGitFilter(ctx context.Context, args map[string]any, exec ExecutionContext) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repo, workspace, err := nativeResolveGitWorkspace(exec, args)
	if err != nil {
		return nil, err
	}
	target := normalizeFileTarget(nativeStringDefault(args, "target", "code"))
	files, err := nativeMapSlice(args["files"])
	if err != nil {
		return nil, fmt.Errorf("files: %w", err)
	}
	for _, file := range files {
		if _, pathErr := nativeCleanRepoFilePath(nativeAnyString(file["path"])); pathErr != nil {
			return nil, pathErr
		}
	}
	commit, err := nativeResolveCommit(ctx, repo, nativeString(args, "commit"))
	if err != nil {
		return nil, fmt.Errorf("resolve inventory commit: %w", err)
	}
	trustedInventory, err := nativeCollectInventory(ctx, repo, commit)
	if err != nil {
		return nil, fmt.Errorf("load trusted inventory: %w", err)
	}
	trustedFiles := make(map[string]nativeGitFile, len(trustedInventory))
	for _, file := range trustedInventory {
		trustedFiles[file.Path] = file
	}
	filter := nativeMapValue(args["filter"])
	includeGlobs := nativeStringSliceAny(
		filter,
		"include_globs",
		"includeGlobs",
		"include",
		"includes",
	)
	excludeGlobs := nativeStringSliceAny(
		filter,
		"exclude_globs",
		"excludeGlobs",
		"exclude",
		"excludes",
	)
	selectedPaths := nativeStringSet(nativeStringSliceAny(
		filter,
		"selected_paths",
		"selectedPaths",
		"paths",
	))

	filtered := make([]map[string]any, 0, len(files))
	selected := make([]map[string]any, 0, len(files))
	for _, original := range files {
		file := cloneMap(original)
		filePath := strings.TrimSpace(nativeAnyString(file["path"]))
		category := strings.TrimSpace(nativeAnyString(file["category"]))
		if category == "" {
			category = nativeCategorizePath(filePath)
			file["category"] = category
		}
		cleanPath, err := nativeCleanRepoFilePath(filePath)
		if err != nil {
			return nil, err
		}
		filePath = cleanPath
		file["path"] = filePath
		trusted, ok := trustedFiles[filePath]
		if !ok || strings.TrimSpace(nativeAnyString(file["fileHash"])) != trusted.BlobHash ||
			nativeInt64Any(file, "sizeBytes", "size_bytes") != trusted.SizeBytes {
			return nil, fmt.Errorf("file %q does not match immutable inventory commit %s", filePath, commit)
		}
		file["fileHash"] = trusted.BlobHash
		file["sizeBytes"] = trusted.SizeBytes
		baseSelected := nativeTargetSelects(target, category)
		matchesInclude := len(includeGlobs) == 0 && len(selectedPaths) == 0
		if !matchesInclude {
			matchesInclude = selectedPaths[nativeNormalizeRepoPath(filePath)] ||
				nativeAnyGlobMatches(includeGlobs, filePath)
		}
		matchesExclude := nativeAnyGlobMatches(excludeGlobs, filePath)
		isSelected := baseSelected && matchesInclude && !matchesExclude
		file["selected"] = isSelected
		source, err := nativeGitFileSource(workspace, filePath)
		if err != nil {
			return nil, err
		}
		file["source"] = source
		filtered = append(filtered, file)
		if isSelected {
			selected = append(selected, file)
		}
	}
	return map[string]any{
		"workingDirectory": repo,
		"workspace":        workspace.Map(),
		"commit":           commit,
		"target":           target,
		"inventoryHash":    nativeStringAny(args, "inventory_hash", "inventoryHash"),
		"filter": map[string]any{
			"includeGlobs":  includeGlobs,
			"excludeGlobs":  excludeGlobs,
			"selectedPaths": nativeSortedSetValues(selectedPaths),
			"rationale":     nativeAnyString(filter["rationale"]),
		},
		"files":         filtered,
		"selectedFiles": selected,
		"counts": map[string]any{
			"totalFiles":         len(filtered),
			"totalSelectedFiles": len(selected),
			"filesExcluded":      len(filtered) - len(selected),
		},
	}, nil
}

func hydrateImmutableGitScope(
	ctx context.Context,
	scope any,
	args map[string]any,
	exec ExecutionContext,
) (any, error) {
	items, wrapper, err := nativeScopeItems(scope)
	if err != nil {
		return nil, err
	}
	perFileLimit := int(nativeInt64Any(args, "max_content_bytes", "maxContentBytes"))
	if perFileLimit <= 0 || perFileLimit > maxNativeGitContentFileBytes {
		perFileLimit = maxNativeGitContentFileBytes
	}
	aggregateLimit := int(nativeInt64Any(args, "max_total_content_bytes", "maxTotalContentBytes"))
	if aggregateLimit <= 0 || aggregateLimit > maxNativeGitContentAggregateBytes {
		aggregateLimit = maxNativeGitContentAggregateBytes
	}
	type pendingBlob struct {
		repo  string
		index int
		blob  nativeGitBlobRequest
	}
	used := 0
	hydrated := make([]any, 0, len(items))
	pending := make([]pendingBlob, 0, len(items))
	for index, raw := range items {
		file, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("immutable Git scope item %d must be an object", index)
		}
		file = cloneMap(file)
		delete(file, "content")
		delete(file, "contentBytes")
		delete(file, "contentComplete")
		delete(file, "contentUnavailable")
		if strings.TrimSpace(nativeAnyString(file["category"])) == "binary" {
			file["contentComplete"] = false
			file["contentUnavailable"] = "binary"
			hydrated = append(hydrated, file)
			continue
		}
		source := nativeMapValue(file["source"])
		workspacePath := strings.TrimSpace(nativeAnyString(source["workspacePath"]))
		if workspacePath == "" {
			return nil, fmt.Errorf("immutable Git scope item %d has no workspace source", index)
		}
		repo, resolveErr := nativeResolveRepo(exec, workspacePath)
		if resolveErr != nil {
			return nil, fmt.Errorf("immutable Git scope item %d: %w", index, resolveErr)
		}
		delete(file, "source")
		size := nativeInt64Any(file, "sizeBytes", "size_bytes")
		if size < 0 {
			return nil, fmt.Errorf("immutable Git scope item %d has invalid size", index)
		}
		if size > int64(perFileLimit) {
			file["contentComplete"] = false
			file["contentUnavailable"] = "file_too_large"
			hydrated = append(hydrated, file)
			continue
		}
		hash := strings.TrimSpace(nativeAnyString(file["fileHash"]))
		if !nativeValidGitObjectID(hash) {
			return nil, fmt.Errorf("immutable Git scope file %q has invalid blob hash", nativeAnyString(file["path"]))
		}
		hydrated = append(hydrated, file)
		pending = append(pending, pendingBlob{
			repo: repo, index: len(hydrated) - 1,
			blob: nativeGitBlobRequest{ObjectID: hash, ExpectedSize: size, RetainBytes: size},
		})
	}
	for start := 0; start < len(pending); {
		end := start + 1
		for end < len(pending) && pending[end].repo == pending[start].repo {
			end++
		}
		requests := make([]nativeGitBlobRequest, end-start)
		for index := start; index < end; index++ {
			requests[index-start] = pending[index].blob
		}
		if err := nativeGitBatchReadBlobs(
			ctx,
			pending[start].repo,
			requests,
			func(requestIndex int, content []byte) error {
				entry := pending[start+requestIndex]
				file := hydrated[entry.index].(map[string]any)
				if len(content) > aggregateLimit-used {
					file["contentComplete"] = false
					file["contentUnavailable"] = "aggregate_limit"
					return nil
				}
				textContent := string(content)
				if !nativeReviewText(textContent) {
					file["contentComplete"] = false
					file["contentUnavailable"] = "binary"
					return nil
				}
				promptBytes := nativeReviewEncodedContentBytes(textContent)
				if promptBytes > perFileLimit {
					file["contentComplete"] = false
					file["contentUnavailable"] = "file_too_large"
					return nil
				}
				file["content"] = textContent
				file["contentBytes"] = len(content)
				file["contentPromptBytes"] = promptBytes
				file["contentComplete"] = true
				used += len(content)
				return nil
			},
		); err != nil {
			return nil, fmt.Errorf("read immutable blob batch: %w", err)
		}
		start = end
	}
	if wrapper != nil {
		out := cloneMap(wrapper)
		out["items"] = hydrated
		return out, nil
	}
	if _, preserveMapSlice := scope.([]map[string]any); preserveMapSlice {
		out := make([]map[string]any, 0, len(hydrated))
		for _, item := range hydrated {
			out = append(out, item.(map[string]any))
		}
		return out, nil
	}
	return hydrated, nil
}

func nativeReviewText(content string) bool {
	if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
		return false
	}
	for _, character := range content {
		if character < 0x20 && character != '\n' && character != '\r' && character != '\t' {
			return false
		}
	}
	return true
}

func nativeReviewEncodedContentBytes(content string) int {
	encoded, err := json.Marshal(content)
	if err != nil || len(encoded) < 2 {
		return len(content)
	}
	return len(encoded) - 2
}

func nativeScopeItems(scope any) ([]any, map[string]any, error) {
	switch typed := scope.(type) {
	case []any:
		return append([]any(nil), typed...), nil, nil
	case []map[string]any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return items, nil, nil
	case map[string]any:
		items, ok := typed["items"]
		if !ok {
			return []any{typed}, nil, nil
		}
		slice, _, err := nativeScopeItems(items)
		return slice, typed, err
	default:
		return nil, nil, errors.New("immutable Git scope must be an object or array")
	}
}

func nativeCategorizePath(filePath string) string {
	low := strings.ToLower(filepath.ToSlash(filePath))
	for _, pattern := range nativeExcludePatterns {
		if nativeGlobMatches(pattern, low) {
			return "excluded"
		}
	}
	ext := path.Ext(low)
	if _, binary := nativeBinaryExtensions[ext]; binary {
		return "binary"
	}
	parts := strings.FieldsFunc(low, func(r rune) bool {
		return r == '/' || r == '_' || r == '.' || r == '-'
	})
	for _, marker := range nativeTestMarkers {
		for _, part := range parts {
			if part == marker {
				return "tests"
			}
		}
	}
	baseWithoutExt := strings.TrimSuffix(path.Base(low), path.Ext(low))
	if strings.HasSuffix(baseWithoutExt, "_test") || strings.HasSuffix(baseWithoutExt, ".test") ||
		strings.HasSuffix(baseWithoutExt, "_spec") || strings.HasSuffix(baseWithoutExt, ".spec") {
		return "tests"
	}
	base := path.Base(low)
	if base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") ||
		base == "makefile" || base == "rakefile" || base == "gemfile" ||
		base == "procfile" || base == "justfile" || base == "cmakelists.txt" {
		return "code"
	}
	if _, ok := nativeCodeExtensions[ext]; ok {
		return "code"
	}
	if _, ok := nativeConfigExtensions[ext]; ok && !strings.HasSuffix(low, ".lock") {
		return "code"
	}
	return "other"
}

func normalizeFileTarget(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "code", "tests", "all":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "code"
	}
}

func nativeTargetSelects(target, category string) bool {
	switch target {
	case "all":
		return category != "excluded"
	case "code":
		return category == "code"
	case "tests":
		return category == "tests"
	default:
		return false
	}
}

func nativeAnyGlobMatches(patterns []string, filePath string) bool {
	for _, pattern := range patterns {
		if nativeGlobMatches(pattern, filePath) {
			return true
		}
	}
	return false
}

func nativeGlobMatches(pattern, filePath string) bool {
	pattern = nativeNormalizeRepoPath(pattern)
	filePath = nativeNormalizeRepoPath(filePath)
	if pattern == "" || filePath == "" {
		return false
	}
	if !strings.Contains(pattern, "/") {
		base := path.Base(filePath)
		if ok, _ := path.Match(pattern, base); ok {
			return true
		}
		return filePath == pattern || strings.Contains(filePath, "/"+pattern+"/")
	}
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	re, err := regexp.Compile(nativeGlobRegexp(pattern))
	if err != nil {
		return false
	}
	return re.MatchString(filePath)
}

func nativeGlobRegexp(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
				continue
			}
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	b.WriteString("$")
	return b.String()
}

func nativeNormalizeRepoPath(value string) string {
	value = strings.TrimSpace(filepath.ToSlash(value))
	value = strings.TrimPrefix(value, "./")
	return strings.ToLower(value)
}

func readNativeStateValue(exec ExecutionContext, namespace, key string) (any, bool, error) {
	return readNativeStateValueContext(context.Background(), exec, namespace, key)
}

func readNativeStateValueContext(ctx context.Context, exec ExecutionContext, namespace, key string) (any, bool, error) {
	db, release, err := borrowWorkflowDatabase(ctx, nativeWorkspace(exec))
	if err != nil {
		return nil, false, err
	}
	defer release()
	var data []byte
	err = db.QueryRowContext(ctx, `SELECT value_json FROM workflow_native_state
		WHERE namespace_id=? AND key_id=? AND key_text=?`, safeStorageSegment(namespace),
		safeStorageSegment(key), strings.TrimSpace(key)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var value any
	if err := decodeWorkflowJSON(data, &value); err != nil {
		return nil, false, err
	}
	return value, true, nil
}

//nolint:govet // Transaction-local errors stay scoped to their exact statement.
func writeNativeStateValue(ctx context.Context, exec ExecutionContext, namespace, key string, value any) error {
	key = strings.TrimSpace(key)
	data, err := encodeWorkflowJSON(value, maximumWorkflowNativeValueBytes)
	if err != nil {
		return err
	}
	if data == nil {
		data = []byte("null")
	}
	db, release, err := borrowWorkflowDatabase(ctx, nativeWorkspace(exec))
	if err != nil {
		return err
	}
	defer release()
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		now := time.Now().UTC()
		seconds, nanos, err := workflowTimestamp(now)
		if err != nil {
			return err
		}
		var totalBytes, previousBytes int64
		if err := conn.QueryRowContext(ctx, `SELECT
			(SELECT COALESCE(SUM(length(value_json)),0) FROM workflow_native_state),
			COALESCE((SELECT length(value_json) FROM workflow_native_state
			 WHERE namespace_id=? AND key_id=?),0)`, safeStorageSegment(namespace),
			safeStorageSegment(key)).Scan(&totalBytes, &previousBytes); err != nil {
			return err
		}
		if int64(len(data)) > maximumWorkflowNativeTotalBytes-(totalBytes-previousBytes) {
			return fmt.Errorf("workflow native state exceeds its aggregate limit")
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO workflow_native_state
			(namespace_id,key_id,key_text,value_json,updated_at_seconds,updated_at_nanosecond,version)
			VALUES(?,?,?,?,?,?,1) ON CONFLICT(namespace_id,key_id) DO UPDATE SET
			key_text=excluded.key_text,value_json=excluded.value_json,
			updated_at_seconds=excluded.updated_at_seconds,
			updated_at_nanosecond=excluded.updated_at_nanosecond,
			version=workflow_native_state.version+1`, safeStorageSegment(namespace),
			safeStorageSegment(key), key, data, seconds, nanos)
		return err
	})
}

//nolint:govet // Transaction-local errors stay scoped to their exact statement.
func deleteNativeStateValue(ctx context.Context, exec ExecutionContext, namespace, key string) (bool, error) {
	db, release, err := borrowWorkflowDatabase(ctx, nativeWorkspace(exec))
	if err != nil {
		return false, err
	}
	defer release()
	var deleted bool
	err = sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `DELETE FROM workflow_native_state
			WHERE namespace_id=? AND key_id=? AND key_text=?`, safeStorageSegment(namespace),
			safeStorageSegment(key), strings.TrimSpace(key))
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		deleted = count != 0
		return err
	})
	return deleted, err
}

func listNativeStateValues(
	exec ExecutionContext,
	namespace string,
	includeValues bool,
) ([]string, map[string]any, error) {
	return listNativeStateValuesContext(context.Background(), exec, namespace, includeValues)
}

func listNativeStateValuesContext(
	ctx context.Context,
	exec ExecutionContext,
	namespace string,
	includeValues bool,
) ([]string, map[string]any, error) {
	db, release, err := borrowWorkflowDatabase(ctx, nativeWorkspace(exec))
	if err != nil {
		return nil, nil, err
	}
	defer release()
	rows, err := db.QueryContext(ctx, `SELECT key_text,value_json FROM workflow_native_state
		WHERE namespace_id=? ORDER BY key_text,key_id`, safeStorageSegment(namespace))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	keys := make([]string, 0)
	values := map[string]any(nil)
	if includeValues {
		values = make(map[string]any)
	}
	for rows.Next() {
		var key string
		var data []byte
		if err := rows.Scan(&key, &data); err != nil {
			return nil, nil, err
		}
		keys = append(keys, key)
		if includeValues {
			var value any
			if err := decodeWorkflowJSON(data, &value); err != nil {
				return nil, nil, err
			}
			values[key] = value
		}
	}
	return keys, values, rows.Err()
}

func nativeStatePath(exec ExecutionContext, namespace, key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("state key is required")
	}
	return nativeConfinedPath(
		exec,
		workflowStateDir,
		safeStorageSegment(namespace),
		safeStorageSegment(key)+".json",
	)
}

func writeNativeArtifact(exec ExecutionContext, namespace, runID, name string, content string) (map[string]any, error) {
	artifactPath, relPath, err := nativeArtifactPath(exec, namespace, runID, name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		return nil, err
	}
	data := []byte(content)
	if err := os.WriteFile(artifactPath, data, 0o600); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return map[string]any{
		"namespace":    namespace,
		"runId":        runID,
		"name":         filepath.ToSlash(name),
		"relativePath": relPath,
		"path":         artifactPath,
		"bytes":        len(data),
		"sha256":       hex.EncodeToString(sum[:]),
	}, nil
}

func readNativeArtifact(exec ExecutionContext, namespace, runID, name string) (map[string]any, error) {
	artifactPath, relPath, err := nativeArtifactPath(exec, namespace, runID, name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	out := map[string]any{
		"namespace":    namespace,
		"runId":        runID,
		"name":         filepath.ToSlash(name),
		"relativePath": relPath,
		"path":         artifactPath,
		"bytes":        len(data),
		"sha256":       hex.EncodeToString(sum[:]),
		"content":      string(data),
	}
	var value any
	if err := json.Unmarshal(data, &value); err == nil {
		out["value"] = value
	}
	return out, nil
}

func listNativeArtifacts(exec ExecutionContext, namespace, runID string) (map[string]any, error) {
	parts := []string{workflowArtifactsDir, safeStorageSegment(namespace)}
	if strings.TrimSpace(runID) != "" {
		parts = append(parts, safeStorageSegment(runID))
	}
	root, err := nativeConfinedPath(exec, parts...)
	if err != nil {
		return nil, err
	}
	artifacts := make([]map[string]any, 0)
	err = filepath.WalkDir(root, func(item string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		itemRel, relErr := filepath.Rel(nativeWorkspace(exec), item)
		if relErr != nil {
			return relErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		artifacts = append(artifacts, map[string]any{
			"relativePath": filepath.ToSlash(itemRel),
			"path":         item,
			"bytes":        info.Size(),
		})
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{"namespace": namespace, "runId": runID, "artifacts": artifacts}, nil
		}
		return nil, err
	}
	sort.Slice(artifacts, func(i, j int) bool {
		return fmt.Sprint(artifacts[i]["relativePath"]) < fmt.Sprint(artifacts[j]["relativePath"])
	})
	return map[string]any{"namespace": namespace, "runId": runID, "artifacts": artifacts}, nil
}

func nativeArtifactPath(exec ExecutionContext, namespace, runID, name string) (string, string, error) {
	if strings.TrimSpace(runID) == "" {
		runID = "manual"
	}
	relName, err := safeArtifactRel(name)
	if err != nil {
		return "", "", err
	}
	rel := filepath.Join(
		workflowArtifactsDir,
		safeStorageSegment(namespace),
		safeStorageSegment(runID),
		filepath.FromSlash(relName),
	)
	parts := make([]string, 0, 3+strings.Count(relName, "/")+1)
	parts = append(parts, workflowArtifactsDir, safeStorageSegment(namespace), safeStorageSegment(runID))
	parts = append(parts, strings.Split(relName, "/")...)
	target, err := nativeConfinedPath(exec, parts...)
	if err != nil {
		return "", "", err
	}
	return target, filepath.ToSlash(rel), nil
}

func nativeConfinedPath(exec ExecutionContext, parts ...string) (string, error) {
	workspace := nativeWorkspace(exec)
	target := filepath.Join(append([]string{workspace}, parts...)...)
	if len(parts) > 0 && parts[0] == workflowStateDir {
		var err error
		if len(parts) == 1 {
			target, err = resolveWorkflowInternalRoot(workspace, workflowStateDir)
		} else {
			target, err = resolveWorkflowInternalPath(
				workspace,
				workflowStateDir,
				parts[1:]...,
			)
		}
		if err != nil {
			return "", fmt.Errorf(
				"path must stay inside workflow workspace storage root: %w",
				err,
			)
		}
	}
	if err := nativeEnsureInside(workspace, target); err != nil {
		return "", fmt.Errorf("path must stay inside workflow workspace: %w", err)
	}
	if err := nativeEnsureInsideStorageRoot(workspace, target, parts...); err != nil {
		return "", fmt.Errorf("path must stay inside workflow workspace storage root: %w", err)
	}
	return target, nil
}

func nativeEnsureInsideStorageRoot(workspace, target string, parts ...string) error {
	if len(parts) < 2 {
		return nil
	}
	if parts[0] != workflowStateDir && parts[0] != workflowArtifactsDir {
		return nil
	}
	workspaceEval, err := evalWorkflowPathPrefix(workspace)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if workspaceEval == "" {
		workspaceEval = filepath.Clean(workspace)
	}
	root := filepath.Join(workspace, parts[0], parts[1])
	rootEval, err := evalWorkflowPathPrefix(root)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if rootEval == "" {
		rootEval = filepath.Clean(root)
	}
	intendedRoot := filepath.Join(workspaceEval, parts[0], parts[1])
	relRoot, err := filepath.Rel(filepath.Clean(intendedRoot), filepath.Clean(rootEval))
	if err != nil {
		return err
	}
	if relRoot != "." && relRoot != "" {
		return fmt.Errorf("storage root is a symlink")
	}
	if err := nativeEnsureInside(rootEval, target); err != nil {
		return fmt.Errorf("path escapes storage root: %w", err)
	}
	return nil
}

func safeArtifactRel(name string) (string, error) {
	clean := path.Clean(strings.TrimSpace(filepath.ToSlash(name)))
	if clean == "." || clean == "" {
		return "", fmt.Errorf("artifact name is required")
	}
	if path.IsAbs(clean) {
		return "", fmt.Errorf("artifact name must be relative")
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("artifact name contains unsafe path component")
		}
	}
	return clean, nil
}

func nativeArtifactContent(args map[string]any) (string, error) {
	if value, ok := args["content"]; ok {
		return fmt.Sprint(value), nil
	}
	value, ok := args["value"]
	if !ok {
		return "", fmt.Errorf("content or value is required")
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func defaultArtifactName(args map[string]any) string {
	format := strings.ToLower(strings.TrimSpace(nativeString(args, "format")))
	ext := ".txt"
	if format == "json" {
		ext = ".json"
	} else if format == "markdown" || format == "md" {
		ext = ".md"
	}
	return fmt.Sprintf("artifact-%d%s", time.Now().UTC().UnixNano(), ext)
}

func nativeNamespace(args map[string]any, exec ExecutionContext) string {
	namespace := strings.TrimSpace(nativeString(args, "namespace"))
	if namespace != "" {
		return namespace
	}
	if strings.TrimSpace(exec.WorkflowRef) != "" {
		return strings.TrimSpace(exec.WorkflowRef)
	}
	return "default"
}

func nativeWorkspace(exec ExecutionContext) string {
	workspace := strings.TrimSpace(exec.WorkspaceDir)
	if workspace == "" {
		workspace = "."
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return workspace
	}
	return abs
}

func safeStorageSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "default"
	}
	clean := safeStorageSegmentPattern.ReplaceAllString(value, "-")
	clean = strings.Trim(clean, ".-_")
	if clean == "" {
		clean = "value"
	}
	if len(clean) > 80 {
		clean = clean[:80]
	}
	sum := sha256.Sum256([]byte(value))
	return clean + "-" + hex.EncodeToString(sum[:])[:12]
}

func nativeString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func nativeAnyString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func nativeStringAny(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(nativeString(args, key)); value != "" {
			return value
		}
	}
	return ""
}

func nativeStringDefault(args map[string]any, key, fallback string) string {
	value := strings.TrimSpace(nativeString(args, key))
	if value == "" {
		return fallback
	}
	return value
}

func nativeMapValue(value any) map[string]any {
	switch v := value.(type) {
	case nil:
		return nil
	case map[string]any:
		return v
	case map[string]string:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = item
		}
		return out
	case string:
		var out map[string]any
		if err := json.Unmarshal([]byte(v), &out); err == nil {
			return out
		}
	}
	return nil
}

func nativeMapSlice(value any) ([]map[string]any, error) {
	switch v := value.(type) {
	case nil:
		return nil, fmt.Errorf("required")
	case []map[string]any:
		return v, nil
	case []any:
		out := make([]map[string]any, 0, len(v))
		for i, item := range v {
			obj, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("item %d must be an object", i)
			}
			out = append(out, obj)
		}
		return out, nil
	case string:
		var out []map[string]any
		if err := json.Unmarshal([]byte(v), &out); err != nil {
			return nil, err
		}
		return out, nil
	default:
		return nil, fmt.Errorf("must be an array of objects")
	}
}

func nativeStringSliceAny(args map[string]any, keys ...string) []string {
	for _, key := range keys {
		values := nativeStringSlice(args[key])
		if len(values) > 0 {
			return values
		}
	}
	return nil
}

func nativeStringSlice(value any) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case []string:
		return nativeCleanStringSlice(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text := strings.TrimSpace(nativeAnyString(item)); text != "" {
				out = append(out, text)
			}
		}
		return nativeCleanStringSlice(out)
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return nil
		}
		var decoded []string
		if err := json.Unmarshal([]byte(text), &decoded); err == nil {
			return nativeCleanStringSlice(decoded)
		}
		return nativeCleanStringSlice(strings.Split(text, ","))
	default:
		return nil
	}
}

func nativeCleanStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func nativeStringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		if value = nativeNormalizeRepoPath(value); value != "" {
			out[value] = true
		}
	}
	return out
}

func nativeSortedSetValues(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func nativeBool(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	switch value := args[key].(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func nativeBoolAny(args map[string]any, keys ...string) bool {
	for _, key := range keys {
		if nativeBool(args, key) {
			return true
		}
	}
	return false
}

func nativeInt(args map[string]any, key string, fallback int) int {
	if args == nil {
		return fallback
	}
	switch value := args[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func nativeStableHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
