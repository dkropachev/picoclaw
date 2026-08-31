package workflows

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/reposcope"
)

const repositoryEvaluationCatalogMaxFileBytes = 512 << 10

const repositoryEvaluationAICandidateLimit = 4096

const (
	repositoryEvaluationScopeWarnings     = 32
	repositoryEvaluationScopeWarningBytes = 2048
	repositoryEvaluationNativeWarning     = "Native scope validation"
)

const (
	repositoryEvaluationMaxClaimsPerChild     = 32
	repositoryEvaluationMaxClaimsPerBatch     = 1024
	repositoryEvaluationMaxClaimsPerCandidate = 512
	repositoryEvaluationClaimPathBytes        = 4096
	repositoryEvaluationClaimTitleBytes       = 512
	repositoryEvaluationClaimTextBytes        = 2048
)

func nativeRepositoryEvaluationCorpus(
	ctx context.Context,
	args map[string]any,
	exec ExecutionContext,
) (map[string]any, error) {
	switch strings.ToLower(strings.TrimSpace(nativeString(args, "action"))) {
	case "catalog":
		return nativeRepositoryEvaluationCatalog(ctx, args, exec)
	case "select":
		return nativeRepositoryEvaluationSelection(args)
	case "subset":
		return nativeRepositoryEvaluationSubset(args)
	case "validate":
		return nativeRepositoryEvaluationValidate(ctx, args, exec)
	case "filter":
		return nativeRepositoryEvaluationFilter(args)
	case "blind":
		return nativeRepositoryEvaluationBlind(args["managed_children"], args["candidate_models"])
	default:
		return nil, fmt.Errorf("unsupported evaluation.corpus action %q", nativeString(args, "action"))
	}
}

// nativeRepositoryEvaluationValidate rebinds only the controller-persisted
// corpus slice needed by one evaluation batch. The candidate ID protects every
// classification field, while an exact tree lookup protects path/blob/mode/
// size. Commit and inventory identities must match the durable manifest passed
// by the trusted built-in workflow; no repository-wide catalog is rebuilt.
func nativeRepositoryEvaluationValidate(
	ctx context.Context,
	args map[string]any,
	exec ExecutionContext,
) (map[string]any, error) {
	repo, workspace, err := nativeResolveGitWorkspace(exec, args)
	if err != nil {
		return nil, err
	}
	expectedCommit := strings.ToLower(strings.TrimSpace(nativeStringAny(
		args, "commit", "commit_sha", "commitSha",
	)))
	expectedInventory := strings.TrimSpace(nativeStringAny(args, "inventory_hash", "inventoryHash"))
	if !nativeValidGitObjectID(expectedCommit) || len(expectedInventory) != sha256.Size*2 ||
		strings.ToLower(expectedInventory) != expectedInventory {
		return nil, errors.New("repository evaluation batch requires exact commit and inventory identities")
	}
	if _, decodeErr := hex.DecodeString(expectedInventory); decodeErr != nil {
		return nil, errors.New("repository evaluation batch requires exact commit and inventory identities")
	}
	commit, err := nativeResolveCommit(ctx, repo, expectedCommit)
	if err != nil {
		return nil, err
	}
	commit = strings.ToLower(strings.TrimSpace(commit))
	if commit != expectedCommit {
		return nil, errors.New("repository evaluation batch resolved a different commit")
	}
	var candidates []reposcope.Candidate
	if decodeErr := nativeRepositoryEvaluationDecode(
		firstNonNil(args["candidates"], args["selected_candidates"], args["selectedCandidates"]),
		&candidates,
	); decodeErr != nil || len(candidates) == 0 || len(candidates) > 128 {
		return nil, errors.New("repository evaluation batch candidates are invalid")
	}
	hardScope, err := nativeRepositoryEvaluationScope(args["scope"])
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(candidates))
	seenIDs := make(map[string]struct{}, len(candidates))
	seenPaths := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidateErr := reposcope.ValidateCandidate(candidate); candidateErr != nil {
			return nil, candidateErr
		}
		if strings.ToLower(candidate.CommitID) != commit || candidate.InventoryID != expectedInventory ||
			candidate.Size > repositoryEvaluationCatalogMaxFileBytes {
			return nil, reposcope.ErrInvalidCandidate
		}
		if _, duplicate := seenIDs[candidate.ID]; duplicate {
			return nil, reposcope.ErrDuplicateCandidate
		}
		if _, duplicate := seenPaths[candidate.Path]; duplicate {
			return nil, reposcope.ErrInvalidCandidate
		}
		seenIDs[candidate.ID] = struct{}{}
		seenPaths[candidate.Path] = struct{}{}
		paths = append(paths, candidate.Path)
	}
	exactFiles, err := nativeCollectInventoryPaths(ctx, repo, commit, paths)
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]nativeGitFile, len(exactFiles))
	for _, file := range exactFiles {
		if _, duplicate := byPath[file.Path]; duplicate {
			return nil, errors.New("repository evaluation exact tree lookup returned a duplicate path")
		}
		byPath[file.Path] = file
	}
	requests := make([]nativeGitBlobRequest, 0, len(candidates))
	for _, candidate := range candidates {
		file, found := byPath[candidate.Path]
		if !found || nativeRepositoryEvaluationFileKind(file.Mode) != reposcope.FileKindRegular ||
			!strings.EqualFold(file.BlobHash, candidate.BlobID) || file.SizeBytes != candidate.Size {
			return nil, fmt.Errorf("repository evaluation candidate %q does not match exact commit", candidate.Path)
		}
		requests = append(requests, nativeGitBlobRequest{
			ObjectID: file.BlobHash, ExpectedSize: file.SizeBytes,
			RetainBytes: min(file.SizeBytes, int64(reposcope.MaxSampleBytes)),
		})
	}
	if err := nativeGitBatchReadBlobs(ctx, repo, requests, func(index int, sample []byte) error {
		candidate := candidates[index]
		file := byPath[candidate.Path]
		rebuilt, _, _ := reposcope.BuildCandidates(
			reposcope.Inventory{
				CommitID: commit,
				ID:       expectedInventory,
				Files: []reposcope.FileMetadata{{
					Path: candidate.Path, BlobID: file.BlobHash, Size: file.SizeBytes,
					Kind: nativeRepositoryEvaluationFileKind(file.Mode), Sample: sample,
				}},
			},
			hardScope,
			reposcope.BuildOptions{MaxFileBytes: repositoryEvaluationCatalogMaxFileBytes},
		)
		if len(rebuilt) != 1 || rebuilt[0] != candidate {
			return reposcope.ErrInvalidCandidate
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("reclassify exact repository evaluation batch: %w", err)
	}
	selectedFiles := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		// Candidate validation and the exact-tree pass above prove both lookups.
		file := byPath[candidate.Path]
		source, _ := nativeGitFileSource(workspace, candidate.Path)
		selectedFiles = append(selectedFiles, map[string]any{
			"candidateId": candidate.ID,
			"path":        candidate.Path, "fileHash": file.BlobHash, "sizeBytes": file.SizeBytes,
			"category": nativeCategorizePath(candidate.Path), "selected": true, "source": source,
		})
	}
	return map[string]any{
		"commit": commit, "inventoryHash": expectedInventory,
		"candidates":    nativeRepositoryEvaluationCandidateMaps(candidates),
		"selectedPaths": append([]string(nil), paths...), "selectedFiles": selectedFiles,
		"counts": map[string]any{"totalSelectedFiles": len(selectedFiles)},
	}, nil
}

