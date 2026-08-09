package localci

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type githubJobNode struct {
	id   string
	node *yaml.Node
}

func (accumulator *discoveryAccumulator) discoverGitHubWorkflow(root string, file discoveredFile) error {
	raw, err := readDiscoveryFile(root, file.path, maximumDefinitionFileSize)
	if err != nil {
		return err
	}
	rootNode, err := decodeStrictYAML(raw)
	if err != nil {
		return fmt.Errorf("%w: parse %s: %v", ErrInvalid, file.path, err)
	}
	values, err := yamlMapping(rootNode)
	if err != nil {
		return fmt.Errorf("%w: parse %s: %v", ErrInvalid, file.path, err)
	}
	pullRequest, pullRequestTarget, err := githubWorkflowTriggers(values["on"])
	if err != nil {
		return fmt.Errorf("%w: parse %s triggers: %v", ErrInvalid, file.path, err)
	}
	if pullRequestTarget {
		accumulator.addDefinition(file.path, raw)
		accumulator.addDiagnostic(Diagnostic{
			Code:   "unsafe_pull_request_target",
			Source: file.path,
			Detail: "pull_request_target workflows are not local candidate validation",
		})
		return nil
	}
	if !pullRequest {
		return nil
	}
	accumulator.addDefinition(file.path, raw)
	if keyErr := rejectUnknownYAMLKeys(values, "name", "on", "permissions", "jobs"); keyErr != nil {
		accumulator.addGitHubDiagnostic("unsupported_workflow_key", file.path, keyErr.Error())
	}
	jobsNode := values["jobs"]
	if jobsNode == nil || jobsNode.Kind != yaml.MappingNode {
		accumulator.addDiagnostic(Diagnostic{
			Code: "invalid_workflow_jobs", Source: file.path, Detail: "pull-request workflow has no jobs mapping",
		})
		return nil
	}
	jobs := make([]githubJobNode, 0, len(jobsNode.Content)/2)
	for index := 0; index < len(jobsNode.Content); index += 2 {
		jobs = append(jobs, githubJobNode{
			id:   jobsNode.Content[index].Value,
			node: jobsNode.Content[index+1],
		})
	}
	slices.SortFunc(jobs, func(left, right githubJobNode) int {
		return strings.Compare(left.id, right.id)
	})
	for _, job := range jobs {
		if err = accumulator.discoverGitHubJob(file.path, job); err != nil {
			return err
		}
	}
	return nil
}

func githubWorkflowTriggers(node *yaml.Node) (bool, bool, error) {
	if node == nil {
		return false, false, nil
	}
	switch node.Kind {
	case yaml.ScalarNode:
		value, err := yamlString(node)
		return value == "pull_request", value == "pull_request_target", err
	case yaml.SequenceNode:
		var pullRequest, target bool
		for _, child := range node.Content {
			value, err := yamlString(child)
			if err != nil {
				return false, false, err
			}
			pullRequest = pullRequest || value == "pull_request"
			target = target || value == "pull_request_target"
		}
		return pullRequest, target, nil
	case yaml.MappingNode:
		values, err := yamlMapping(node)
		if err != nil {
			return false, false, err
		}
		_, pullRequest := values["pull_request"]
		_, target := values["pull_request_target"]
		return pullRequest, target, nil
	default:
		return false, false, fmt.Errorf("unsupported on shape")
	}
}

