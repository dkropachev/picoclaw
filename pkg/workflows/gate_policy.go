package workflows

import (
	"fmt"

	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

// GatePolicyMode selects how one repository policy combines with the global
// policy for the same decision point.
type GatePolicyMode = gatetypes.GatePolicyMode

const (
	GatePolicyInherit = gatetypes.GatePolicyInherit
	GatePolicyOverlay = gatetypes.GatePolicyOverlay
	GatePolicyReplace = gatetypes.GatePolicyReplace
	GatePolicyDisable = gatetypes.GatePolicyDisable
)

// RepositoryGatePolicy is one already-selected repository override. Policy
// persistence and trusted repository lookup are deliberately outside this
// resolver.
type RepositoryGatePolicy = gatetypes.RepositoryGatePolicy

// GatePolicyResolutionAction explains how one effective gate was selected.
type GatePolicyResolutionAction string

const (
	GatePolicyResolutionInherited  GatePolicyResolutionAction = "inherited"
	GatePolicyResolutionReplaced   GatePolicyResolutionAction = "replaced"
	GatePolicyResolutionTombstoned GatePolicyResolutionAction = "tombstoned"
	GatePolicyResolutionAppended   GatePolicyResolutionAction = "appended"
	GatePolicyResolutionSelected   GatePolicyResolutionAction = "selected"
)

// GatePolicyResolutionEntry is a one-based, stable provenance record for one
// effective gate. A zero effective gate still has a position even though it
// later emits no workflow step.
type GatePolicyResolutionEntry struct {
	ID                 string                     `json:"id"`
	Action             GatePolicyResolutionAction `json:"action"`
	GlobalPosition     int                        `json:"global_position,omitempty"`
	RepositoryPosition int                        `json:"repository_position,omitempty"`
	EffectivePosition  int                        `json:"effective_position"`
}

// GatePolicyResolution contains the detached effective gate list and the
// provenance needed by later configuration and simulation surfaces.
type GatePolicyResolution struct {
	Mode      GatePolicyMode              `json:"mode"`
	Effective []GateSpec                  `json:"effective"`
	Entries   []GatePolicyResolutionEntry `json:"entries"`
}

// ResolveGatePolicy deterministically combines one ordered global gate policy
// with one optional, already-selected repository policy.
//
// Overlay mode replaces same-ID global gates in place and appends new IDs in
// repository order. Replacing an inherited nonzero gate with a same-ID zero
// gate is a tombstone at this policy boundary; the zero remains in Effective
// and is only an identity when CompileGateWorkflow consumes the result.
// Resolution success covers source validity and merge structure only;
// CompileGateWorkflow still owns subject-path and aggregate invocation-input
// validation.
func ResolveGatePolicy(
	global []GateSpec,
	repository *RepositoryGatePolicy,
) (*GatePolicyResolution, error) {
	globalCopy, err := cloneGatePolicyLayer("global", global)
	if err != nil {
		return nil, err
	}

	if repository == nil {
		return inheritedGatePolicyResolution(globalCopy), nil
	}

	switch repository.Mode {
	case GatePolicyInherit:
		if len(repository.Gates) != 0 {
			return nil, fmt.Errorf("repository inherit policy cannot configure gates")
		}
		return inheritedGatePolicyResolution(globalCopy), nil
	case GatePolicyDisable:
		if len(repository.Gates) != 0 {
			return nil, fmt.Errorf("repository disable policy cannot configure gates")
		}
		return &GatePolicyResolution{
			Mode:      GatePolicyDisable,
			Effective: []GateSpec{},
			Entries:   []GatePolicyResolutionEntry{},
		}, nil
	case GatePolicyOverlay, GatePolicyReplace:
		if len(repository.Gates) == 0 {
			return nil, fmt.Errorf("repository %s policy requires at least one gate", repository.Mode)
		}
	default:
		return nil, fmt.Errorf("repository gate policy mode %q is unsupported", repository.Mode)
	}

	repositoryCopy, err := cloneGatePolicyLayer("repository.gates", repository.Gates)
	if err != nil {
		return nil, err
	}
	if repository.Mode == GatePolicyReplace {
		entries := make([]GatePolicyResolutionEntry, len(repositoryCopy))
		for index, spec := range repositoryCopy {
			entries[index] = GatePolicyResolutionEntry{
				ID:                 spec.ID,
				Action:             GatePolicyResolutionSelected,
				RepositoryPosition: index + 1,
				EffectivePosition:  index + 1,
			}
		}
		return &GatePolicyResolution{
			Mode:      GatePolicyReplace,
			Effective: repositoryCopy,
			Entries:   entries,
		}, nil
	}

	effective := globalCopy
	entries := make([]GatePolicyResolutionEntry, 0, len(globalCopy)+len(repositoryCopy))
	positions := make(map[string]int, len(globalCopy))
	for index, spec := range globalCopy {
		positions[spec.ID] = index
		entries = append(entries, GatePolicyResolutionEntry{
			ID:                spec.ID,
			Action:            GatePolicyResolutionInherited,
			GlobalPosition:    index + 1,
			EffectivePosition: index + 1,
		})
	}
	for repositoryIndex, spec := range repositoryCopy {
		if effectiveIndex, exists := positions[spec.ID]; exists {
			action := GatePolicyResolutionReplaced
			if effective[effectiveIndex].Kind != GateZero && spec.Kind == GateZero {
				action = GatePolicyResolutionTombstoned
			}
			effective[effectiveIndex] = spec
			entries[effectiveIndex] = GatePolicyResolutionEntry{
				ID:                 spec.ID,
				Action:             action,
				GlobalPosition:     effectiveIndex + 1,
				RepositoryPosition: repositoryIndex + 1,
				EffectivePosition:  effectiveIndex + 1,
			}
			continue
		}
		if len(effective) == MaxWorkflowGateCount {
			return nil, fmt.Errorf("effective gate policy exceeds %d gates", MaxWorkflowGateCount)
		}
		positions[spec.ID] = len(effective)
		effective = append(effective, spec)
		entries = append(entries, GatePolicyResolutionEntry{
			ID:                 spec.ID,
			Action:             GatePolicyResolutionAppended,
			RepositoryPosition: repositoryIndex + 1,
			EffectivePosition:  len(effective),
		})
	}
	if err := validateGatePolicyWorkingAgents("effective", effective); err != nil {
		return nil, err
	}
	return &GatePolicyResolution{
		Mode:      GatePolicyOverlay,
		Effective: effective,
		Entries:   entries,
	}, nil
}

func inheritedGatePolicyResolution(global []GateSpec) *GatePolicyResolution {
	entries := make([]GatePolicyResolutionEntry, len(global))
	for index, spec := range global {
		entries[index] = GatePolicyResolutionEntry{
			ID:                spec.ID,
			Action:            GatePolicyResolutionInherited,
			GlobalPosition:    index + 1,
			EffectivePosition: index + 1,
		}
	}
	return &GatePolicyResolution{
		Mode:      GatePolicyInherit,
		Effective: global,
		Entries:   entries,
	}
}

func cloneGatePolicyLayer(label string, specs []GateSpec) ([]GateSpec, error) {
	if len(specs) > MaxWorkflowGateCount {
		return nil, fmt.Errorf("%s exceeds %d gates", label, MaxWorkflowGateCount)
	}
	cloned := make([]GateSpec, len(specs))
	seen := make(map[string]struct{}, len(specs))
	for index, source := range specs {
		path := fmt.Sprintf("%s[%d]", label, index)
		if err := validateWorkflowGateSpec(path, source); err != nil {
			return nil, err
		}
		if _, exists := seen[source.ID]; exists {
			return nil, fmt.Errorf("%s gate %q is duplicated", label, source.ID)
		}
		seen[source.ID] = struct{}{}
		cloned[index] = source
		if source.Questions == nil {
			continue
		}
		questions, err := normalizeWorkflowGateValue(
			path+".questions",
			source.Questions,
			MaxWorkflowGateQuestionBytes,
		)
		if err != nil {
			return nil, err
		}
		if source.Kind == GateDeterministic && questions == nil {
			return nil, fmt.Errorf("%s.questions are required", path)
		}
		cloned[index].Questions = questions
	}
	if err := validateGatePolicyWorkingAgents(label, cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func validateGatePolicyWorkingAgents(label string, specs []GateSpec) error {
	agentID := ""
	for _, spec := range specs {
		if spec.Kind != GateAIWorkingContext {
			continue
		}
		if agentID != "" && agentID != spec.AgentID {
			return fmt.Errorf(
				"%s working-context gates must use one session-owning agent; got %q and %q",
				label,
				agentID,
				spec.AgentID,
			)
		}
		agentID = spec.AgentID
	}
	return nil
}