func nativeRepositoryEvaluationSubset(args map[string]any) (map[string]any, error) {
	var candidates []reposcope.Candidate
	if err := nativeRepositoryEvaluationDecode(args["candidates"], &candidates); err != nil {
		return nil, fmt.Errorf("repository evaluation candidates: %w", err)
	}
	byID := make(map[string]reposcope.Candidate, len(candidates))
	for _, candidate := range candidates {
		if err := reposcope.ValidateCandidate(candidate); err != nil {
			return nil, err
		}
		if _, duplicate := byID[candidate.ID]; duplicate {
			return nil, reposcope.ErrInvalidCandidate
		}
		byID[candidate.ID] = candidate
	}
	ids := nativeStringSliceAny(
		args,
		"candidate_ids", "candidateIds", "selected_candidate_ids", "selectedCandidateIds",
	)
	if len(ids) == 0 || len(ids) > len(candidates) {
		return nil, reposcope.ErrInvalidPolicy
	}
	selected := make([]reposcope.Candidate, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return nil, reposcope.ErrDuplicateCandidate
		}
		candidate, exists := byID[id]
		if !exists {
			return nil, reposcope.ErrUnknownCandidate
		}
		seen[id] = struct{}{}
		selected = append(selected, candidate)
	}
	return nativeRepositoryEvaluationSelectedOutput(selected, ids, nil), nil
}

func nativeRepositoryEvaluationBlind(value any, universeValues ...any) (map[string]any, error) {
	children, err := nativeMapSlice(value)
	if err != nil {
		return nil, fmt.Errorf("repository evaluation candidate outputs: %w", err)
	}
	aliases := make([]string, 0)
	allowed := make(map[string]struct{})
	present := make(map[string]struct{})
	if len(universeValues) > 0 {
		aliases = nativeRepositoryEvaluationAliasUniverse(universeValues[0])
		for _, alias := range aliases {
			allowed[alias] = struct{}{}
		}
	}
	configuredUniverse := len(aliases) > 0
	for _, child := range children {
		alias, _ := nativeRepositoryEvaluationModelIdentity(child)
		if alias == "" {
			return nil, errors.New("repository evaluation candidate output has no selected model alias")
		}
		if _, exists := allowed[alias]; !exists && configuredUniverse {
			return nil, errors.New("repository evaluation candidate alias is outside the configured universe")
		} else if !exists {
			allowed[alias] = struct{}{}
			aliases = append(aliases, alias)
		}
		present[alias] = struct{}{}
	}
	sort.Strings(aliases)
	ids := make(map[string]string, len(aliases))
	mapping := make([]map[string]any, 0, len(aliases))
	for index, alias := range aliases {
		id := fmt.Sprintf("candidate-%03d", index+1)
		ids[alias] = id
		if _, exists := present[alias]; exists {
			mapping = append(mapping, map[string]any{"candidateId": id, "modelAlias": alias})
		}
	}
	blinded := make([]map[string]any, 0, len(children))
	ledger := make([]map[string]any, 0)
	claimCounts := make(map[string]int, len(aliases))
	for _, child := range children {
		var clone map[string]any
		if err := nativeRepositoryEvaluationDecode(child, &clone); err != nil {
			return nil, err
		}
		alias, concrete := nativeRepositoryEvaluationModelIdentity(clone)
		delete(clone, "model")
		clone["candidateId"] = ids[alias]
		if concrete != "" {
			clone["concreteModelDigest"] = nativeRepositoryEvaluationOpaqueDigest(concrete)
		}
		claims, claimErr := nativeRepositoryEvaluationBlindClaims(clone, ids[alias], claimCounts)
		if claimErr != nil {
			return nil, claimErr
		}
		ledger = append(ledger, claims...)
		if len(ledger) > repositoryEvaluationMaxClaimsPerBatch {
			return nil, errors.New("repository evaluation candidate claim ledger exceeds its batch limit")
		}
		var usage []map[string]any
		if usageErr := nativeRepositoryEvaluationDecode(clone["usage"], &usage); usageErr == nil {
			for _, item := range usage {
				delete(item, "model")
				delete(item, "reviewer")
			}
			clone["usage"] = usage
		}
		blinded = append(blinded, clone)
	}
	return map[string]any{"blinded": blinded, "mapping": mapping, "ledger": ledger}, nil
}

func nativeRepositoryEvaluationBlindClaims(
	child map[string]any,
	candidateID string,
	claimCounts map[string]int,
) ([]map[string]any, error) {
	valid, _ := child["valid"].(bool)
	if !valid {
		return []map[string]any{}, nil
	}
	structured := nativeMapValue(child["structured"])
	if structured == nil {
		return nil, errors.New("valid repository evaluation candidate output has no structured diagnosis")
	}
	for key := range structured {
		if key != "summary" && key != "claims" && key != "residualRisks" {
			return nil, errors.New("repository evaluation candidate output contains a prohibited diagnosis field")
		}
	}
	claims, err := nativeMapSlice(structured["claims"])
	if err != nil {
		return nil, fmt.Errorf("repository evaluation candidate claims: %w", err)
	}
	if len(claims) > repositoryEvaluationMaxClaimsPerChild {
		return nil, errors.New("repository evaluation candidate output exceeds its claim limit")
	}
	allowedPaths := make(map[string]struct{})
	if scope, scopeErr := nativeMapSlice(child["scope"]); scopeErr == nil {
		for _, item := range scope {
			if pathValue := strings.TrimSpace(nativeAnyString(item["path"])); pathValue != "" {
				allowedPaths[pathValue] = struct{}{}
			}
		}
	}
	ledger := make([]map[string]any, 0, len(claims))
	for _, claim := range claims {
		for key := range claim {
			if key != "path" && key != "title" && key != "evidence" && key != "impact" {
				return nil, errors.New("repository evaluation candidate claim contains a prohibited field")
			}
		}
		pathValue := strings.TrimSpace(nativeAnyString(claim["path"]))
		title := strings.TrimSpace(nativeAnyString(claim["title"]))
		evidence := strings.TrimSpace(nativeAnyString(claim["evidence"]))
		impact := strings.TrimSpace(nativeAnyString(claim["impact"]))
		if _, ok := allowedPaths[pathValue]; !ok ||
			!nativeRepositoryEvaluationBoundedText(pathValue, repositoryEvaluationClaimPathBytes) ||
			!nativeRepositoryEvaluationBoundedText(title, repositoryEvaluationClaimTitleBytes) ||
			!nativeRepositoryEvaluationBoundedText(evidence, repositoryEvaluationClaimTextBytes) ||
			!nativeRepositoryEvaluationBoundedText(impact, repositoryEvaluationClaimTextBytes) {
			return nil, errors.New("repository evaluation candidate claim is outside its exact diagnosis contract")
		}
		claimCounts[candidateID]++
		if claimCounts[candidateID] > repositoryEvaluationMaxClaimsPerCandidate {
			return nil, errors.New("repository evaluation candidate exceeds its aggregate claim limit")
		}
		claimID := fmt.Sprintf("claim-%s-%04d", strings.TrimPrefix(candidateID, "candidate-"), claimCounts[candidateID])
		claim["claimId"] = claimID
		ledger = append(ledger, map[string]any{
			"candidateId": candidateID,
			"claimId":     claimID,
			"path":        pathValue,
			"title":       title,
			"evidence":    evidence,
			"impact":      impact,
		})
	}
	structured["claims"] = claims
	child["structured"] = structured
	return ledger, nil
}

