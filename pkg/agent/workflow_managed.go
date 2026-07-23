package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	pathpkg "path"
	"sort"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type workflowManagedChildPlan struct {
	index int
	label string
	scope []any
	tasks []string
}

type workflowManagedChildResult struct {
	plan       workflowManagedChildPlan
	choice     workflowManagedExecutionChoice
	text       string
	structured workflows.StructuredOutputResult
	repairs    int
	err        error
}

type workflowManagedExecutionChoice struct {
	modelName       string
	reasoningEffort string
	modelMeta       map[string]any
	effortMeta      map[string]any
	costMeta        map[string]any
}

type workflowManagedModelCandidate struct {
	name                string
	inputPricePerMTok   float64
	outputPricePerMTok  float64
	subscription        bool
	equivalentModelName string
	priceKnown          bool
	source              string
}

type workflowManagedCalibrationCacheEntry struct {
	Key                   string
	Uses                  int
	SuccessStreak         int
	NextCalibrationUse    int
	LastMatch             bool
	LastStatus            string
	Strategy              string
	Model                 string
	OutputFormat          string
	PromptHash            string
	ContextHash           string
	OutputSchemaHash      string
	TaskHash              string
	ChunkingHash          string
	PlanShapeHash         string
	Languages             []string
	Repositories          []string
	ScopeCount            int
	TaskCount             int
	ChildCount            int
	ChildScopeCounts      []int
	ChildTaskCounts       []int
	SplitFitScore         float64
	Provisional           bool
	BorrowedFromKey       string
	BorrowedSimilarity    float64
	BorrowedSuccessStreak int
}

type workflowManagedScopeCacheDescriptor struct {
	Count        int
	Items        []workflowManagedScopeItemCacheDescriptor
	Languages    []string
	Repositories []string
	WrapperHash  string
}

type workflowManagedScopeItemCacheDescriptor struct {
	Index       int
	Type        string
	ID          string
	Path        string
	Language    string
	Repository  string
	Commit      string
	Size        int
	Hash        string
	ContentHash string
	ValueHash   string
}

func (r *workflowAgentRunner) runManagedSplit(
	req workflows.AgentRequest,
	agent *AgentInstance,
	agentID string,
	sessionKey string,
	historyMode string,
	cacheMode string,
	promptCacheKey string,
	strategy string,
	runOnce workflowAgentTextRunner,
) (map[string]any, error) {
	options := workflowManagedOptions(req.Managed)
	strategy = workflowNormalizeSplitStrategy(strategy)
	plans := workflowManagedChildPlans(req, agent, options, strategy)
	metadata := workflowManagedMetadata(req, agent)
	metadata["strategy"] = strategy
	metadata["split"] = workflowManagedSplitMetadata(req, agent, options, strategy, plans)
	if len(plans) <= 1 {
		fallbackReq := req
		fallbackReq.Managed = "off"
		text, structured, repairs, err := workflowRunStructuredAgentWithOptions(
			workflowAgentMessage(fallbackReq),
			req.Output,
			runOnce,
			workflowAgentRunOptions{},
		)
		outputs := workflowStructuredAgentOutputs(
			text,
			structured,
			repairs,
			agentID,
			sessionKey,
			historyMode,
			cacheMode,
			promptCacheKey,
			req.MessageID,
		)
		outputs["managed"] = metadata
		return outputs, err
	}
	if options.calibrationEnabled {
		cacheKey, cacheIdentity := workflowManagedCalibrationCacheKey(req, agent, options, strategy, plans)
		shouldCalibrate, cacheMeta := agent.workflowManagedCalibrationCacheDecision(
			cacheKey,
			cacheIdentity,
			options,
		)
		if shouldCalibrate {
			calibration := r.runManagedSplitCalibration(req, agent, options, strategy, runOnce)
			recordMeta := agent.recordWorkflowManagedCalibrationCache(
				cacheKey,
				cacheIdentity,
				options,
				calibration,
			)
			for key, value := range recordMeta {
				cacheMeta[key] = value
			}
			calibration["cache"] = cacheMeta
			metadata["calibration"] = calibration
			if match, _ := calibration["match"].(bool); !match {
				fallbackReq := req
				fallbackReq.Managed = "off"
				text, structured, repairs, err := workflowRunStructuredAgentWithOptions(
					workflowAgentMessage(fallbackReq),
					req.Output,
					runOnce,
					workflowAgentRunOptions{},
				)
				outputs := workflowStructuredAgentOutputs(
					text,
					structured,
					repairs,
					agentID,
					sessionKey,
					historyMode,
					cacheMode,
					promptCacheKey,
					req.MessageID,
				)
				outputs["managed"] = metadata
				return outputs, err
			}
		} else {
			metadata["calibration"] = map[string]any{
				"status":   "trusted_cache",
				"match":    true,
				"strategy": strategy,
				"reason":   "recent matching split calibration passed",
				"cache":    cacheMeta,
			}
		}
	} else {
		metadata["calibration"] = map[string]any{
			"status": "not_run",
			"reason": "disabled by agent execution optimization options",
		}
	}

	cfg := (*config.Config)(nil)
	if r != nil && r.loop != nil {
		cfg = r.loop.cfg
	}
	results := workflowRunManagedChildren(req, agent, cfg, options, strategy, plans, runOnce)
	partials := make([]any, 0, len(results))
	childOutputs := make([]map[string]any, 0, len(results))
	totalRepairs := 0
	var firstErr error
	for _, result := range results {
		totalRepairs += result.repairs
		childOutputs = append(childOutputs, workflowManagedChildOutput(result))
		if result.structured.Structured != nil {
			partials = append(partials, result.structured.Structured)
		}
		if result.err != nil && firstErr == nil {
			firstErr = result.err
		}
	}
	metadata["optimization"] = workflowManagedOptimizationSummary(req, agent, cfg, options, results)
	if firstErr != nil {
		text := ""
		for _, result := range results {
			if result.text != "" {
				text = result.text
				break
			}
		}
		outputs := workflowAgentBaseOutputs(
			text,
			agentID,
			sessionKey,
			historyMode,
			cacheMode,
			promptCacheKey,
			req.MessageID,
		)
		outputs["managed"] = metadata
		outputs["managed_children"] = childOutputs
		return outputs, firstErr
	}

	combined := workflows.CombineStructuredOutputs(partials, req.Output.Schema)
	combinedJSON, _ := json.Marshal(combined)
	validation := workflows.ValidateAgentStructuredOutput(string(combinedJSON), req.Output)
	outputs := workflowAgentBaseOutputs(
		string(combinedJSON),
		agentID,
		sessionKey,
		historyMode,
		cacheMode,
		promptCacheKey,
		req.MessageID,
	)
	outputs["managed"] = metadata
	outputs["managed_children"] = childOutputs
	outputs["structured_valid"] = validation.Valid
	outputs["structured_repairs"] = totalRepairs
	outputs["structured_json"] = string(combinedJSON)
	outputs["structured"] = combined
	if validation.Error != "" {
		outputs["structured_error"] = validation.Error
	}
	if !validation.Valid {
		return outputs, fmt.Errorf("combined agent structured output invalid: %s", validation.Error)
	}
	return outputs, nil
}