func (accumulator *discoveryAccumulator) discoverGitHubJob(source string, job githubJobNode) error {
	values, err := yamlMapping(job.node)
	if err != nil {
		return accumulator.rejectGitHub(
			"unsupported_job",
			source,
			job.id+": job must be a mapping",
		)
	}
	if keyErr := rejectUnknownYAMLKeys(
		values,
		"name", "runs-on", "permissions", "if", "env", "defaults", "steps",
	); keyErr != nil {
		return accumulator.rejectGitHub("unsupported_job_key", source, job.id+": "+keyErr.Error())
	}
	for _, key := range []string{"uses", "strategy", "services", "container"} {
		if values[key] != nil {
			return accumulator.rejectGitHub(
				"unsupported_job_"+key,
				source,
				job.id+": "+key+" is unsupported",
			)
		}
	}
	condition, conditionErr := yamlOptionalString(values, "if")
	if conditionErr != nil || !staticGitHubCondition(condition) {
		return accumulator.rejectGitHub(
			"dynamic_job_condition",
			source,
			job.id+": dynamic if is unsupported",
		)
	}
	runner, runnerErr := yamlOptionalString(values, "runs-on")
	if runnerErr != nil || (!strings.HasPrefix(runner, "ubuntu-") && runner != "linux") {
		return accumulator.rejectGitHub(
			"unsupported_runner",
			source,
			job.id+": only a static Linux runner is supported",
		)
	}
	jobEnvironment, environmentErr := yamlEnvironment(values["env"])
	if environmentErr != nil {
		return accumulator.rejectGitHub(
			"dynamic_job_environment",
			source,
			job.id+": "+environmentErr.Error(),
		)
	}
	workingDirectory, defaultsErr := githubDefaultWorkingDirectory(values["defaults"])
	if defaultsErr != nil {
		return accumulator.rejectGitHub(
			"unsupported_job_defaults",
			source,
			job.id+": "+defaultsErr.Error(),
		)
	}
	stepsNode := values["steps"]
	if stepsNode == nil || stepsNode.Kind != yaml.SequenceNode {
		return accumulator.rejectGitHub(
			"unsupported_job_steps",
			source,
			job.id+": steps must be a sequence",
		)
	}
	firstExecutable := len(accumulator.steps)
	for index, stepNode := range stepsNode.Content {
		if err = accumulator.discoverGitHubStep(
			source,
			job.id,
			index,
			stepNode,
			workingDirectory,
			jobEnvironment,
		); err != nil {
			return err
		}
	}
	if len(accumulator.steps)-firstExecutable > 1 {
		// Each local step intentionally receives a fresh filesystem and process
		// sandbox. A GitHub job, in contrast, shares state between its run steps.
		// Refuse to silently change those semantics until jobs can be executed as
		// one bounded session.
		accumulator.steps = accumulator.steps[:firstExecutable]
		accumulator.addGitHubDiagnostic(
			"stateful_job_unsupported",
			source,
			job.id+": multiple executable steps require shared job state",
		)
	}
	return accumulator.err
}