func nativeRepositoryEvaluationBoundedText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum
}

func nativeRepositoryEvaluationAliasUniverse(value any) []string {
	var values []string
	if raw, ok := value.(string); ok {
		values = strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == '\n' })
	} else {
		_ = nativeRepositoryEvaluationDecode(value, &values)
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func nativeRepositoryEvaluationModelIdentity(child map[string]any) (string, string) {
	model := nativeMapValue(child["model"])
	concrete := strings.TrimSpace(nativeAnyString(model["selected"]))
	alias := strings.TrimSpace(nativeAnyString(model["requested"]))
	if alias == "" {
		alias = concrete
	}
	return alias, concrete
}

func nativeRepositoryEvaluationOpaqueDigest(value string) string {
	digest, _ := nativeStableHash("repository-evaluation-concrete-model-v1\x00" + value)
	return "sha256:" + digest
}

func nativeRepositoryEvaluationCatalog(
	ctx context.Context,
	args map[string]any,
	exec ExecutionContext,
) (map[string]any, error) {
	repo, _, err := nativeResolveGitWorkspace(exec, args)
	if err != nil {
		return nil, err
	}
	commit, err := nativeResolveCommit(
		ctx,
		repo,
		nativeStringAny(args, "commit", "commit_sha", "commitSha"),
	)
	if err != nil {
		return nil, err
	}
	inventory, err := nativeCollectInventory(ctx, repo, commit)
	if err != nil {
		return nil, err
	}
	// nativeGitFile contains only JSON scalar fields, so hashing cannot fail.
	inventoryHash, _ := nativeStableHash(inventory)
	if expected := strings.TrimSpace(
		nativeStringAny(args, "inventory_hash", "inventoryHash"),
	); expected != "" &&
		expected != inventoryHash {
		return nil, errors.New("repository evaluation inventory hash does not match the exact commit")
	}

	scope, err := nativeRepositoryEvaluationScope(args["scope"])
	if err != nil {
		return nil, err
	}
	if nativeBoolAny(args, "promote_hotpath", "promoteHotpath") &&
		nativeRepositoryEvaluationHasCodeType(scope.CodeTypes, reposcope.CodeTypeHotpath) &&
		!nativeRepositoryEvaluationHasCodeType(scope.CodeTypes, reposcope.CodeTypeCode) {
		scope.CodeTypes = append(scope.CodeTypes, reposcope.CodeTypeCode)
		sort.Slice(scope.CodeTypes, func(i, j int) bool { return scope.CodeTypes[i] < scope.CodeTypes[j] })
	}

	// Classify one immutable inventory entry at a time while one cat-file
	// process streams bounded samples for every potentially eligible blob. This
	// avoids both O(files) subprocesses and retaining O(files*sample-size) bytes.
	requests := make([]nativeGitBlobRequest, 0, len(inventory))
	requestIndexes := make([]int, 0, len(inventory))
	seenInventoryPaths := make(map[string]struct{}, len(inventory))
	for index, file := range inventory {
		if _, duplicate := seenInventoryPaths[file.Path]; duplicate {
			return nil, fmt.Errorf("%w: duplicate path %q", reposcope.ErrInvalidInventory, file.Path)
		}
		seenInventoryPaths[file.Path] = struct{}{}
		metadata := reposcope.FileMetadata{
			Path: file.Path, BlobID: file.BlobHash, Size: file.SizeBytes,
			Kind: nativeRepositoryEvaluationFileKind(file.Mode),
		}
		if reposcope.CandidateNeedsSample(
			metadata,
			reposcope.BuildOptions{MaxFileBytes: repositoryEvaluationCatalogMaxFileBytes},
		) {
			requests = append(requests, nativeGitBlobRequest{
				ObjectID: file.BlobHash, ExpectedSize: file.SizeBytes,
				RetainBytes: min(file.SizeBytes, int64(reposcope.MaxSampleBytes)),
			})
			requestIndexes = append(requestIndexes, index)
		}
	}
	candidates := make([]reposcope.Candidate, 0, min(len(inventory), repositoryEvaluationAICandidateLimit))
	rejections := make([]reposcope.Rejection, 0)
	processed := make([]bool, len(inventory))
	classify := func(index int, sample []byte) {
		file := inventory[index]
		built, rejected, _ := reposcope.BuildCandidates(
			reposcope.Inventory{CommitID: commit, ID: inventoryHash, Files: []reposcope.FileMetadata{{
				Path: file.Path, BlobID: file.BlobHash, Size: file.SizeBytes,
				Kind: nativeRepositoryEvaluationFileKind(file.Mode), Sample: sample,
			}}},
			scope,
			reposcope.BuildOptions{MaxFileBytes: repositoryEvaluationCatalogMaxFileBytes},
		)
		processed[index] = true
		candidates = append(candidates, built...)
		rejections = append(rejections, rejected...)
	}
	if err := nativeGitBatchReadBlobs(ctx, repo, requests, func(requestIndex int, sample []byte) error {
		classify(requestIndexes[requestIndex], sample)
		return nil
	}); err != nil {
		return nil, err
	}
	for index := range inventory {
		if !processed[index] {
			classify(index, nil)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Language != candidates[j].Language {
			return candidates[i].Language < candidates[j].Language
		}
		return candidates[i].Path < candidates[j].Path
	})
	sort.Slice(rejections, func(i, j int) bool { return rejections[i].Path < rejections[j].Path })
	outputCandidates := nativeRepositoryEvaluationAICandidates(candidates, repositoryEvaluationAICandidateLimit)
	if nativeBoolAny(args, "full_output", "fullOutput") {
		outputCandidates = candidates
	}
	candidateMaps := nativeRepositoryEvaluationCandidateMaps(outputCandidates)
	rejectionMaps := nativeRepositoryEvaluationRejectionMaps(rejections)
	return map[string]any{
		"commit":        commit,
		"inventoryHash": inventoryHash,
		"candidates":    candidateMaps,
		"rejections":    rejectionMaps,
		"candidatePoolTotal": len(
			candidates,
		),
		"candidatePoolAI":        min(len(candidates), repositoryEvaluationAICandidateLimit),
		"candidatePoolTruncated": len(candidates) > repositoryEvaluationAICandidateLimit,
		"counts":                 nativeRepositoryEvaluationCatalogCounts(candidates, rejections),
	}, nil
}

func nativeRepositoryEvaluationFilter(args map[string]any) (map[string]any, error) {
	var candidates []reposcope.Candidate
	if err := nativeRepositoryEvaluationDecode(args["candidates"], &candidates); err != nil {
		return nil, fmt.Errorf("repository scope candidates: %w", err)
	}
	byID := make(map[string]reposcope.Candidate, len(candidates))
	for _, candidate := range candidates {
		if err := reposcope.ValidateCandidate(candidate); err != nil {
			return nil, err
		}
		if _, duplicate := byID[candidate.ID]; duplicate {
			return nil, reposcope.ErrInvalidCandidate
		}
		byID[candidate.ID] = candidate
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Path < candidates[j].Path
	})
	frozenSelectionValue, hasFrozenSelection := nativeRepositoryEvaluationOptionalArg(
		args, "frozen_selection", "frozenSelection",
	)
	frozenPlanValue, hasFrozenPlan := nativeRepositoryEvaluationOptionalArg(
		args, "frozen_plan", "frozenPlan",
	)
	if hasFrozenSelection != hasFrozenPlan {
		return nil, errors.New("frozen repository scope requires both selection and plan")
	}
	frozen := hasFrozenSelection
	if requested := nativeBoolAny(args, "scope_planned", "scopePlanned"); requested != frozen {
		return nil, errors.New("repository scope planned flag does not match frozen scope state")
	}
	var selection repoaudit.RepositoryReviewScopeSelection
	var frozenPlan repoaudit.RepositoryReviewScopePlan
	var err error
	if frozen {
		selection, err = nativeRepositoryEvaluationParseScopeSelection(frozenSelectionValue)
		if err != nil {
			return nil, err
		}
		frozenPlan, err = nativeRepositoryEvaluationParseScopePlan(frozenPlanValue)
		if err != nil {
			return nil, err
		}
	} else {
		planner := nativeMapValue(firstNonNil(
			args["planner"], args["filter"], args["scope_plan"], args["scopePlan"],
		))
		plannedScope, scopeErr := reposcope.NormalizeScope(reposcope.Scope{
			IncludePrefixes: nativeStringSliceAny(
				planner, "includePrefixes", "include_prefixes", "include_folders", "includeFolders",
			),
			ExcludePrefixes: nativeStringSliceAny(
				planner, "excludePrefixes", "exclude_prefixes", "exclude_folders", "excludeFolders",
			),
			CodeTypes: []reposcope.CodeType{
				reposcope.CodeTypeHotpath, reposcope.CodeTypeCode,
				reposcope.CodeTypeTest, reposcope.CodeTypeBenchTest,
			},
		})
		if scopeErr != nil {
			return nil, scopeErr
		}
		candidateIDs, candidateErr := nativeRepositoryEvaluationStrictStringArrayAny(
			planner,
			"candidateIds", "candidate_ids", "selectedCandidateIds", "selected_candidate_ids",
		)
		if candidateErr != nil {
			return nil, candidateErr
		}
		hotpathCandidateIDs, hotpathErr := nativeRepositoryEvaluationStrictStringArrayAny(
			planner, "hotpathCandidateIds", "hotpath_candidate_ids",
		)
		if hotpathErr != nil {
			return nil, hotpathErr
		}
		selection = repoaudit.RepositoryReviewScopeSelection{
			IncludePrefixes: plannedScope.IncludePrefixes,
			ExcludePrefixes: plannedScope.ExcludePrefixes,
			CandidateIDs:    candidateIDs, HotpathCandidateIDs: hotpathCandidateIDs,
		}
	}
	hardScope, err := nativeRepositoryEvaluationScope(firstNonNil(args["hard_scope"], args["hardScope"]))
	if err != nil {
		return nil, err
	}
	if len(selection.CandidateIDs) > repositoryEvaluationAICandidateLimit ||
		len(selection.HotpathCandidateIDs) > repositoryEvaluationAICandidateLimit {
		return nil, fmt.Errorf("%w: repository scope candidate ID limit exceeded", reposcope.ErrInvalidPolicy)
	}
	exactIDs, unknownExactIDs, err := nativeRepositoryEvaluationResolveCandidateIDs(
		selection.CandidateIDs,
		byID,
		!frozen,
		false,
	)
	if err != nil {
		return nil, fmt.Errorf("validate repository scope exact candidate IDs: %w", err)
	}
	exact := make(map[string]struct{}, len(exactIDs))
	for _, id := range exactIDs {
		exact[id] = struct{}{}
	}
	hotpathIDs, unknownHotpathIDs, err := nativeRepositoryEvaluationResolveCandidateIDs(
		selection.HotpathCandidateIDs,
		byID,
		!frozen,
		true,
	)
	if err != nil {
		return nil, fmt.Errorf("validate repository scope hotpath candidate IDs: %w", err)
	}
	hotpaths := make(map[string]struct{}, len(hotpathIDs))
	for _, id := range hotpathIDs {
		hotpaths[id] = struct{}{}
	}
	selection.CandidateIDs = exactIDs
	selection.HotpathCandidateIDs = hotpathIDs
	plannedScope := reposcope.Scope{
		IncludePrefixes: selection.IncludePrefixes,
		ExcludePrefixes: selection.ExcludePrefixes,
	}
	selected := make([]reposcope.Candidate, 0, len(candidates))
	includeFiles := 0
	codeTypeFiles := 0
	for _, candidate := range candidates {
		if !nativeRepositoryEvaluationHardCodeTypeAllows(hardScope.CodeTypes, candidate, hotpaths) {
			continue
		}
		codeTypeFiles++
		includedByPrefix := len(plannedScope.IncludePrefixes) == 0 || nativeRepositoryEvaluationPathMatches(
			candidate.Path, plannedScope.IncludePrefixes,
		)
		if includedByPrefix {
			includeFiles++
		}
		allowed := includedByPrefix && !nativeRepositoryEvaluationPathMatches(
			candidate.Path,
			plannedScope.ExcludePrefixes,
		)
		if !allowed || len(exact) > 0 && !nativeRepositoryEvaluationCandidateSelected(candidate, exact) {
			continue
		}
		selected = append(selected, candidate)
	}
	if len(selected) == 0 {
		return nil, errors.New("AI repository scope selected no safe files")
	}
	output := nativeRepositoryEvaluationSelectedOutput(selected, exactIDs, nil)
	var rationale string
	var warnings []string
	if frozen {
		rationale = frozenPlan.Rationale
		warnings = nativeRepositoryEvaluationCloneStrings(frozenPlan.Warnings)
	} else {
		planner := nativeMapValue(firstNonNil(
			args["planner"], args["filter"], args["scope_plan"], args["scopePlan"],
		))
		rationale = strings.TrimSpace(nativeAnyString(planner["rationale"]))
		warnings, err = nativeRepositoryEvaluationPlannerWarnings(planner)
		if err != nil {
			return nil, err
		}
		warnings = nativeRepositoryEvaluationAppendScopeRecoveryWarning(
			warnings,
			unknownExactIDs,
			unknownHotpathIDs,
		)
	}
	policyHash, err := nativeStableHash(firstNonNil(args["hard_scope"], args["hardScope"], map[string]any{}))
	if err != nil {
		return nil, err
	}
	commit := strings.ToLower(strings.TrimSpace(nativeStringAny(args, "commit", "commit_sha", "commitSha")))
	if frozen {
		if !nativeValidGitObjectID(commit) {
			return nil, errors.New("frozen repository scope requires an exact commit")
		}
		inventoryID := ""
		for _, candidate := range candidates {
			if candidate.CommitID != commit {
				return nil, errors.New("frozen repository scope candidates do not match the exact commit")
			}
			if inventoryID == "" {
				inventoryID = candidate.InventoryID
			} else if candidate.InventoryID != inventoryID {
				return nil, errors.New("frozen repository scope candidates do not share one inventory")
			}
		}
	}
	legacyPlanHash := nativeRepositoryEvaluationPlanHash(
		exactIDs,
		hotpathIDs,
		plannedScope.IncludePrefixes,
		plannedScope.ExcludePrefixes,
		output["acceptedCandidateIds"].([]string),
		output["selectedPaths"].([]string),
		rationale,
	)
	planHash := nativeRepositoryEvaluationPlanHash(
		exactIDs,
		hotpathIDs,
		plannedScope.IncludePrefixes,
		plannedScope.ExcludePrefixes,
		output["acceptedCandidateIds"].([]string),
		output["selectedPaths"].([]string),
		rationale,
		warnings,
	)
	counts := repoaudit.RepositoryReviewScopePlanCounts{
		TotalFiles: len(candidates), CodeTypeFiles: codeTypeFiles,
		IncludeFiles: includeFiles, ExcludedFiles: includeFiles - len(selected),
		SelectedFiles: len(selected),
	}
	summary := fmt.Sprintf("AI scope preflight selected %d of %d safe files.", len(selected), len(candidates))
	plan := repoaudit.RepositoryReviewScopePlan{
		CommitSHA: commit, PolicyHash: policyHash, Hash: planHash,
		Summary: summary, Rationale: rationale, Warnings: warnings, Counts: counts,
	}
	if frozen {
		hashMatches := frozenPlan.Hash == planHash ||
			(frozenPlan.Hash == legacyPlanHash &&
				!nativeRepositoryEvaluationHasReservedWarning(frozenPlan.Warnings))
		if frozenPlan.CommitSHA != commit || frozenPlan.PolicyHash != policyHash ||
			!hashMatches || frozenPlan.Summary != summary || frozenPlan.Counts != counts {
			return nil, errors.New(
				"frozen repository scope plan does not match the current commit, policy, or candidates",
			)
		}
		plan = frozenPlan
	}
	output["scopeSelection"] = nativeRepositoryEvaluationScopeSelectionMap(selection)
	output["scopePlan"] = nativeRepositoryEvaluationScopePlanMap(plan)
	return output, nil
}