func (r *workflowAgentRunner) runManagedSplitCalibration(
	req workflows.AgentRequest,
	agent *AgentInstance,
	options workflowManagedExecutionOptions,
	strategy string,
	runOnce workflowAgentTextRunner,
) map[string]any {
	sampleReq := workflowManagedCalibrationRequest(req, agent, options, strategy)
	plans := workflowManagedChildPlans(sampleReq, agent, options.withoutOptimization(), strategy)
	sampleScope := len(workflowScopeItems(sampleReq.Scope))
	sampleTasks := len(workflowAssignedTasks(sampleReq))
	if len(plans) <= 1 {
		return map[string]any{
			"status":       "skipped",
			"match":        true,
			"reason":       "sample fits in one child plan",
			"trials":       0,
			"sample_scope": sampleScope,
			"sample_tasks": sampleTasks,
		}
	}
	requiredMatches := options.calibrationRequiredMatches
	if requiredMatches <= 0 {
		requiredMatches = 1
	}
	maxTrials := options.calibrationMaxTrials
	if maxTrials <= 0 || maxTrials < requiredMatches {
		maxTrials = requiredMatches
	}
	var (
		matches        int
		trials         int
		repairs        int
		lastComparison map[string]any
		lastBaseline   any
		lastCombined   any
	)
	for trials < maxTrials && matches < requiredMatches {
		trials++
		baselineReq := sampleReq
		baselineReq.Managed = "off"
		baselineMessage := workflowManagedCalibrationMessage(baselineReq, "grouped baseline")
		_, baseline, baselineRepairs, baselineErr := workflowRunStructuredAgentWithOptions(
			baselineMessage,
			req.Output,
			runOnce,
			workflowAgentRunOptions{NoTools: true},
		)
		repairs += baselineRepairs
		if baselineErr != nil {
			return map[string]any{
				"status":           "failed",
				"match":            false,
				"phase":            "baseline",
				"error":            baselineErr.Error(),
				"repairs":          repairs,
				"trials":           trials,
				"required_matches": requiredMatches,
				"sample_scope":     sampleScope,
				"sample_tasks":     sampleTasks,
			}
		}
		results := workflowRunManagedChildren(
			sampleReq,
			agent,
			nil,
			options.withoutOptimization(),
			strategy,
			plans,
			runOnce,
		)
		partials := make([]any, 0, len(results))
		for _, result := range results {
			repairs += result.repairs
			if result.err != nil {
				return map[string]any{
					"status":           "failed",
					"match":            false,
					"phase":            "split",
					"error":            result.err.Error(),
					"repairs":          repairs,
					"trials":           trials,
					"required_matches": requiredMatches,
					"sample_scope":     sampleScope,
					"sample_tasks":     sampleTasks,
				}
			}
			partials = append(partials, result.structured.Structured)
		}
		combined := workflows.CombineStructuredOutputs(partials, req.Output.Schema)
		comparison := workflows.CompareStructuredOutputs(baseline.Structured, combined)
		lastComparison = comparison
		lastBaseline = baseline.Structured
		lastCombined = combined
		if match, _ := comparison["match"].(bool); !match {
			return map[string]any{
				"status":           "failed",
				"match":            false,
				"phase":            "compare",
				"repairs":          repairs,
				"trials":           trials,
				"required_matches": requiredMatches,
				"sample_scope":     sampleScope,
				"sample_tasks":     sampleTasks,
				"comparison":       comparison,
				"baseline":         baseline.Structured,
				"split_combined":   combined,
			}
		}
		matches++
	}
	return map[string]any{
		"status":           "passed",
		"match":            matches >= requiredMatches,
		"strategy":         strategy,
		"matches":          matches,
		"required_matches": requiredMatches,
		"trials":           trials,
		"sample_scope":     sampleScope,
		"sample_tasks":     sampleTasks,
		"repairs":          repairs,
		"comparison":       lastComparison,
		"baseline":         lastBaseline,
		"split_combined":   lastCombined,
	}
}

func workflowManagedCalibrationCacheKey(
	req workflows.AgentRequest,
	agent *AgentInstance,
	options workflowManagedExecutionOptions,
	strategy string,
	plans []workflowManagedChildPlan,
) (string, map[string]any) {
	model := ""
	if agent != nil {
		model = strings.TrimSpace(agent.Model)
	}
	scope := workflowManagedScopeCacheDescriptorFor(req.Scope)
	tasks := workflowAssignedOrAgentTasks(req, agent)
	scopeCounts := make([]int, 0, len(plans))
	taskCounts := make([]int, 0, len(plans))
	for _, plan := range plans {
		scopeCounts = append(scopeCounts, len(plan.scope))
		taskCounts = append(taskCounts, len(plan.tasks))
	}
	outputFormat := ""
	var outputSchema any
	if req.Output != nil {
		outputFormat = req.Output.Format
		outputSchema = req.Output.Schema
	}
	promptHash := workflowManagedHashString(req.Prompt)
	contextHash := workflowManagedHashString(req.Context)
	outputSchemaHash := workflowManagedHash(outputSchema)
	taskHash := workflowManagedHash(tasks)
	chunking := map[string]any{
		"adaptive_chunking":          options.adaptiveChunking,
		"max_items_per_chunk":        options.maxItemsPerChunk,
		"max_tasks_per_chunk":        options.maxTasksPerChunk,
		"target_child_prompt_tokens": options.targetChildPromptTokens,
	}
	planShape := map[string]any{
		"child_count":        len(plans),
		"child_scope_counts": scopeCounts,
		"child_task_counts":  taskCounts,
	}
	chunkingHash := workflowManagedHash(chunking)
	planShapeHash := workflowManagedHash(planShape)
	splitFitScore := workflowManagedSplitFitScore(req, options, plans)
	payload := map[string]any{
		"version":            1,
		"strategy":           strategy,
		"model":              model,
		"prompt_hash":        promptHash,
		"context_hash":       contextHash,
		"output_format":      outputFormat,
		"output_schema_hash": outputSchemaHash,
		"scope":              scope,
		"task_hash":          taskHash,
		"chunking":           chunking,
		"plan":               planShape,
	}
	key := workflowManagedHash(payload)
	return key, map[string]any{
		"key":                key,
		"key_version":        1,
		"strategy":           strategy,
		"model":              model,
		"output_format":      outputFormat,
		"scope_count":        scope.Count,
		"task_count":         len(tasks),
		"child_count":        len(plans),
		"child_scope_counts": append([]int(nil), scopeCounts...),
		"child_task_counts":  append([]int(nil), taskCounts...),
		"languages":          append([]string(nil), scope.Languages...),
		"repositories":       append([]string(nil), scope.Repositories...),
		"prompt_hash":        promptHash,
		"context_hash":       contextHash,
		"output_schema_hash": outputSchemaHash,
		"task_hash":          taskHash,
		"chunking_hash":      chunkingHash,
		"plan_shape_hash":    planShapeHash,
		"split_fit_score":    splitFitScore,
	}
}

func (agent *AgentInstance) workflowManagedCalibrationCacheDecision(
	key string,
	identity map[string]any,
	options workflowManagedExecutionOptions,
) (bool, map[string]any) {
	meta := workflowManagedCalibrationCacheBaseMeta(identity, options)
	if !options.calibrationCacheEnabled {
		meta["decision"] = "disabled"
		return true, meta
	}
	if agent == nil || key == "" {
		meta["decision"] = "unavailable"
		return true, meta
	}
	agentManagedCalibrationCacheMu.Lock()
	defer agentManagedCalibrationCacheMu.Unlock()
	if agent.managedCalibrationCache == nil {
		agent.managedCalibrationCache = make(map[string]workflowManagedCalibrationCacheEntry)
	}
	entry, ok := agent.managedCalibrationCache[key]
	if !ok {
		if source, similarity, found := workflowManagedSimilarCalibrationCacheEntry(
			agent.managedCalibrationCache,
			identity,
			options,
		); found {
			entry = workflowManagedBorrowedCalibrationCacheEntry(key, identity, source, similarity, options)
			agent.managedCalibrationCache[key] = entry
			meta["decision"] = "similar_hit"
			meta["borrowed"] = true
			meta["trusted"] = true
			workflowManagedCopyCalibrationEntryMeta(meta, entry)
			return false, meta
		}
		entry = workflowManagedCalibrationCacheEntry{Key: key, Uses: 1}
		workflowManagedApplyCalibrationIdentity(&entry, identity)
		agent.managedCalibrationCache[key] = entry
		meta["decision"] = "miss"
		workflowManagedCopyCalibrationEntryMeta(meta, entry)
		return true, meta
	}
	entry.Uses++
	workflowManagedApplyCalibrationIdentity(&entry, identity)
	agent.managedCalibrationCache[key] = entry
	workflowManagedCopyCalibrationEntryMeta(meta, entry)
	if !entry.LastMatch {
		meta["decision"] = "previous_not_trusted"
		return true, meta
	}
	if entry.Provisional {
		meta["decision"] = "borrowed_due"
		return true, meta
	}
	if entry.NextCalibrationUse <= 0 || entry.Uses >= entry.NextCalibrationUse {
		meta["decision"] = "due"
		return true, meta
	}
	meta["decision"] = "hit"
	return false, meta
}

