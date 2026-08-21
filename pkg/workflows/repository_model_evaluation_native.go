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

	"github.com/sipeed/picoclaw/pkg/reposcope"
)

const repositoryEvaluationCatalogMaxFileBytes = 512 << 10

const repositoryEvaluationAICandidateLimit = 4096

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
	return map[string]any{"blinded": blinded, "mapping": mapping}, nil
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
	planner := nativeMapValue(firstNonNil(args["planner"], args["filter"], args["scope_plan"], args["scopePlan"]))
	hardScope, err := nativeRepositoryEvaluationScope(firstNonNil(args["hard_scope"], args["hardScope"]))
	if err != nil {
		return nil, err
	}
	include := nativeStringSliceAny(planner, "includePrefixes", "include_folders", "includeFolders")
	exclude := nativeStringSliceAny(planner, "excludePrefixes", "exclude_folders", "excludeFolders")
	plannedScope, err := reposcope.NormalizeScope(reposcope.Scope{
		IncludePrefixes: include,
		ExcludePrefixes: exclude,
		CodeTypes: []reposcope.CodeType{
			reposcope.CodeTypeHotpath, reposcope.CodeTypeCode,
			reposcope.CodeTypeTest, reposcope.CodeTypeBenchTest,
		},
	})
	if err != nil {
		return nil, err
	}
	exactIDs := nativeStringSliceAny(
		planner,
		"candidateIds",
		"candidate_ids",
		"selectedCandidateIds",
		"selected_candidate_ids",
	)
	exact := make(map[string]struct{}, len(exactIDs))
	for _, id := range exactIDs {
		if _, duplicate := exact[id]; duplicate {
			return nil, reposcope.ErrDuplicateCandidate
		}
		if _, exists := byID[id]; !exists {
			return nil, reposcope.ErrUnknownCandidate
		}
		exact[id] = struct{}{}
	}
	hotpathIDs := nativeStringSliceAny(planner, "hotpathCandidateIds", "hotpath_candidate_ids")
	hotpaths := make(map[string]struct{}, len(hotpathIDs))
	for _, id := range hotpathIDs {
		if _, duplicate := hotpaths[id]; duplicate {
			return nil, reposcope.ErrDuplicateCandidate
		}
		candidate, exists := byID[id]
		if !exists {
			return nil, reposcope.ErrUnknownCandidate
		}
		if candidate.CodeType != reposcope.CodeTypeCode && candidate.CodeType != reposcope.CodeTypeHotpath {
			return nil, reposcope.ErrInvalidCandidate
		}
		hotpaths[id] = struct{}{}
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
	rationale := strings.TrimSpace(nativeAnyString(planner["rationale"]))
	warnings := nativeStringSliceAny(planner, "warnings")
	policyHash, err := nativeStableHash(firstNonNil(args["hard_scope"], args["hardScope"], map[string]any{}))
	if err != nil {
		return nil, err
	}
	planHash := nativeRepositoryEvaluationPlanHash(
		exactIDs,
		hotpathIDs,
		plannedScope.IncludePrefixes,
		plannedScope.ExcludePrefixes,
		output["acceptedCandidateIds"].([]string),
		output["selectedPaths"].([]string),
		rationale,
	)
	commit := strings.TrimSpace(nativeStringAny(args, "commit", "commit_sha", "commitSha"))
	output["scopePlan"] = map[string]any{
		"commit_sha": commit, "policy_hash": policyHash, "hash": planHash,
		"summary":   fmt.Sprintf("AI scope preflight selected %d of %d safe files.", len(selected), len(candidates)),
		"rationale": rationale, "warnings": warnings,
		"counts": map[string]any{
			"total_files": len(candidates), "code_type_files": codeTypeFiles,
			"include_files": includeFiles, "excluded_files": includeFiles - len(selected),
			"selected_files": len(selected),
		},
	}
	return output, nil
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