func nativeRepositoryEvaluationScopeSelectionMap(
	selection repoaudit.RepositoryReviewScopeSelection,
) map[string]any {
	return map[string]any{
		"include_prefixes":      nativeRepositoryEvaluationCloneStrings(selection.IncludePrefixes),
		"exclude_prefixes":      nativeRepositoryEvaluationCloneStrings(selection.ExcludePrefixes),
		"candidate_ids":         nativeRepositoryEvaluationCloneStrings(selection.CandidateIDs),
		"hotpath_candidate_ids": nativeRepositoryEvaluationCloneStrings(selection.HotpathCandidateIDs),
	}
}

func nativeRepositoryEvaluationScopePlanMap(plan repoaudit.RepositoryReviewScopePlan) map[string]any {
	return map[string]any{
		"commit_sha": plan.CommitSHA, "policy_hash": plan.PolicyHash, "hash": plan.Hash,
		"summary": plan.Summary, "rationale": plan.Rationale,
		"warnings": nativeRepositoryEvaluationCloneStrings(plan.Warnings),
		"counts": map[string]any{
			"total_files": plan.Counts.TotalFiles, "code_type_files": plan.Counts.CodeTypeFiles,
			"include_files": plan.Counts.IncludeFiles, "excluded_files": plan.Counts.ExcludedFiles,
			"selected_files": plan.Counts.SelectedFiles,
		},
	}
}