func (agent *AgentInstance) recordWorkflowManagedCalibrationCache(
	key string,
	identity map[string]any,
	options workflowManagedExecutionOptions,
	calibration map[string]any,
) map[string]any {
	meta := workflowManagedCalibrationCacheBaseMeta(identity, options)
	if !options.calibrationCacheEnabled {
		meta["stored"] = false
		return meta
	}
	if agent == nil || key == "" {
		meta["stored"] = false
		return meta
	}
	match, _ := calibration["match"].(bool)
	status := stringMapValue(calibration, "status")
	trusted := match && status == "passed"
	agentManagedCalibrationCacheMu.Lock()
	defer agentManagedCalibrationCacheMu.Unlock()
	if agent.managedCalibrationCache == nil {
		agent.managedCalibrationCache = make(map[string]workflowManagedCalibrationCacheEntry)
	}
	entry := agent.managedCalibrationCache[key]
	wasProvisional := entry.Provisional
	borrowedSuccessStreak := entry.BorrowedSuccessStreak
	if entry.Uses <= 0 {
		entry.Uses = 1
	}
	entry.Key = key
	entry.LastMatch = trusted
	entry.LastStatus = status
	workflowManagedApplyCalibrationIdentity(&entry, identity)
	if trusted {
		if wasProvisional && borrowedSuccessStreak > entry.SuccessStreak {
			entry.SuccessStreak = borrowedSuccessStreak
		}
		entry.Provisional = false
		entry.SuccessStreak++
		interval := workflowManagedCalibrationCacheInterval(
			entry.SuccessStreak,
			workflowManagedCalibrationCacheMaxInterval(options),
			entry.SplitFitScore,
		)
		entry.NextCalibrationUse = entry.Uses + interval
	} else {
		entry.SuccessStreak = 0
		entry.Provisional = false
		entry.NextCalibrationUse = entry.Uses + 1
	}
	agent.managedCalibrationCache[key] = entry
	meta["stored"] = true
	meta["trusted"] = trusted
	workflowManagedCopyCalibrationEntryMeta(meta, entry)
	return meta
}

func workflowManagedCalibrationCacheBaseMeta(
	identity map[string]any,
	options workflowManagedExecutionOptions,
) map[string]any {
	meta := map[string]any{
		"enabled":      options.calibrationCacheEnabled,
		"max_interval": workflowManagedCalibrationCacheMaxInterval(options),
	}
	for key, value := range identity {
		meta[key] = value
	}
	return meta
}

func workflowManagedApplyCalibrationIdentity(
	entry *workflowManagedCalibrationCacheEntry,
	identity map[string]any,
) {
	if entry == nil {
		return
	}
	entry.Strategy = stringMapValue(identity, "strategy")
	entry.Model = stringMapValue(identity, "model")
	entry.OutputFormat = stringMapValue(identity, "output_format")
	entry.PromptHash = stringMapValue(identity, "prompt_hash")
	entry.ContextHash = stringMapValue(identity, "context_hash")
	entry.OutputSchemaHash = stringMapValue(identity, "output_schema_hash")
	entry.TaskHash = stringMapValue(identity, "task_hash")
	entry.ChunkingHash = stringMapValue(identity, "chunking_hash")
	entry.PlanShapeHash = stringMapValue(identity, "plan_shape_hash")
	entry.Languages = stringSliceMapValue(identity, "languages")
	entry.Repositories = stringSliceMapValue(identity, "repositories")
	entry.ScopeCount = intFromAny(identity["scope_count"])
	entry.TaskCount = intFromAny(identity["task_count"])
	entry.ChildCount = intFromAny(identity["child_count"])
	entry.ChildScopeCounts = intSliceMapValue(identity, "child_scope_counts")
	entry.ChildTaskCounts = intSliceMapValue(identity, "child_task_counts")
	entry.SplitFitScore = floatFromAny(identity["split_fit_score"])
}

func workflowManagedCopyCalibrationEntryMeta(meta map[string]any, entry workflowManagedCalibrationCacheEntry) {
	meta["uses"] = entry.Uses
	meta["success_streak"] = entry.SuccessStreak
	meta["next_calibration_use"] = entry.NextCalibrationUse
	meta["last_match"] = entry.LastMatch
	meta["last_status"] = entry.LastStatus
	meta["strategy"] = entry.Strategy
	meta["model"] = entry.Model
	meta["languages"] = append([]string(nil), entry.Languages...)
	meta["repositories"] = append([]string(nil), entry.Repositories...)
	meta["scope_count"] = entry.ScopeCount
	meta["task_count"] = entry.TaskCount
	meta["child_count"] = entry.ChildCount
	meta["split_fit_score"] = entry.SplitFitScore
	meta["provisional"] = entry.Provisional
	if entry.BorrowedFromKey != "" {
		meta["borrowed_from_key"] = entry.BorrowedFromKey
		meta["borrowed_similarity"] = entry.BorrowedSimilarity
		meta["borrowed_success_streak"] = entry.BorrowedSuccessStreak
	}
}

func workflowManagedCalibrationCacheMaxInterval(options workflowManagedExecutionOptions) int {
	if options.calibrationCacheMaxInterval > 0 {
		return options.calibrationCacheMaxInterval
	}
	return 16
}

func workflowManagedCalibrationCacheInterval(successStreak int, maxInterval int, splitFitScore float64) int {
	if maxInterval <= 0 {
		maxInterval = 1
	}
	if successStreak <= 1 {
		return 1
	}
	interval := 1 << min(successStreak-1, 30)
	if interval > maxInterval {
		interval = maxInterval
	}
	switch {
	case splitFitScore > 0 && splitFitScore < 0.45:
		interval = 1
	case splitFitScore > 0 && splitFitScore < 0.70:
		interval = max(1, (interval+1)/2)
	}
	return interval
}

func workflowManagedSimilarCalibrationCacheEntry(
	cache map[string]workflowManagedCalibrationCacheEntry,
	identity map[string]any,
	options workflowManagedExecutionOptions,
) (workflowManagedCalibrationCacheEntry, float64, bool) {
	threshold := options.calibrationSimilarityThreshold
	if threshold <= 0 {
		threshold = 0.72
	}
	var (
		best      workflowManagedCalibrationCacheEntry
		bestScore float64
		found     bool
	)
	for _, entry := range cache {
		if !entry.LastMatch || entry.Provisional {
			continue
		}
		score := workflowManagedCalibrationSimilarityScore(entry, identity)
		if score < threshold || score <= bestScore {
			continue
		}
		best = entry
		bestScore = score
		found = true
	}
	return best, bestScore, found
}

func workflowManagedBorrowedCalibrationCacheEntry(
	key string,
	identity map[string]any,
	source workflowManagedCalibrationCacheEntry,
	similarity float64,
	options workflowManagedExecutionOptions,
) workflowManagedCalibrationCacheEntry {
	entry := workflowManagedCalibrationCacheEntry{
		Key:                key,
		Uses:               1,
		LastMatch:          true,
		LastStatus:         "borrowed",
		Provisional:        true,
		BorrowedFromKey:    source.Key,
		BorrowedSimilarity: similarity,
	}
	workflowManagedApplyCalibrationIdentity(&entry, identity)
	entry.BorrowedSuccessStreak = workflowManagedBorrowedSuccessStreak(
		source.SuccessStreak,
		similarity,
		entry.SplitFitScore,
	)
	entry.SuccessStreak = entry.BorrowedSuccessStreak
	entry.NextCalibrationUse = 2
	if options.calibrationCacheMaxInterval <= 1 {
		entry.NextCalibrationUse = 1
	}
	return entry
}

func workflowManagedBorrowedSuccessStreak(sourceStreak int, similarity float64, splitFitScore float64) int {
	if sourceStreak <= 1 {
		return 1
	}
	if splitFitScore <= 0 {
		splitFitScore = 0.75
	}
	confidence := similarity * splitFitScore
	if confidence < 0.25 {
		confidence = 0.25
	}
	inherited := int(float64(sourceStreak) * confidence)
	if float64(inherited) < float64(sourceStreak)*confidence {
		inherited++
	}
	if inherited < 1 {
		return 1
	}
	if inherited > sourceStreak {
		return sourceStreak
	}
	return inherited
}