func (accumulator *discoveryAccumulator) discoverGitHubStep(
	source, jobID string,
	index int,
	node *yaml.Node,
	jobDirectory string,
	jobEnvironment []EnvironmentVariable,
) error {
	values, err := yamlMapping(node)
	if err != nil {
		return accumulator.rejectGitHub(
			"unsupported_step",
			source,
			fmt.Sprintf("%s step %d is not a mapping", jobID, index),
		)
	}
	if keyErr := rejectUnknownYAMLKeys(
		values,
		"id", "name", "if", "env", "run", "uses", "with", "shell", "working-directory",
		"timeout-minutes", "continue-on-error",
	); keyErr != nil {
		return accumulator.rejectGitHub(
			"unsupported_step_key",
			source,
			fmt.Sprintf("%s step %d: %v", jobID, index, keyErr),
		)
	}
	condition, err := yamlOptionalString(values, "if")
	if err != nil || !staticGitHubCondition(condition) {
		return accumulator.rejectGitHub(
			"dynamic_step_condition",
			source,
			fmt.Sprintf("%s step %d has dynamic if", jobID, index),
		)
	}
	if staticGitHubConditionFalse(condition) {
		return nil
	}
	name, _ := yamlOptionalString(values, "name")
	if name == "" {
		name = fmt.Sprintf("%s step %d", jobID, index+1)
	}
	if optional, exists := values["continue-on-error"]; exists {
		continueOnError, boolErr := yamlBool(optional)
		if boolErr != nil || continueOnError {
			return accumulator.rejectGitHub(
				"unsupported_continue_on_error",
				source,
				name+": continue-on-error must be false",
			)
		}
	}
	timeoutSeconds := int64(defaultStepTimeoutSeconds)
	if timeoutNode := values["timeout-minutes"]; timeoutNode != nil {
		timeoutMinutes, timeoutErr := yamlInteger(timeoutNode)
		if timeoutErr != nil || timeoutMinutes < 1 || timeoutMinutes > 30 {
			return accumulator.rejectGitHub(
				"unsupported_step_timeout",
				source,
				name+": timeout-minutes must be between 1 and 30",
			)
		}
		timeoutSeconds = timeoutMinutes * 60
	}
	uses, usesErr := yamlOptionalString(values, "uses")
	if usesErr != nil {
		return accumulator.rejectGitHub("unsupported_action", source, name+": uses must be a string")
	}
	runNode := values["run"]
	if (uses == "") == (runNode == nil) {
		return accumulator.rejectGitHub(
			"unsupported_step_invocation",
			source,
			name+": exactly one of uses or run is required",
		)
	}
	if uses != "" {
		if values["shell"] != nil || values["working-directory"] != nil {
			return accumulator.rejectGitHub(
				"unsupported_action_shape",
				source,
				name+": action cannot set shell or working-directory",
			)
		}
		stepEnvironment, environmentErr := yamlEnvironment(values["env"])
		if environmentErr != nil {
			return accumulator.rejectGitHub(
				"dynamic_step_environment",
				source,
				name+": "+environmentErr.Error(),
			)
		}
		return accumulator.discoverGitHubAction(
			source,
			jobID,
			index,
			name,
			uses,
			values,
			mergeEnvironment(jobEnvironment, stepEnvironment),
			timeoutSeconds,
		)
	}
	if values["with"] != nil {
		return accumulator.rejectGitHub("unsupported_run_shape", source, name+": run step cannot set with")
	}
	script, err := yamlString(runNode)
	if err != nil || script == "" || strings.Contains(script, "${{") {
		return accumulator.rejectGitHub(
			"dynamic_run_script",
			source,
			name+": run must be a nonempty literal script",
		)
	}
	shell, err := yamlOptionalString(values, "shell")
	if err != nil || strings.Contains(shell, "${{") {
		return accumulator.rejectGitHub("unsupported_shell", source, name+": shell must be static")
	}
	if shell == "" {
		shell = "bash"
	}
	shellFields := strings.Fields(shell)
	if len(shellFields) != 1 {
		return accumulator.rejectGitHub("unsupported_shell", source, name+": shell must be sh or bash")
	}
	shell = shellFields[0]
	if shell != "sh" && shell != "bash" {
		return accumulator.rejectGitHub("unsupported_shell", source, name+": only sh and bash are supported")
	}
	workingDirectory, err := yamlOptionalString(values, "working-directory")
	if err != nil || strings.Contains(workingDirectory, "${{") {
		return accumulator.rejectGitHub(
			"dynamic_working_directory",
			source,
			name+": working directory must be static",
		)
	}
	if workingDirectory == "" {
		workingDirectory = jobDirectory
	}
	stepEnvironment, err := yamlEnvironment(values["env"])
	if err != nil {
		return accumulator.rejectGitHub("dynamic_step_environment", source, name+": "+err.Error())
	}
	accumulator.addStep(Step{
		ID:               stepID(OriginGitHubAction, source, fmt.Sprintf("%s/%d", jobID, index)),
		Name:             name,
		Kind:             classifyStepKind(name + "\n" + script),
		Origin:           OriginGitHubAction,
		Source:           source,
		WorkingDirectory: workingDirectory,
		Script:           script,
		Shell:            shell,
		Environment:      mergeEnvironment(jobEnvironment, stepEnvironment),
		TimeoutSeconds:   timeoutSeconds,
		Required:         true,
	})
	return nil
}

