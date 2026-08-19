package localci

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

func (accumulator *discoveryAccumulator) discoverExplicit(root string, file discoveredFile) error {
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
	if err = rejectUnknownYAMLKeys(values, "version", "steps"); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalid, file.path, err)
	}
	version, err := yamlInteger(values["version"])
	if err != nil || version != 1 {
		return fmt.Errorf("%w: %s requires version 1", ErrInvalid, file.path)
	}
	stepsNode := values["steps"]
	if stepsNode == nil || stepsNode.Kind != yaml.SequenceNode || len(stepsNode.Content) == 0 {
		return fmt.Errorf("%w: %s requires nonempty steps", ErrInvalid, file.path)
	}
	accumulator.addDefinition(file.path, raw)
	for index, node := range stepsNode.Content {
		step, stepErr := parseExplicitStep(node, file.path, index)
		if stepErr != nil {
			return stepErr
		}
		accumulator.addStep(step)
	}
	return nil
}

func parseExplicitStep(node *yaml.Node, source string, index int) (Step, error) {
	values, err := yamlMapping(node)
	if err != nil {
		return Step{}, fmt.Errorf("%w: %s step %d: %v", ErrInvalid, source, index, err)
	}
	if err = rejectUnknownYAMLKeys(
		values,
		"id", "name", "kind", "run", "command", "shell", "working-directory", "env", "timeout-seconds",
	); err != nil {
		return Step{}, fmt.Errorf("%w: %s step %d: %v", ErrInvalid, source, index, err)
	}
	id, err := yamlOptionalString(values, "id")
	if err != nil || strings.TrimSpace(id) == "" {
		return Step{}, fmt.Errorf("%w: %s step %d requires an ID", ErrInvalid, source, index)
	}
	name, err := yamlOptionalString(values, "name")
	if err != nil {
		return Step{}, fmt.Errorf("%w: %s step %d name: %v", ErrInvalid, source, index, err)
	}
	if strings.TrimSpace(name) == "" {
		name = id
	}
	kindValue, err := yamlOptionalString(values, "kind")
	if err != nil {
		return Step{}, fmt.Errorf("%w: %s step %d kind: %v", ErrInvalid, source, index, err)
	}
	kind := StepKind(kindValue)
	workingDirectory, err := yamlOptionalString(values, "working-directory")
	if err != nil {
		return Step{}, fmt.Errorf("%w: %s step %d directory: %v", ErrInvalid, source, index, err)
	}
	timeout := int64(defaultStepTimeoutSeconds)
	if timeoutNode := values["timeout-seconds"]; timeoutNode != nil {
		timeout, err = yamlInteger(timeoutNode)
		if err != nil {
			return Step{}, fmt.Errorf("%w: %s step %d timeout: %v", ErrInvalid, source, index, err)
		}
	}
	environment, err := yamlEnvironment(values["env"])
	if err != nil {
		return Step{}, fmt.Errorf("%w: %s step %d environment: %v", ErrInvalid, source, index, err)
	}
	step := Step{
		ID:               id,
		Name:             name,
		Kind:             kind,
		Origin:           OriginExplicit,
		Source:           source,
		WorkingDirectory: workingDirectory,
		Environment:      environment,
		TimeoutSeconds:   timeout,
		Required:         true,
	}
	runNode, commandNode := values["run"], values["command"]
	if (runNode == nil) == (commandNode == nil) {
		return Step{}, fmt.Errorf("%w: %s step %d requires exactly one invocation", ErrInvalid, source, index)
	}
	if runNode != nil {
		step.Script, err = yamlString(runNode)
		if err != nil {
			return Step{}, fmt.Errorf("%w: %s step %d run: %v", ErrInvalid, source, index, err)
		}
		step.Shell, err = yamlOptionalString(values, "shell")
		if err != nil {
			return Step{}, fmt.Errorf("%w: %s step %d shell: %v", ErrInvalid, source, index, err)
		}
		if step.Shell == "" {
			step.Shell = "sh"
		}
	} else {
		step.Argv, err = yamlStringSequence(commandNode)
		if err != nil {
			return Step{}, fmt.Errorf("%w: %s step %d command: %v", ErrInvalid, source, index, err)
		}
		if _, hasShell := values["shell"]; hasShell {
			return Step{}, fmt.Errorf("%w: %s step %d command cannot set shell", ErrInvalid, source, index)
		}
	}
	return step, nil
}