func workflowManagedCalibrationSimilarityScore(
	entry workflowManagedCalibrationCacheEntry,
	identity map[string]any,
) float64 {
	if entry.Strategy == "" || entry.Strategy != stringMapValue(identity, "strategy") {
		return 0
	}
	var score float64
	add := func(weight float64, value float64) {
		if value < 0 {
			value = 0
		}
		if value > 1 {
			value = 1
		}
		score += weight * value
	}
	add(0.16, 1)
	add(
		0.14,
		workflowExactSimilarity(entry.PlanShapeHash, stringMapValue(identity, "plan_shape_hash")),
	)
	add(0.08, workflowCountSimilarity(entry.ScopeCount, intFromAny(identity["scope_count"])))
	add(0.08, workflowCountSimilarity(entry.TaskCount, intFromAny(identity["task_count"])))
	add(0.10, workflowSetSimilarity(entry.Languages, stringSliceMapValue(identity, "languages")))
	add(
		0.08,
		workflowSetSimilarity(entry.Repositories, stringSliceMapValue(identity, "repositories")),
	)
	add(
		0.10,
		workflowExactOrChangedSimilarity(
			entry.OutputSchemaHash,
			stringMapValue(identity, "output_schema_hash"),
			0.2,
		),
	)
	add(0.08, workflowExactOrChangedSimilarity(entry.Model, stringMapValue(identity, "model"), 0.5))
	add(
		0.07,
		workflowExactOrChangedSimilarity(
			entry.TaskHash,
			stringMapValue(identity, "task_hash"),
			workflowCountSimilarity(entry.TaskCount, intFromAny(identity["task_count"]))*0.45,
		),
	)
	add(
		0.05,
		workflowExactOrChangedSimilarity(entry.PromptHash, stringMapValue(identity, "prompt_hash"), 0.4),
	)
	add(
		0.03,
		workflowExactOrChangedSimilarity(entry.ContextHash, stringMapValue(identity, "context_hash"), 0.35),
	)
	add(0.03, workflowExactSimilarity(entry.ChunkingHash, stringMapValue(identity, "chunking_hash")))
	return score
}

func workflowExactSimilarity(left string, right string) float64 {
	if left == "" || right == "" {
		return 0
	}
	if left == right {
		return 1
	}
	return 0
}

func workflowExactOrChangedSimilarity(left string, right string, changedScore float64) float64 {
	if left == "" || right == "" {
		return 0
	}
	if left == right {
		return 1
	}
	return changedScore
}

func workflowCountSimilarity(left int, right int) float64 {
	if left == right {
		return 1
	}
	if left <= 0 || right <= 0 {
		return 0
	}
	minValue := min(left, right)
	maxValue := max(left, right)
	return float64(minValue) / float64(maxValue)
}

func workflowSetSimilarity(left []string, right []string) float64 {
	if len(left) == 0 && len(right) == 0 {
		return 1
	}
	if len(left) == 0 || len(right) == 0 {
		return 0.4
	}
	leftSet := make(map[string]struct{}, len(left))
	for _, value := range left {
		value = strings.TrimSpace(value)
		if value != "" {
			leftSet[value] = struct{}{}
		}
	}
	if len(leftSet) == 0 {
		return 0.4
	}
	overlap := 0
	for _, value := range right {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := leftSet[value]; ok {
			overlap++
		}
	}
	union := len(leftSet)
	for _, value := range right {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := leftSet[value]; !ok {
			union++
		}
	}
	if union == 0 {
		return 0.4
	}
	return float64(overlap) / float64(union)
}

func workflowManagedSplitFitScore(
	req workflows.AgentRequest,
	options workflowManagedExecutionOptions,
	plans []workflowManagedChildPlan,
) float64 {
	if len(plans) <= 1 {
		return 0.5
	}
	unsplitPromptTokens := workflows.EstimateAgentPayloadTokens(workflowAgentMessage(req))
	childPromptTokens := make([]int, 0, len(plans))
	totalTokens := 0
	minTokens := 0
	maxTokens := 0
	for _, plan := range plans {
		childReq := workflowManagedApplyPlan(req, plan)
		tokens := workflows.EstimateAgentPayloadTokens(workflowAgentMessage(childReq))
		childPromptTokens = append(childPromptTokens, tokens)
		totalTokens += tokens
		if minTokens == 0 || tokens < minTokens {
			minTokens = tokens
		}
		if tokens > maxTokens {
			maxTokens = tokens
		}
	}
	score := 1.0
	if options.targetChildPromptTokens > 0 && maxTokens > options.targetChildPromptTokens {
		score *= float64(options.targetChildPromptTokens) / float64(maxTokens)
	}
	if unsplitPromptTokens > 0 && totalTokens > unsplitPromptTokens {
		ratio := float64(totalTokens) / float64(unsplitPromptTokens)
		score *= 1 / (1 + (ratio-1)/2)
	}
	if maxTokens > 0 && minTokens > 0 {
		balance := float64(minTokens) / float64(maxTokens)
		score *= 0.75 + 0.25*balance
	}
	if options.targetChildPromptTokens > 0 && totalTokens/len(childPromptTokens) < options.targetChildPromptTokens/4 {
		score *= 0.85
	}
	if score < 0.1 {
		return 0.1
	}
	if score > 1 {
		return 1
	}
	return score
}

func workflowManagedScopeCacheDescriptorFor(scope any) workflowManagedScopeCacheDescriptor {
	items := workflowScopeItems(scope)
	languages := make(map[string]struct{})
	repositories := make(map[string]struct{})
	desc := workflowManagedScopeCacheDescriptor{
		Count: len(items),
		Items: make([]workflowManagedScopeItemCacheDescriptor, 0, len(items)),
	}
	if mapped, ok := scope.(map[string]any); ok {
		wrapper := make(map[string]any, len(mapped))
		for key, value := range mapped {
			if key == "items" {
				continue
			}
			wrapper[key] = value
		}
		if len(wrapper) > 0 {
			desc.WrapperHash = workflowManagedHash(wrapper)
			workflowManagedCollectRepositorySignals(mapped, repositories)
		}
	}
	for index, item := range items {
		itemDesc := workflowManagedScopeItemCacheDescriptorFor(index, item)
		desc.Items = append(desc.Items, itemDesc)
		if itemDesc.Language != "" {
			languages[itemDesc.Language] = struct{}{}
		}
		if itemDesc.Repository != "" {
			repositories[itemDesc.Repository] = struct{}{}
		}
	}
	desc.Languages = workflowManagedSortedSet(languages)
	desc.Repositories = workflowManagedSortedSet(repositories)
	return desc
}

func workflowManagedScopeItemCacheDescriptorFor(
	index int,
	item any,
) workflowManagedScopeItemCacheDescriptor {
	desc := workflowManagedScopeItemCacheDescriptor{
		Index: index,
		Type:  fmt.Sprintf("%T", item),
	}
	mapped, ok := item.(map[string]any)
	if !ok {
		desc.ValueHash = workflowManagedHash(item)
		return desc
	}
	desc.ID = workflowFirstStringMapValue(mapped, "id", "name")
	desc.Path = workflowFirstStringMapValue(
		mapped,
		"path",
		"file_path",
		"filePath",
		"filepath",
		"relative_path",
		"relativePath",
		"file",
	)
	desc.Language = workflowFirstStringMapValue(mapped, "language", "lang")
	if desc.Language == "" {
		desc.Language = workflowManagedLanguageFromPath(desc.Path)
	}
	desc.Repository = workflowFirstStringMapValue(
		mapped,
		"repository",
		"repo",
		"repo_root",
		"repoRoot",
		"working_directory",
		"workingDirectory",
		"workspace",
	)
	desc.Commit = workflowFirstStringMapValue(mapped, "commit", "commit_sha", "commitSha", "sha")
	desc.Hash = workflowFirstStringMapValue(mapped, "file_hash", "fileHash", "hash", "content_hash", "contentHash")
	desc.Size = intFromAny(mapped["size_bytes"])
	if desc.Size == 0 {
		desc.Size = intFromAny(mapped["sizeBytes"])
	}
	normalized := make(map[string]any, len(mapped))
	for key, value := range mapped {
		if key == "content" {
			if content, ok := value.(string); ok {
				desc.ContentHash = workflowManagedHashString(content)
				normalized["content_hash"] = desc.ContentHash
			}
			continue
		}
		normalized[key] = value
	}
	desc.ValueHash = workflowManagedHash(normalized)
	return desc
}