func nativeRepositoryEvaluationOptionalArg(args map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		value, exists := args[key]
		if !exists || value == nil {
			continue
		}
		if object, ok := value.(map[string]any); ok && len(object) == 0 {
			continue
		}
		return value, true
	}
	return nil, false
}

func nativeRepositoryEvaluationParseScopeSelection(
	value any,
) (repoaudit.RepositoryReviewScopeSelection, error) {
	required := []string{
		"include_prefixes", "exclude_prefixes", "candidate_ids", "hotpath_candidate_ids",
	}
	if err := nativeRepositoryEvaluationValidateFrozenObject(value, required, nil); err != nil {
		return repoaudit.RepositoryReviewScopeSelection{}, fmt.Errorf(
			"invalid frozen repository scope selection: %w", err,
		)
	}
	object := nativeMapValue(value)
	includePrefixes, includeErr := nativeRepositoryEvaluationStrictStringArrayAny(
		object, "include_prefixes",
	)
	excludePrefixes, excludeErr := nativeRepositoryEvaluationStrictStringArrayAny(
		object, "exclude_prefixes",
	)
	candidateIDs, candidateErr := nativeRepositoryEvaluationStrictStringArrayAny(
		object, "candidate_ids",
	)
	hotpathCandidateIDs, hotpathErr := nativeRepositoryEvaluationStrictStringArrayAny(
		object, "hotpath_candidate_ids",
	)
	if includeErr != nil || excludeErr != nil || candidateErr != nil || hotpathErr != nil {
		return repoaudit.RepositoryReviewScopeSelection{}, errors.New(
			"invalid frozen repository scope selection",
		)
	}
	selection := repoaudit.RepositoryReviewScopeSelection{
		IncludePrefixes: includePrefixes, ExcludePrefixes: excludePrefixes,
		CandidateIDs: candidateIDs, HotpathCandidateIDs: hotpathCandidateIDs,
	}
	normalized, err := repoaudit.NormalizeRepositoryReviewScopeSelection(selection)
	if err != nil || !nativeRepositoryEvaluationScopeSelectionsEqual(selection, normalized) {
		return repoaudit.RepositoryReviewScopeSelection{}, errors.New(
			"frozen repository scope selection is not canonical",
		)
	}
	return normalized, nil
}

func nativeRepositoryEvaluationParseScopePlan(value any) (repoaudit.RepositoryReviewScopePlan, error) {
	required := []string{"commit_sha", "policy_hash", "hash", "summary", "warnings", "counts"}
	if err := nativeRepositoryEvaluationValidateFrozenObject(value, required, []string{"rationale"}); err != nil {
		return repoaudit.RepositoryReviewScopePlan{}, errors.New("invalid frozen repository scope plan")
	}
	object := nativeMapValue(value)
	countFields := []string{
		"total_files", "code_type_files", "include_files", "excluded_files", "selected_files",
	}
	if err := nativeRepositoryEvaluationValidateFrozenObject(object["counts"], countFields, nil); err != nil {
		return repoaudit.RepositoryReviewScopePlan{}, errors.New("invalid frozen repository scope plan counts")
	}
	var plan repoaudit.RepositoryReviewScopePlan
	if err := nativeRepositoryEvaluationDecode(value, &plan); err != nil {
		return repoaudit.RepositoryReviewScopePlan{}, errors.New("invalid frozen repository scope plan")
	}
	normalized, err := repoaudit.NormalizeRepositoryReviewScopePlan(plan)
	if err != nil || normalized.Hash == "" || !nativeRepositoryEvaluationScopePlansEqual(plan, normalized) {
		return repoaudit.RepositoryReviewScopePlan{}, errors.New("frozen repository scope plan is not canonical")
	}
	return normalized, nil
}