func (accumulator *discoveryAccumulator) discoverGitHubAction(
	source, jobID string,
	index int,
	name, uses string,
	values map[string]*yaml.Node,
	environment []EnvironmentVariable,
	timeoutSeconds int64,
) error {
	if strings.Contains(uses, "${{") || !validPinnedGitHubAction(uses) {
		accumulator.addGitHubDiagnostic("dynamic_action", source, name+": action identity must be pinned")
		return nil
	}
	if strings.HasPrefix(uses, "actions/checkout@") {
		if values["with"] != nil {
			return accumulator.rejectGitHub(
				"unsupported_action_input",
				source,
				name+": checkout inputs can change candidate semantics",
			)
		}
		accumulator.addDiagnostic(Diagnostic{
			Code: "passive_action", Source: source, Detail: name + ": actions/checkout",
		})
		return nil
	}
	if strings.HasPrefix(uses, "golangci/golangci-lint-action@") {
		arguments := []string{"golangci-lint", "run"}
		if withNode := values["with"]; withNode != nil {
			withValues, err := yamlMapping(withNode)
			if err != nil {
				return accumulator.rejectGitHub(
					"unsupported_action_input",
					source,
					name+": with must be static",
				)
			}
			if inputErr := rejectUnknownYAMLKeys(withValues, "args"); inputErr != nil {
				return accumulator.rejectGitHub(
					"unsupported_action_input",
					source,
					name+": "+inputErr.Error(),
				)
			}
			if rawArguments, exists := withValues["args"]; exists {
				argumentText, argumentErr := yamlString(rawArguments)
				if argumentErr != nil || strings.Contains(argumentText, "${{") ||
					strings.ContainsAny(argumentText, "'\"\\") {
					return accumulator.rejectGitHub(
						"unsupported_action_input",
						source,
						name+": args must be static",
					)
				}
				arguments = append(arguments, strings.Fields(argumentText)...)
			}
		}
		accumulator.addStep(Step{
			ID:             stepID(OriginGitHubAction, source, fmt.Sprintf("%s/%d", jobID, index)),
			Name:           name,
			Kind:           StepLint,
			Origin:         OriginGitHubAction,
			Source:         source,
			Argv:           arguments,
			Environment:    environment,
			TimeoutSeconds: timeoutSeconds,
			Required:       true,
		})
		return nil
	}
	accumulator.addGitHubDiagnostic("unsupported_action", source, name+": "+strings.SplitN(uses, "@", 2)[0])
	return nil
}

func validPinnedGitHubAction(value string) bool {
	identity, revision, found := strings.Cut(value, "@")
	if !found || identity == "" || strings.ContainsRune(identity, '@') || len(revision) != 40 ||
		strings.ToLower(revision) != revision {
		return false
	}
	for _, character := range revision {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	segments := strings.Split(identity, "/")
	return len(segments) == 2 && segments[0] != "" && segments[1] != "" &&
		!strings.HasPrefix(identity, ".")
}

func githubDefaultWorkingDirectory(node *yaml.Node) (string, error) {
	if node == nil {
		return "", nil
	}
	values, err := yamlMapping(node)
	if err != nil {
		return "", err
	}
	if err = rejectUnknownYAMLKeys(values, "run"); err != nil {
		return "", err
	}
	runNode := values["run"]
	if runNode == nil {
		return "", nil
	}
	runValues, err := yamlMapping(runNode)
	if err != nil {
		return "", err
	}
	if err = rejectUnknownYAMLKeys(runValues, "working-directory"); err != nil {
		return "", err
	}
	workingDirectory, err := yamlOptionalString(runValues, "working-directory")
	if err != nil || strings.Contains(workingDirectory, "${{") {
		return "", fmt.Errorf("working directory must be a static string")
	}
	return workingDirectory, nil
}

func staticGitHubCondition(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || strings.EqualFold(value, "true") || strings.EqualFold(value, "false") ||
		strings.EqualFold(value, "${{ true }}") || strings.EqualFold(value, "${{ false }}")
}

func staticGitHubConditionFalse(value string) bool {
	value = strings.TrimSpace(value)
	return strings.EqualFold(value, "false") || strings.EqualFold(value, "${{ false }}")
}

func classifyStepKind(value string) StepKind {
	value = strings.ToLower(value)
	switch {
	case strings.Contains(value, "lint"), strings.Contains(value, "vet"), strings.Contains(value, "format"):
		return StepLint
	case strings.Contains(value, "test"), strings.Contains(value, "coverage"):
		return StepTest
	case strings.Contains(value, "build"), strings.Contains(value, "compile"):
		return StepBuild
	default:
		return StepCheck
	}
}

func mergeEnvironment(base, override []EnvironmentVariable) []EnvironmentVariable {
	values := make(map[string]string, len(base)+len(override))
	for _, variable := range base {
		values[variable.Name] = variable.Value
	}
	for _, variable := range override {
		values[variable.Name] = variable.Value
	}
	result := make([]EnvironmentVariable, 0, len(values))
	for name, value := range values {
		result = append(result, EnvironmentVariable{Name: name, Value: value})
	}
	return result
}

func (accumulator *discoveryAccumulator) addGitHubDiagnostic(code, source, detail string) {
	accumulator.addDiagnostic(Diagnostic{
		Code: code, Source: source, Detail: detail,
	})
}

func (accumulator *discoveryAccumulator) rejectGitHub(code, source, detail string) error {
	accumulator.addGitHubDiagnostic(code, source, detail)
	return accumulator.err
}