func workflowManagedCollectRepositorySignals(scope map[string]any, repositories map[string]struct{}) {
	for _, key := range []string{
		"repository",
		"repo",
		"repo_root",
		"repoRoot",
		"working_directory",
		"workingDirectory",
		"workspace",
	} {
		value := stringMapValue(scope, key)
		if value != "" {
			repositories[value] = struct{}{}
		}
	}
}

func workflowFirstStringMapValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringMapValue(values, key); value != "" {
			return value
		}
	}
	return ""
}

func workflowManagedLanguageFromPath(value string) string {
	ext := strings.TrimPrefix(strings.ToLower(pathpkg.Ext(value)), ".")
	if ext == "" {
		return ""
	}
	switch ext {
	case "js", "jsx", "mjs", "cjs":
		return "javascript"
	case "ts", "tsx", "mts", "cts":
		return "typescript"
	case "py":
		return "python"
	case "go":
		return "go"
	case "rb":
		return "ruby"
	case "rs":
		return "rust"
	case "java":
		return "java"
	case "kt", "kts":
		return "kotlin"
	case "cs":
		return "csharp"
	case "cpp", "cc", "cxx", "hpp", "hh", "hxx":
		return "cpp"
	case "c", "h":
		return "c"
	case "md", "mdx":
		return "markdown"
	case "yml", "yaml":
		return "yaml"
	case "json":
		return "json"
	default:
		return ext
	}
}

func workflowManagedSortedSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func stringSliceMapValue(values map[string]any, key string) []string {
	raw, ok := values[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			text := strings.TrimSpace(fmt.Sprint(item))
			if text != "" && text != "<nil>" {
				out = append(out, text)
			}
		}
		return out
	default:
		text := strings.TrimSpace(fmt.Sprint(raw))
		if text == "" || text == "<nil>" {
			return nil
		}
		return []string{text}
	}
}

func intSliceMapValue(values map[string]any, key string) []int {
	raw, ok := values[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []int:
		return append([]int(nil), v...)
	case []any:
		out := make([]int, 0, len(v))
		for _, item := range v {
			out = append(out, intFromAny(item))
		}
		return out
	default:
		return []int{intFromAny(raw)}
	}
}

func workflowManagedHash(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		encoded = []byte(fmt.Sprintf("%#v", value))
	}
	return workflowManagedHashBytes(encoded)
}

func workflowManagedHashString(value string) string {
	return workflowManagedHashBytes([]byte(value))
}

func workflowManagedHashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func workflowRunManagedChildren(
	req workflows.AgentRequest,
	agent *AgentInstance,
	cfg *config.Config,
	options workflowManagedExecutionOptions,
	strategy string,
	plans []workflowManagedChildPlan,
	runOnce workflowAgentTextRunner,
) []workflowManagedChildResult {
	if len(plans) == 0 {
		return nil
	}
	results := make([]workflowManagedChildResult, len(plans))
	maxParallel := options.maxParallelChildren
	if maxParallel <= 0 {
		maxParallel = 1
	}
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, plan := range plans {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			choice := workflowManagedRunChoice(req, agent, cfg, options, strategy, plan)
			childReq := workflowManagedApplyPlan(req, plan)
			childReq.Managed = "off"
			message := workflowManagedPlanMessage(childReq, plan, len(plans))
			modelOverride := ""
			if changed, _ := choice.modelMeta["changed"].(bool); changed {
				modelOverride = choice.modelName
			}
			text, structured, repairs, err := workflowRunStructuredAgentWithOptions(
				message,
				req.Output,
				runOnce,
				workflowAgentRunOptions{
					ModelName:       modelOverride,
					ReasoningEffort: choice.reasoningEffort,
					NoTools:         true,
				},
			)
			results[i] = workflowManagedChildResult{
				plan:       plan,
				choice:     choice,
				text:       text,
				structured: structured,
				repairs:    repairs,
				err:        err,
			}
		}()
	}
	wg.Wait()
	return results
}

func workflowStructuredAgentOutputs(
	text string,
	structured workflows.StructuredOutputResult,
	repairs int,
	agentID string,
	sessionKey string,
	historyMode string,
	cacheMode string,
	promptCacheKey string,
	messageID string,
) map[string]any {
	outputs := workflowAgentBaseOutputs(text, agentID, sessionKey, historyMode, cacheMode, promptCacheKey, messageID)
	outputs["structured_valid"] = structured.Valid
	outputs["structured_repairs"] = repairs
	if structured.RawJSON != "" {
		outputs["structured_json"] = structured.RawJSON
	}
	if structured.Structured != nil {
		outputs["structured"] = structured.Structured
	}
	if structured.Error != "" {
		outputs["structured_error"] = structured.Error
	}
	return outputs
}

func workflowManagedChildOutput(result workflowManagedChildResult) map[string]any {
	out := map[string]any{
		"index":          result.plan.index,
		"label":          result.plan.label,
		"scope_count":    len(result.plan.scope),
		"task_count":     len(result.plan.tasks),
		"tasks":          append([]string(nil), result.plan.tasks...),
		"text":           result.text,
		"valid":          result.structured.Valid,
		"repairs":        result.repairs,
		"model":          result.choice.modelMeta,
		"effort":         result.choice.effortMeta,
		"estimated_cost": result.choice.costMeta,
	}
	if result.structured.Structured != nil {
		out["structured"] = result.structured.Structured
	}
	if result.structured.Error != "" {
		out["error"] = result.structured.Error
	}
	if result.err != nil {
		out["run_error"] = result.err.Error()
	}
	return out
}

func workflowManagedSplitStrategy(req workflows.AgentRequest, agent *AgentInstance) string {
	options := workflowManagedOptions(req.Managed)
	if options.mode == "off" || req.Output == nil || !req.Output.Enabled() {
		return ""
	}
	requested := workflowNormalizeSplitStrategy(options.requestedSplitStrategy)
	if requested == "none" {
		return ""
	}
	tasks := workflowAssignedOrAgentTasks(req, agent)
	scopeSplittable := len(workflowManagedScopeChunks(req, options)) > 1
	taskSplittable := len(tasks) > options.maxTasksPerChunk && options.maxTasksPerChunk > 0
	switch requested {
	case "scope_split":
		if scopeSplittable {
			return requested
		}
	case "task_split":
		if taskSplittable {
			return requested
		}
	case "hybrid_split":
		if scopeSplittable && taskSplittable {
			return requested
		}
	case "":
		if scopeSplittable && taskSplittable {
			return "hybrid_split"
		}
		if scopeSplittable {
			return "scope_split"
		}
		if taskSplittable {
			return "task_split"
		}
	}
	return ""
}

func workflowNormalizeSplitStrategy(strategy string) string {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "", "auto":
		return ""
	case "none", "off":
		return "none"
	case "scope", "by_scope", "scope_split":
		return "scope_split"
	case "task", "tasks", "by_task", "task_split":
		return "task_split"
	case "hybrid", "both", "scope_task", "task_scope", "hybrid_split":
		return "hybrid_split"
	default:
		return strings.ToLower(strings.TrimSpace(strategy))
	}
}

func workflowStrategyUsesScope(strategy string) bool {
	strategy = workflowNormalizeSplitStrategy(strategy)
	return strategy == "scope_split" || strategy == "hybrid_split"
}

func workflowStrategyUsesTasks(strategy string) bool {
	strategy = workflowNormalizeSplitStrategy(strategy)
	return strategy == "task_split" || strategy == "hybrid_split"
}