func nativeRepositoryEvaluationValidateFrozenObject(
	value any,
	required []string,
	optional []string,
) error {
	object := nativeMapValue(value)
	if object == nil {
		return errors.New("object is required")
	}
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, key := range append(append([]string(nil), required...), optional...) {
		allowed[key] = struct{}{}
	}
	if err := nativeValidateRepositoryReviewObjectFields(object, allowed, "frozen scope"); err != nil {
		return err
	}
	for _, key := range required {
		if _, exists := object[key]; !exists {
			return errors.New("required field is missing")
		}
	}
	return nil
}

func nativeRepositoryEvaluationCanonicalStrings(values []string) []string {
	canonical := nativeRepositoryEvaluationCloneStrings(values)
	sort.Strings(canonical)
	return canonical
}

func nativeRepositoryEvaluationStrictStringArrayAny(
	values map[string]any,
	keys ...string,
) ([]string, error) {
	var result []string
	found := false
	for _, key := range keys {
		value, exists := values[key]
		if !exists {
			continue
		}
		if found {
			return nil, errors.New("repository scope planner returned multiple candidate ID arrays")
		}
		found = true
		switch items := value.(type) {
		case []string:
			result = nativeRepositoryEvaluationCloneStrings(items)
		case []any:
			result = make([]string, len(items))
			for index, item := range items {
				text, ok := item.(string)
				if !ok {
					return nil, errors.New("repository scope planner returned an invalid candidate ID array")
				}
				result[index] = text
			}
		default:
			return nil, errors.New("repository scope planner returned an invalid candidate ID array")
		}
	}
	if found {
		return result, nil
	}
	return []string{}, nil
}

func nativeRepositoryEvaluationResolveCandidateIDs(
	values []string,
	byID map[string]reposcope.Candidate,
	dropWellFormedUnknown bool,
	requireHotpathEligible bool,
) ([]string, int, error) {
	canonical := nativeRepositoryEvaluationCanonicalStrings(values)
	resolved := make([]string, 0, len(canonical))
	seen := make(map[string]struct{}, len(canonical))
	dropped := 0
	for _, id := range canonical {
		if _, duplicate := seen[id]; duplicate {
			return nil, 0, reposcope.ErrDuplicateCandidate
		}
		seen[id] = struct{}{}
		candidate, exists := byID[id]
		if !exists {
			wellFormed := nativeRepositoryEvaluationWellFormedCandidateID(id)
			if dropWellFormedUnknown && wellFormed {
				dropped++
				continue
			}
			if !wellFormed {
				return nil, 0, nativeRepositoryEvaluationMalformedCandidateIDError(id)
			}
			return nil, 0, fmt.Errorf(
				"%w: frozen repository scope references a candidate absent from the rebuilt commit-bound catalog",
				reposcope.ErrUnknownCandidate,
			)
		}
		if requireHotpathEligible && candidate.CodeType != reposcope.CodeTypeCode &&
			candidate.CodeType != reposcope.CodeTypeHotpath {
			return nil, 0, reposcope.ErrInvalidCandidate
		}
		resolved = append(resolved, id)
	}
	return resolved, dropped, nil
}

func nativeRepositoryEvaluationMalformedCandidateIDError(value string) error {
	const prefix = "cand_"
	suffix, prefixed := strings.CutPrefix(value, prefix)
	if prefixed && nativeValidGitObjectID(suffix) {
		return fmt.Errorf(
			"%w: repository scope planner returned a value shaped like %q plus a %d-character Git object ID instead of a 64-character opaque candidate ID copied from the supplied catalog",
			reposcope.ErrUnknownCandidate,
			prefix,
			len(suffix),
		)
	}
	return fmt.Errorf(
		"%w: repository scope planner returned a malformed opaque candidate ID; expected %q plus 64 lowercase hexadecimal characters copied from the supplied catalog",
		reposcope.ErrUnknownCandidate,
		prefix,
	)
}

func nativeRepositoryEvaluationWellFormedCandidateID(value string) bool {
	if len(value) != len("cand_")+sha256.Size*2 || !strings.HasPrefix(value, "cand_") {
		return false
	}
	for _, character := range value[len("cand_"):] {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func nativeRepositoryEvaluationCloneStrings(values []string) []string {
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func nativeRepositoryEvaluationNormalizeWarnings(values []string) ([]string, error) {
	if len(values) > repositoryEvaluationScopeWarnings {
		return nil, errors.New("repository scope planner returned too many warnings")
	}
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		warning := strings.TrimSpace(raw)
		if warning == "" || len(warning) > repositoryEvaluationScopeWarningBytes ||
			strings.ContainsRune(warning, 0) {
			return nil, errors.New("repository scope planner returned an invalid warning")
		}
		if strings.HasPrefix(strings.ToLower(warning), strings.ToLower(repositoryEvaluationNativeWarning)) {
			return nil, errors.New("repository scope planner returned a reserved warning")
		}
		if _, duplicate := seen[warning]; duplicate {
			return nil, errors.New("repository scope planner returned duplicate warnings")
		}
		seen[warning] = struct{}{}
		normalized = append(normalized, warning)
	}
	return normalized, nil
}

func nativeRepositoryEvaluationPlannerWarnings(planner map[string]any) ([]string, error) {
	value, exists := planner["warnings"]
	if !exists || value == nil {
		return []string{}, nil
	}
	var warnings []string
	if err := nativeRepositoryEvaluationDecode(value, &warnings); err != nil {
		return nil, errors.New("repository scope planner returned invalid warnings")
	}
	return nativeRepositoryEvaluationNormalizeWarnings(warnings)
}

func nativeRepositoryEvaluationAppendScopeRecoveryWarning(
	warnings []string,
	unknownExactIDs int,
	unknownHotpathIDs int,
) []string {
	if unknownExactIDs == 0 && unknownHotpathIDs == 0 {
		return warnings
	}
	recovery := fmt.Sprintf(
		repositoryEvaluationNativeWarning+" ignored %d unknown exact candidate IDs and %d unknown hotpath candidate IDs; the sanitized selection uses only known commit-bound candidates and trusted prefixes.",
		unknownExactIDs,
		unknownHotpathIDs,
	)
	retained := make([]string, 0, min(repositoryEvaluationScopeWarnings, len(warnings)+1))
	for _, warning := range warnings {
		if len(retained) == repositoryEvaluationScopeWarnings-1 {
			break
		}
		retained = append(retained, warning)
	}
	return append(retained, recovery)
}

func nativeRepositoryEvaluationHasReservedWarning(warnings []string) bool {
	reserved := strings.ToLower(repositoryEvaluationNativeWarning)
	for _, warning := range warnings {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(warning)), reserved) {
			return true
		}
	}
	return false
}

func nativeRepositoryEvaluationStringsEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func nativeRepositoryEvaluationScopeSelectionsEqual(
	left repoaudit.RepositoryReviewScopeSelection,
	right repoaudit.RepositoryReviewScopeSelection,
) bool {
	return nativeRepositoryEvaluationStringsEqual(left.IncludePrefixes, right.IncludePrefixes) &&
		nativeRepositoryEvaluationStringsEqual(left.ExcludePrefixes, right.ExcludePrefixes) &&
		nativeRepositoryEvaluationStringsEqual(left.CandidateIDs, right.CandidateIDs) &&
		nativeRepositoryEvaluationStringsEqual(left.HotpathCandidateIDs, right.HotpathCandidateIDs)
}

func nativeRepositoryEvaluationScopePlansEqual(
	left repoaudit.RepositoryReviewScopePlan,
	right repoaudit.RepositoryReviewScopePlan,
) bool {
	return left.CommitSHA == right.CommitSHA && left.PolicyHash == right.PolicyHash &&
		left.Hash == right.Hash && left.Summary == right.Summary &&
		left.Rationale == right.Rationale && left.Counts == right.Counts &&
		nativeRepositoryEvaluationStringsEqual(left.Warnings, right.Warnings)
}

func nativeRepositoryEvaluationPathMatches(filePath string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if prefix == "." || filePath == prefix || strings.HasPrefix(filePath, prefix+"/") {
			return true
		}
	}
	return false
}