func workflowManagedChildPlans(
	req workflows.AgentRequest,
	agent *AgentInstance,
	options workflowManagedExecutionOptions,
	strategy string,
) []workflowManagedChildPlan {
	strategy = workflowNormalizeSplitStrategy(strategy)
	scopeChunks := [][]any{nil}
	taskChunks := [][]string{nil}
	if workflowStrategyUsesScope(strategy) {
		scopeChunks = workflowManagedScopeChunks(req, options)
	}
	if workflowStrategyUsesTasks(strategy) {
		taskChunks = workflowChunkTasks(workflowAssignedOrAgentTasks(req, agent), options.maxTasksPerChunk)
	}
	if len(scopeChunks) == 0 {
		scopeChunks = [][]any{nil}
	}
	if len(taskChunks) == 0 {
		taskChunks = [][]string{nil}
	}
	plans := make([]workflowManagedChildPlan, 0, len(scopeChunks)*len(taskChunks))
	for si, scope := range scopeChunks {
		for ti, tasks := range taskChunks {
			labelParts := make([]string, 0, 2)
			if workflowStrategyUsesScope(strategy) {
				labelParts = append(labelParts, fmt.Sprintf("scope chunk %d of %d", si+1, len(scopeChunks)))
			}
			if workflowStrategyUsesTasks(strategy) {
				labelParts = append(labelParts, fmt.Sprintf("task chunk %d of %d", ti+1, len(taskChunks)))
			}
			plans = append(plans, workflowManagedChildPlan{
				index: len(plans) + 1,
				label: strings.Join(labelParts, ", "),
				scope: append([]any(nil), scope...),
				tasks: append([]string(nil), tasks...),
			})
		}
	}
	return plans
}

func workflowManagedSplitMetadata(
	req workflows.AgentRequest,
	agent *AgentInstance,
	options workflowManagedExecutionOptions,
	strategy string,
	plans []workflowManagedChildPlan,
) map[string]any {
	scopeCounts := make([]int, 0, len(plans))
	taskCounts := make([]int, 0, len(plans))
	childPromptTokens := make([]int, 0, len(plans))
	for _, plan := range plans {
		scopeCounts = append(scopeCounts, len(plan.scope))
		taskCounts = append(taskCounts, len(plan.tasks))
		childReq := workflowManagedApplyPlan(req, plan)
		childPromptTokens = append(
			childPromptTokens,
			workflows.EstimateAgentPayloadTokens(workflowAgentMessage(childReq)),
		)
	}
	return map[string]any{
		"status":                "split",
		"strategy":              strategy,
		"child_count":           len(plans),
		"max_items_per_chunk":   options.maxItemsPerChunk,
		"max_tasks_per_chunk":   options.maxTasksPerChunk,
		"max_parallel_children": options.maxParallelChildren,
		"adaptive_chunking":     options.adaptiveChunking,
		"scope_count":           len(workflowScopeItems(req.Scope)),
		"task_count":            len(workflowAssignedOrAgentTasks(req, agent)),
		"child_scope_counts":    scopeCounts,
		"child_task_counts":     taskCounts,
		"token_efficiency": workflowManagedTokenEfficiency(
			workflows.EstimateAgentPayloadTokens(workflowAgentMessage(req)),
			childPromptTokens,
			options.targetChildPromptTokens,
		),
		"hidden_child_runs":    true,
		"visible_result_count": 1,
	}
}

func workflowManagedScopeChunks(req workflows.AgentRequest, options workflowManagedExecutionOptions) [][]any {
	scope := workflowScopeItems(req.Scope)
	if len(scope) == 0 {
		return nil
	}
	if !options.adaptiveChunking || options.targetChildPromptTokens <= 0 {
		return workflowChunkScope(scope, options.maxItemsPerChunk)
	}
	return workflowChunkScopeByPromptTokens(req, scope, options.maxItemsPerChunk, options.targetChildPromptTokens)
}

func workflowChunkScopeByPromptTokens(
	req workflows.AgentRequest,
	scope []any,
	maxItems int,
	targetPromptTokens int,
) [][]any {
	if len(scope) == 0 {
		return nil
	}
	if maxItems <= 0 {
		maxItems = len(scope)
	}
	if targetPromptTokens <= 0 {
		return workflowChunkScope(scope, maxItems)
	}
	chunks := make([][]any, 0, (len(scope)+maxItems-1)/maxItems)
	current := make([]any, 0, maxItems)
	for _, item := range scope {
		candidate := append(append([]any(nil), current...), item)
		overCount := len(candidate) > maxItems
		overTokens := len(current) > 0 && workflowScopeChunkPromptTokens(req, candidate) > targetPromptTokens
		if overCount || overTokens {
			chunks = append(chunks, append([]any(nil), current...))
			current = []any{item}
			continue
		}
		current = candidate
	}
	if len(current) > 0 {
		chunks = append(chunks, append([]any(nil), current...))
	}
	return chunks
}

func workflowScopeChunkPromptTokens(req workflows.AgentRequest, scope []any) int {
	childReq := req
	childReq.Scope = workflowScopeForPlan(req.Scope, scope)
	return workflows.EstimateAgentPayloadTokens(workflowAgentMessage(childReq))
}

func workflowManagedTokenEfficiency(
	unsplitPromptTokens int,
	childPromptTokens []int,
	targetChildPromptTokens int,
) map[string]any {
	total := 0
	maxTokens := 0
	for _, tokens := range childPromptTokens {
		total += tokens
		if tokens > maxTokens {
			maxTokens = tokens
		}
	}
	overhead := total - unsplitPromptTokens
	out := map[string]any{
		"unsplit_prompt_tokens":        unsplitPromptTokens,
		"target_child_prompt_tokens":   targetChildPromptTokens,
		"child_prompt_tokens":          append([]int(nil), childPromptTokens...),
		"max_child_prompt_tokens":      maxTokens,
		"total_child_prompt_tokens":    total,
		"estimated_overhead_tokens":    overhead,
		"estimated_over_split":         overhead > 0 && len(childPromptTokens) > 1,
		"estimated_under_target_split": maxTokens <= targetChildPromptTokens,
	}
	if unsplitPromptTokens > 0 {
		out["split_to_unsplit_ratio"] = float64(total) / float64(unsplitPromptTokens)
	}
	return out
}

func workflowManagedCalibrationRequest(
	req workflows.AgentRequest,
	agent *AgentInstance,
	options workflowManagedExecutionOptions,
	strategy string,
) workflows.AgentRequest {
	scopeItems := workflowScopeItems(req.Scope)
	tasks := workflowAssignedOrAgentTasks(req, agent)
	scopeLimit := len(scopeItems)
	if workflowStrategyUsesScope(strategy) {
		scopeLimit = options.calibrationSampleSize
		if scopeLimit <= 0 || scopeLimit > len(scopeItems) {
			scopeLimit = len(scopeItems)
		}
	}
	taskLimit := len(tasks)
	if workflowStrategyUsesTasks(strategy) {
		taskLimit = options.calibrationTaskSampleSize
		if taskLimit <= 0 || taskLimit > len(tasks) {
			taskLimit = len(tasks)
		}
	}
	buildSample := func() workflows.AgentRequest {
		sample := req
		if workflowStrategyUsesScope(strategy) && scopeLimit > 0 {
			sample.Scope = workflowScopeForPlan(req.Scope, append([]any(nil), scopeItems[:scopeLimit]...))
		}
		if workflowStrategyUsesTasks(strategy) && taskLimit > 0 {
			sample = workflowManagedApplyTasks(sample, tasks[:taskLimit])
		}
		return sample
	}
	sample := buildSample()
	for len(workflowManagedChildPlans(sample, agent, options.withoutOptimization(), strategy)) <= 1 {
		expanded := false
		if workflowStrategyUsesScope(strategy) && scopeLimit < len(scopeItems) {
			scopeLimit++
			expanded = true
		}
		if workflowStrategyUsesTasks(strategy) && taskLimit < len(tasks) {
			taskLimit++
			expanded = true
		}
		if !expanded {
			return sample
		}
		sample = buildSample()
	}
	return sample
}

func workflowManagedApplyPlan(req workflows.AgentRequest, plan workflowManagedChildPlan) workflows.AgentRequest {
	out := req
	if plan.scope != nil {
		out.Scope = workflowScopeForPlan(req.Scope, plan.scope)
	}
	if len(plan.tasks) > 0 {
		out = workflowManagedApplyTasks(out, plan.tasks)
	}
	return out
}

func workflowManagedApplyTasks(req workflows.AgentRequest, tasks []string) workflows.AgentRequest {
	if len(tasks) == 0 {
		return req
	}
	taskMessage := workflowManagedTaskMessage(tasks)
	if strings.TrimSpace(req.Context) == "" {
		req.Context = taskMessage
	} else {
		req.Context = strings.TrimSpace(req.Context) + "\n\n" + taskMessage
	}
	return req
}

func workflowManagedPlanMessage(req workflows.AgentRequest, plan workflowManagedChildPlan, total int) string {
	return strings.Join([]string{
		fmt.Sprintf("Agent execution optimization child task %d of %d.", plan.index, total),
		"Work only on the assigned scope and textual task subset. Preserve the requested structured output contract.",
		"Do not perform write actions from a hidden managed child run; produce proposed results only.",
		workflowAgentMessage(req),
	}, "\n\n")
}

func workflowManagedTaskMessage(tasks []string) string {
	var b strings.Builder
	b.WriteString("Assigned textual agent tasks:\n")
	for _, task := range tasks {
		task = strings.TrimSpace(task)
		if task == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(task)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func workflowAssignedOrAgentTasks(req workflows.AgentRequest, agent *AgentInstance) []string {
	if tasks := workflowAssignedTasks(req); len(tasks) > 0 {
		return tasks
	}
	return workflowAgentTasks(agent)
}

func workflowAssignedTasks(req workflows.AgentRequest) []string {
	marker := "Assigned textual agent tasks:"
	context := req.Context
	idx := strings.LastIndex(context, marker)
	if idx < 0 {
		return nil
	}
	lines := strings.Split(context[idx+len(marker):], "\n")
	tasks := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(tasks) > 0 {
				break
			}
			continue
		}
		task, ok := parseAgentTaskBullet(line)
		if !ok {
			if len(tasks) > 0 {
				break
			}
			continue
		}
		tasks = append(tasks, task)
	}
	return tasks
}

func workflowAgentTasks(agent *AgentInstance) []string {
	if agent == nil || agent.Definition.Agent == nil {
		return nil
	}
	tasks := make([]string, 0, len(agent.Definition.Agent.Tasks))
	for _, task := range agent.Definition.Agent.Tasks {
		task = strings.TrimSpace(task)
		if task != "" {
			tasks = append(tasks, task)
		}
	}
	return tasks
}

func workflowScopeForPlan(original any, scope []any) any {
	if originalMap, ok := original.(map[string]any); ok {
		out := make(map[string]any, len(originalMap))
		for key, value := range originalMap {
			out[key] = value
		}
		out["items"] = append([]any(nil), scope...)
		return out
	}
	return append([]any(nil), scope...)
}

func workflowChunkTasks(tasks []string, maxItems int) [][]string {
	if len(tasks) == 0 {
		return nil
	}
	if maxItems <= 0 {
		maxItems = len(tasks)
	}
	chunks := make([][]string, 0, (len(tasks)+maxItems-1)/maxItems)
	for start := 0; start < len(tasks); start += maxItems {
		end := start + maxItems
		if end > len(tasks) {
			end = len(tasks)
		}
		chunks = append(chunks, append([]string(nil), tasks[start:end]...))
	}
	return chunks
}

func (options workflowManagedExecutionOptions) withoutOptimization() workflowManagedExecutionOptions {
	options.modelOptimization = false
	options.effortOptimization = false
	options.modelCandidates = nil
	return options
}

func parseWorkflowManagedModelCandidates(raw any) []workflowManagedModelCandidate {
	switch v := raw.(type) {
	case nil:
		return nil
	case []any:
		out := make([]workflowManagedModelCandidate, 0, len(v))
		for _, item := range v {
			if candidate := parseWorkflowManagedModelCandidate(item); candidate.name != "" {
				out = append(out, candidate)
			}
		}
		return out
	default:
		if candidate := parseWorkflowManagedModelCandidate(v); candidate.name != "" {
			return []workflowManagedModelCandidate{candidate}
		}
	}
	return nil
}

func parseWorkflowManagedModelCandidate(raw any) workflowManagedModelCandidate {
	switch v := raw.(type) {
	case string:
		return workflowManagedModelCandidate{name: strings.TrimSpace(v), source: "managed_option"}
	case map[string]any:
		name := stringMapValue(v, "name")
		if name == "" {
			name = stringMapValue(v, "model")
		}
		candidate := workflowManagedModelCandidate{
			name:                name,
			inputPricePerMTok:   floatFromAny(v["input_price_per_1m"]),
			outputPricePerMTok:  floatFromAny(v["output_price_per_1m"]),
			subscription:        boolFromAny(v["subscription"]),
			equivalentModelName: stringMapValue(v, "subscription_equivalent_model"),
			source:              "managed_option",
		}
		if candidate.inputPricePerMTok > 0 || candidate.outputPricePerMTok > 0 {
			candidate.priceKnown = true
		}
		return candidate
	default:
		return workflowManagedModelCandidate{}
	}
}

func stringMapValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func workflowManagedRunChoice(
	req workflows.AgentRequest,
	agent *AgentInstance,
	cfg *config.Config,
	options workflowManagedExecutionOptions,
	strategy string,
	plan workflowManagedChildPlan,
) workflowManagedExecutionChoice {
	childReq := workflowManagedApplyPlan(req, plan)
	message := workflowManagedPlanMessage(childReq, plan, 1)
	inputTokens := workflows.EstimateAgentPayloadTokens(message)
	outputTokens := options.estimatedOutputTokens
	modelName := ""
	if agent != nil {
		modelName = strings.TrimSpace(agent.Model)
	}
	current := workflowModelCandidateProfile(cfg, modelName)
	candidateProfiles := workflowManagedCandidateProfiles(cfg, options.modelCandidates)
	for _, candidate := range candidateProfiles {
		if candidate.name == modelName {
			current = candidate
			break
		}
	}
	selected := current
	if selected.name == "" {
		selected.name = modelName
	}
	selected.name = modelName
	reason := "model optimization disabled"
	if options.modelOptimization {
		reason = "no cheaper configured model with known price"
		best := selected
		if !best.priceKnown {
			best.inputPricePerMTok = 1 << 30
			best.outputPricePerMTok = 1 << 30
		}
		availabilityLimited := false
		for _, candidate := range candidateProfiles {
			if candidate.name == "" || !candidate.priceKnown {
				continue
			}
			if !workflowManagedModelCandidateAvailable(cfg, agent, modelName, candidate.name) {
				availabilityLimited = true
				continue
			}
			if workflowEstimatedCost(
				candidate,
				inputTokens,
				outputTokens,
			) < workflowEstimatedCost(
				best,
				inputTokens,
				outputTokens,
			) {
				best = candidate
			}
		}
		if best.name != "" && best.name != selected.name {
			selected = best
			reason = "selected lowest known estimated cost for child run"
		} else if availabilityLimited {
			reason = "no cheaper configured model with initialized provider"
		}
	}
	effort := ""
	effortReason := "effort optimization disabled"
	if options.effortOptimization {
		effort = workflowOptimizedReasoningEffort(inputTokens, len(plan.tasks), len(plan.scope))
		effortReason = "selected from child prompt size, scope count, and task count"
	}
	return workflowManagedExecutionChoice{
		modelName:       selected.name,
		reasoningEffort: effort,
		modelMeta: map[string]any{
			"selected":            selected.name,
			"default":             modelName,
			"changed":             selected.name != "" && selected.name != modelName,
			"reason":              reason,
			"price_source":        selected.source,
			"price_known":         selected.priceKnown,
			"subscription":        selected.subscription,
			"equivalent_model":    selected.equivalentModelName,
			"input_price_per_1m":  selected.inputPricePerMTok,
			"output_price_per_1m": selected.outputPricePerMTok,
		},
		effortMeta: map[string]any{
			"selected": effort,
			"changed":  effort != "",
			"reason":   effortReason,
		},
		costMeta: workflowManagedCostMeta(selected, current, inputTokens, outputTokens),
	}
}

func workflowManagedModelCandidateAvailable(
	cfg *config.Config,
	agent *AgentInstance,
	defaultModelName string,
	candidateName string,
) bool {
	candidateName = strings.TrimSpace(candidateName)
	if candidateName == "" {
		return false
	}
	if candidateName == strings.TrimSpace(defaultModelName) {
		return true
	}
	if cfg == nil || agent == nil {
		return false
	}
	modelCfg, err := resolvedModelConfig(cfg, candidateName, agent.Workspace)
	if err != nil {
		return false
	}
	protocol, modelID := providers.ExtractProtocol(modelCfg)
	return agent.candidateProvider(providers.ModelKey(protocol, modelID)) != nil
}