func nativeRepositoryEvaluationPlanHash(fields ...any) string {
	hash := sha256.New()
	writeField := func(value string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(value))
	}
	for _, field := range fields {
		switch value := field.(type) {
		case string:
			writeField("string")
			writeField(value)
		case []string:
			writeField("strings")
			writeField(fmt.Sprintf("%d", len(value)))
			for _, item := range value {
				writeField(item)
			}
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func nativeRepositoryEvaluationHasCodeType(types []reposcope.CodeType, target reposcope.CodeType) bool {
	for _, codeType := range types {
		if codeType == target {
			return true
		}
	}
	return false
}

func nativeRepositoryEvaluationHardCodeTypeAllows(
	types []reposcope.CodeType,
	candidate reposcope.Candidate,
	hotpaths map[string]struct{},
) bool {
	if len(types) == 0 || nativeRepositoryEvaluationHasCodeType(types, candidate.CodeType) {
		return true
	}
	if candidate.CodeType == reposcope.CodeTypeCode &&
		nativeRepositoryEvaluationHasCodeType(types, reposcope.CodeTypeHotpath) {
		_, promoted := hotpaths[candidate.ID]
		return promoted
	}
	return false
}

func nativeRepositoryEvaluationCandidateSelected(
	candidate reposcope.Candidate,
	selected map[string]struct{},
) bool {
	_, ok := selected[candidate.ID]
	return ok
}

func nativeRepositoryEvaluationAICandidates(
	candidates []reposcope.Candidate,
	limit int,
) []reposcope.Candidate {
	if limit <= 0 || len(candidates) <= limit {
		return append([]reposcope.Candidate(nil), candidates...)
	}
	byLanguage := make(map[reposcope.Language][]reposcope.Candidate)
	languages := make([]reposcope.Language, 0)
	for _, candidate := range candidates {
		if byLanguage[candidate.Language] == nil {
			languages = append(languages, candidate.Language)
		}
		byLanguage[candidate.Language] = append(byLanguage[candidate.Language], candidate)
	}
	sort.Slice(languages, func(i, j int) bool { return languages[i] < languages[j] })
	for _, language := range languages {
		byLanguage[language] = nativeRepositoryEvaluationDiverseOrder(byLanguage[language])
	}
	perLanguage := min(80, max(reposcope.MaxPerLanguageQuota, limit/max(1, len(languages))))
	selected := make([]reposcope.Candidate, 0, limit)
	indexes := make(map[reposcope.Language]int, len(languages))
	for len(selected) < limit {
		advanced := false
		for _, language := range languages {
			index := indexes[language]
			if index >= len(byLanguage[language]) || index >= perLanguage || len(selected) >= limit {
				continue
			}
			selected = append(selected, byLanguage[language][index])
			indexes[language]++
			advanced = true
		}
		if !advanced {
			break
		}
	}
	return selected
}

func nativeRepositoryEvaluationDiverseOrder(
	candidates []reposcope.Candidate,
) []reposcope.Candidate {
	byRegion := make(map[string][]reposcope.Candidate)
	regions := make([]string, 0)
	for _, candidate := range candidates {
		if byRegion[candidate.Region] == nil {
			regions = append(regions, candidate.Region)
		}
		byRegion[candidate.Region] = append(byRegion[candidate.Region], candidate)
	}
	sort.Strings(regions)
	for _, region := range regions {
		sort.Slice(byRegion[region], func(i, j int) bool {
			left, right := byRegion[region][i], byRegion[region][j]
			leftSubstantive := left.Size >= reposcope.DefaultPreferredMinBytes
			rightSubstantive := right.Size >= reposcope.DefaultPreferredMinBytes
			if leftSubstantive != rightSubstantive {
				return leftSubstantive
			}
			if left.Module != right.Module {
				return left.Module < right.Module
			}
			if left.Size != right.Size {
				return left.Size > right.Size
			}
			return left.Path < right.Path
		})
	}
	ordered := make([]reposcope.Candidate, 0, len(candidates))
	indexes := make(map[string]int, len(regions))
	for len(ordered) < len(candidates) {
		for _, region := range regions {
			index := indexes[region]
			if index >= len(byRegion[region]) {
				continue
			}
			ordered = append(ordered, byRegion[region][index])
			indexes[region]++
		}
	}
	return ordered
}

func nativeRepositoryEvaluationSelection(args map[string]any) (map[string]any, error) {
	var candidates []reposcope.Candidate
	if err := nativeRepositoryEvaluationDecode(args["candidates"], &candidates); err != nil {
		return nil, fmt.Errorf("repository evaluation candidates: %w", err)
	}
	proposalValue := nativeMapValue(firstNonNil(args["selection"], args["ai_selection"], args["aiSelection"]))
	proposal := reposcope.AISelection{CandidateIDs: nativeStringSliceAny(
		proposalValue,
		"candidateIds", "candidate_ids", "selectedCandidateIds", "selected_candidate_ids",
	)}
	policy, err := nativeRepositoryEvaluationSelectionPolicy(
		firstNonNil(args["policy"], args["selection_policy"], args["selectionPolicy"]),
	)
	if err != nil {
		return nil, err
	}
	result, err := reposcope.ValidateAISelection(candidates, proposal, policy)
	if err != nil {
		return nil, err
	}
	return nativeRepositoryEvaluationSelectedOutput(
		result.Selected,
		result.AcceptedAIIDs,
		result.FilledIDs,
	), nil
}

func nativeRepositoryEvaluationSelectedOutput(
	selectedCandidates []reposcope.Candidate,
	acceptedIDs []string,
	filledIDs []string,
) map[string]any {
	selected := nativeRepositoryEvaluationCandidateMaps(selectedCandidates)
	paths := make([]string, 0, len(selectedCandidates))
	counts := make(map[string]int)
	bytesByLanguage := make(map[string]int64)
	regionsByLanguage := make(map[string]map[string]struct{})
	for _, candidate := range selectedCandidates {
		paths = append(paths, candidate.Path)
		language := string(candidate.Language)
		counts[language]++
		bytesByLanguage[language] += candidate.Size
		if regionsByLanguage[language] == nil {
			regionsByLanguage[language] = make(map[string]struct{})
		}
		regionsByLanguage[language][candidate.Region] = struct{}{}
	}
	regions := make(map[string]int, len(regionsByLanguage))
	for language, values := range regionsByLanguage {
		regions[language] = len(values)
	}
	return map[string]any{
		"selected": selected, "selectedPaths": paths,
		"acceptedCandidateIds": append([]string(nil), acceptedIDs...),
		"filledCandidateIds":   append([]string(nil), filledIDs...),
		"languageCounts":       counts, "languageBytes": bytesByLanguage,
		"languageRegions": regions, "totalSelected": len(selectedCandidates),
	}
}

func nativeRepositoryEvaluationScope(value any) (reposcope.Scope, error) {
	mapped := nativeMapValue(value)
	codeTypeValues := nativeStringSliceAny(mapped, "codeTypes", "code_types")
	codeTypes := make([]reposcope.CodeType, 0, len(codeTypeValues))
	for _, value := range codeTypeValues {
		codeTypes = append(codeTypes, reposcope.CodeType(value))
	}
	return reposcope.NormalizeScope(reposcope.Scope{
		IncludePrefixes: nativeStringSliceAny(mapped, "includePrefixes", "include_folders", "includeFolders"),
		ExcludePrefixes: nativeStringSliceAny(mapped, "excludePrefixes", "exclude_folders", "excludeFolders"),
		CodeTypes:       codeTypes,
		FreeText:        nativeStringAny(mapped, "freeText", "free_text"),
	})
}

func nativeRepositoryEvaluationSelectionPolicy(value any) (reposcope.SelectionPolicy, error) {
	mapped := nativeMapValue(value)
	defaultQuota := int(nativeInt64Any(mapped, "defaultPerLanguage", "default_per_language"))
	preferredBytes := nativeInt64Any(mapped, "preferredMinBytes", "preferred_min_bytes")
	perLanguageMap := nativeMapValue(firstNonNil(mapped["perLanguage"], mapped["per_language"]))
	perLanguage := make(map[reposcope.Language]int, len(perLanguageMap))
	for rawLanguage, rawQuota := range perLanguageMap {
		language := reposcope.Language(strings.TrimSpace(rawLanguage))
		quota := int(nativeInt64Any(map[string]any{"quota": rawQuota}, "quota"))
		if language == "" || quota == 0 {
			return reposcope.SelectionPolicy{}, reposcope.ErrInvalidPolicy
		}
		perLanguage[language] = quota
	}
	return reposcope.SelectionPolicy{
		DefaultPerLanguage: defaultQuota,
		PerLanguage:        perLanguage,
		PreferredMinBytes:  preferredBytes,
	}, nil
}

func nativeRepositoryEvaluationCandidateMaps(
	candidates []reposcope.Candidate,
) []map[string]any {
	mapped := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		mapped = append(mapped, map[string]any{
			"id": candidate.ID, "commitId": candidate.CommitID,
			"inventoryId": candidate.InventoryID, "path": candidate.Path,
			"blobId": candidate.BlobID, "size": float64(candidate.Size),
			"language": string(candidate.Language), "codeType": string(candidate.CodeType),
			"region": candidate.Region, "module": candidate.Module,
		})
	}
	return mapped
}

func nativeRepositoryEvaluationRejectionMaps(
	rejections []reposcope.Rejection,
) []map[string]any {
	mapped := make([]map[string]any, 0, len(rejections))
	for _, rejection := range rejections {
		mapped = append(mapped, map[string]any{
			"path": rejection.Path, "reason": string(rejection.Reason),
		})
	}
	return mapped
}

func nativeRepositoryEvaluationJSONMaps[T any](values []T) ([]map[string]any, error) {
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	var mapped []map[string]any
	if err := json.Unmarshal(encoded, &mapped); err != nil {
		return nil, err
	}
	if mapped == nil {
		mapped = []map[string]any{}
	}
	return mapped, nil
}

func nativeRepositoryEvaluationDecode(value any, target any) error {
	if raw, ok := value.(string); ok {
		return json.Unmarshal([]byte(raw), target)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func nativeRepositoryEvaluationFileKind(mode string) reposcope.FileKind {
	switch strings.TrimSpace(mode) {
	case "100644", "100755":
		return reposcope.FileKindRegular
	case "120000":
		return reposcope.FileKindSymlink
	case "040000", "40000":
		return reposcope.FileKindDirectory
	default:
		return reposcope.FileKindOther
	}
}

func nativeRepositoryEvaluationSample(
	ctx context.Context,
	repo string,
	file nativeGitFile,
) ([]byte, error) {
	if nativeRepositoryEvaluationFileKind(file.Mode) != reposcope.FileKindRegular || file.SizeBytes <= 0 {
		return nil, nil
	}
	if file.SizeBytes > repositoryEvaluationCatalogMaxFileBytes {
		return nil, nil
	}
	// Always sample the exact inventory blob. Reading the checkout would let
	// dirty worktree bytes or a different checked-out commit influence the
	// language and safety classification of an immutable candidate.
	var sample []byte
	err := nativeGitBatchReadBlobs(ctx, repo, []nativeGitBlobRequest{{
		ObjectID: file.BlobHash, ExpectedSize: file.SizeBytes,
		RetainBytes: min(file.SizeBytes, int64(reposcope.MaxSampleBytes)),
	}}, func(_ int, content []byte) error {
		sample = append([]byte(nil), content...)
		return nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "size mismatch with inventory") {
			return nil, errors.New("repository evaluation sample does not match its declared size")
		}
		return nil, fmt.Errorf("repository evaluation sample git cat-file --batch: %w", err)
	}
	return sample, err
}

func nativeRepositoryEvaluationCatalogCounts(
	candidates []reposcope.Candidate,
	rejections []reposcope.Rejection,
) map[string]any {
	available := make(map[string]int)
	bytesByLanguage := make(map[string]int64)
	regions := make(map[string]map[string]struct{})
	for _, candidate := range candidates {
		language := string(candidate.Language)
		available[language]++
		bytesByLanguage[language] += candidate.Size
		if regions[language] == nil {
			regions[language] = make(map[string]struct{})
		}
		regions[language][candidate.Region] = struct{}{}
	}
	regionCounts := make(map[string]int, len(regions))
	for language, values := range regions {
		regionCounts[language] = len(values)
	}
	rejected := make(map[string]int)
	for _, rejection := range rejections {
		rejected[string(rejection.Reason)]++
	}
	languages := make([]string, 0, len(available))
	for language := range available {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	return map[string]any{
		"languages": languages, "availableByLanguage": available,
		"bytesByLanguage": bytesByLanguage, "regionsByLanguage": regionCounts,
		"eligibleFiles": len(candidates), "rejectedFiles": len(rejections),
		"rejectedByReason": rejected,
	}
}