func workflowManagedCandidateProfiles(
	cfg *config.Config,
	candidates []workflowManagedModelCandidate,
) []workflowManagedModelCandidate {
	out := make([]workflowManagedModelCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if fromConfig := workflowModelCandidateProfile(cfg, candidate.name); fromConfig.name != "" {
			if !candidate.priceKnown {
				candidate.inputPricePerMTok = fromConfig.inputPricePerMTok
				candidate.outputPricePerMTok = fromConfig.outputPricePerMTok
				candidate.priceKnown = fromConfig.priceKnown
				candidate.source = fromConfig.source
			}
			if candidate.equivalentModelName == "" {
				candidate.equivalentModelName = fromConfig.equivalentModelName
			}
			candidate.subscription = candidate.subscription || fromConfig.subscription
		}
		out = append(out, candidate)
	}
	return out
}

func workflowModelCandidateProfile(cfg *config.Config, name string) workflowManagedModelCandidate {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" {
		return workflowManagedModelCandidate{name: name}
	}
	mc := lookupModelConfigByRef(cfg, name, cfg.Agents.Defaults.Provider)
	if mc == nil {
		return workflowManagedModelCandidate{name: name}
	}
	candidate := workflowManagedModelCandidate{
		name:                strings.TrimSpace(mc.ModelName),
		inputPricePerMTok:   mc.InputPricePerMTok,
		outputPricePerMTok:  mc.OutputPricePerMTok,
		subscription:        mc.Subscription,
		equivalentModelName: strings.TrimSpace(mc.SubscriptionEquivalentModel),
		source:              "model_config",
	}
	if candidate.name == "" {
		candidate.name = name
	}
	if candidate.subscription && candidate.equivalentModelName != "" {
		equiv := workflowModelCandidateProfile(cfg, candidate.equivalentModelName)
		if equiv.priceKnown {
			candidate.inputPricePerMTok = equiv.inputPricePerMTok
			candidate.outputPricePerMTok = equiv.outputPricePerMTok
			candidate.source = "subscription_equivalent_model_config"
		}
	}
	candidate.priceKnown = candidate.inputPricePerMTok > 0 || candidate.outputPricePerMTok > 0
	return candidate
}

func workflowManagedCostMeta(
	selected workflowManagedModelCandidate,
	baseline workflowManagedModelCandidate,
	inputTokens int,
	outputTokens int,
) map[string]any {
	meta := map[string]any{
		"input_tokens":            inputTokens,
		"estimated_output_tokens": outputTokens,
		"selected_price_known":    selected.priceKnown,
		"baseline_price_known":    baseline.priceKnown,
		"selected_model":          selected.name,
		"baseline_model":          baseline.name,
	}
	if selected.priceKnown {
		meta["selected_usd"] = workflowEstimatedCost(selected, inputTokens, outputTokens)
	}
	if baseline.priceKnown {
		meta["baseline_usd"] = workflowEstimatedCost(baseline, inputTokens, outputTokens)
	}
	if selected.priceKnown && baseline.priceKnown {
		meta["estimated_savings_usd"] = workflowEstimatedCost(baseline, inputTokens, outputTokens) -
			workflowEstimatedCost(selected, inputTokens, outputTokens)
	}
	return meta
}

func workflowManagedOptimizationSummary(
	req workflows.AgentRequest,
	agent *AgentInstance,
	cfg *config.Config,
	options workflowManagedExecutionOptions,
	results []workflowManagedChildResult,
) map[string]any {
	modelCounts := make(map[string]int)
	effortCounts := make(map[string]int)
	totalSelectedCost := 0.0
	totalBaselineCost := 0.0
	selectedCostKnown := 0
	baselineCostKnown := 0
	changedModel := false
	changedEffort := false
	for _, result := range results {
		if result.choice.modelName != "" {
			modelCounts[result.choice.modelName]++
		}
		if result.choice.reasoningEffort != "" {
			effortCounts[result.choice.reasoningEffort]++
			changedEffort = true
		}
		if changed, _ := result.choice.modelMeta["changed"].(bool); changed {
			changedModel = true
		}
		if cost, ok := result.choice.costMeta["selected_usd"].(float64); ok {
			totalSelectedCost += cost
			selectedCostKnown++
		}
		if cost, ok := result.choice.costMeta["baseline_usd"].(float64); ok {
			totalBaselineCost += cost
			baselineCostKnown++
		}
	}
	defaultModel := ""
	if agent != nil {
		defaultModel = strings.TrimSpace(agent.Model)
	}
	modelReason := "model optimization disabled"
	if options.modelOptimization {
		modelReason = "per-child cheapest known configured model after split calibration"
	}
	effortReason := "effort optimization disabled"
	if options.effortOptimization {
		effortReason = "per-child effort selected from estimated child complexity"
	}
	return map[string]any{
		"model": map[string]any{
			"enabled":         options.modelOptimization,
			"default":         defaultModel,
			"changed":         changedModel,
			"selected_counts": sortedStringIntMap(modelCounts),
			"reason":          modelReason,
		},
		"effort": map[string]any{
			"enabled":         options.effortOptimization,
			"changed":         changedEffort,
			"selected_counts": sortedStringIntMap(effortCounts),
			"reason":          effortReason,
		},
		"cost": map[string]any{
			"selected_known_children": selectedCostKnown,
			"baseline_known_children": baselineCostKnown,
			"selected_total_usd":      totalSelectedCost,
			"baseline_total_usd":      totalBaselineCost,
			"estimated_savings_usd":   totalBaselineCost - totalSelectedCost,
			"split_prompt_tokens":     workflows.EstimateAgentPayloadTokens(req),
			"config_models_seen":      len(workflowManagedCandidateProfiles(cfg, options.modelCandidates)),
		},
	}
}

func sortedStringIntMap(values map[string]int) []map[string]any {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]any{"value": key, "count": values[key]})
	}
	return out
}

func workflowEstimatedCost(candidate workflowManagedModelCandidate, inputTokens int, outputTokens int) float64 {
	return (float64(inputTokens)*candidate.inputPricePerMTok + float64(outputTokens)*candidate.outputPricePerMTok) / 1_000_000
}

func workflowOptimizedReasoningEffort(inputTokens int, taskCount int, scopeCount int) string {
	switch {
	case inputTokens <= 1200 && taskCount <= 1 && scopeCount <= 2:
		return "low"
	case inputTokens <= 4000 && taskCount <= 2 && scopeCount <= 8:
		return "low"
	case inputTokens <= 12000:
		return "medium"
	default:
		return "high"
	}
}

func floatFromAny(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		n, _ := v.Float64()
		return n
	case string:
		var n float64
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%f", &n); err == nil {
			return n
		}
	}
	return 0
}

func boolFromAny(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "on", "1":
			return true
		}
	}
	return false
}

func (r *workflowAgentRunner) ensureWorkflowManagedProviders(agent *AgentInstance, raw any) error {
	if r == nil || r.loop == nil || r.loop.cfg == nil || agent == nil {
		return nil
	}
	options := workflowManagedOptions(raw)
	if !options.modelOptimization || len(options.modelCandidates) == 0 {
		return nil
	}
	var failures []string
	for _, candidate := range options.modelCandidates {
		name := strings.TrimSpace(candidate.name)
		if name == "" || name == strings.TrimSpace(agent.Model) {
			continue
		}
		modelCfg, err := resolvedModelConfig(r.loop.cfg, name, agent.Workspace)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		protocol, modelID := providers.ExtractProtocol(modelCfg)
		key := providers.ModelKey(protocol, modelID)
		if agent.candidateProvider(key) != nil {
			continue
		}
		factory := r.loop.providerFactory
		if factory == nil {
			factory = providers.CreateProviderFromConfig
		}
		provider, _, err := factory(modelCfg)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		if !agent.setCandidateProviderIfAbsent(key, provider) {
			closeProviderIfStateful(provider)
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}
