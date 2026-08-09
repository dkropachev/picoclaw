package localci

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var localCIIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,255}$`)

func digestParts(domain string, parts ...[]byte) string {
	hash := sha256.New()
	writeDigestPart(hash, []byte(domain))
	for _, part := range parts {
		writeDigestPart(hash, part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type digestWriter interface {
	Write(payload []byte) (written int, err error)
}

func writeDigestPart(writer digestWriter, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = writer.Write(size[:])
	_, _ = writer.Write(value)
}

func digestJSON(domain string, value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode local CI digest payload: %w", err)
	}
	return digestParts(domain, encoded), nil
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func normalizePlan(plan Plan) (Plan, error) {
	plan.Steps = append([]Step(nil), plan.Steps...)
	plan.Diagnostics = append([]Diagnostic(nil), plan.Diagnostics...)
	plan.Version = PlanVersion
	plan.DiscoveryVersion = DiscoveryVersion
	plan.Digest = ""
	if !validDigest(plan.DefinitionDigest) || !validDigest(plan.DependencyDigest) {
		return Plan{}, fmt.Errorf("%w: plan input digest is invalid", ErrInvalid)
	}
	if len(plan.Steps) > maximumPlanSteps {
		return Plan{}, fmt.Errorf("%w: plan contains too many steps", ErrInvalid)
	}
	if len(plan.Diagnostics) > maximumPlanDiagnostics {
		return Plan{}, fmt.Errorf("%w: plan contains too many diagnostics", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(plan.Steps))
	for index := range plan.Steps {
		step, err := normalizeStep(plan.Steps[index])
		if err != nil {
			return Plan{}, err
		}
		if _, exists := seen[step.ID]; exists {
			return Plan{}, fmt.Errorf("%w: duplicate step ID", ErrInvalid)
		}
		seen[step.ID] = struct{}{}
		plan.Steps[index] = step
	}
	if plan.Complete && len(plan.Steps) == 0 {
		return Plan{}, fmt.Errorf("%w: complete plan contains no steps", ErrInvalid)
	}
	for index := range plan.Diagnostics {
		diagnostic, err := normalizeDiagnostic(plan.Diagnostics[index])
		if err != nil {
			return Plan{}, err
		}
		plan.Diagnostics[index] = diagnostic
	}
	slices.SortFunc(plan.Diagnostics, func(left, right Diagnostic) int {
		if compared := strings.Compare(left.Code, right.Code); compared != 0 {
			return compared
		}
		if compared := strings.Compare(left.Source, right.Source); compared != 0 {
			return compared
		}
		return strings.Compare(left.Detail, right.Detail)
	})
	digest, err := digestJSON("picoclaw-local-ci-plan-v1", plan)
	if err != nil {
		return Plan{}, err
	}
	plan.Digest = digest
	return plan, nil
}

func normalizeStep(step Step) (Step, error) {
	step.Argv = append([]string(nil), step.Argv...)
	step.Environment = append([]EnvironmentVariable(nil), step.Environment...)
	step.ID = strings.TrimSpace(step.ID)
	step.Name = strings.TrimSpace(step.Name)
	step.Source = strings.TrimSpace(step.Source)
	step.WorkingDirectory = strings.TrimSpace(step.WorkingDirectory)
	step.Shell = strings.TrimSpace(step.Shell)
	if !localCIIDPattern.MatchString(step.ID) || step.Name == "" || step.Source == "" ||
		!utf8.ValidString(step.Name) || !utf8.ValidString(step.Source) ||
		len(step.Name) > 256 || len(step.Source) > 4096 {
		return Step{}, fmt.Errorf("%w: invalid step identity", ErrInvalid)
	}
	if !validStepKind(step.Kind) || !validStepOrigin(step.Origin) || !step.Required {
		return Step{}, fmt.Errorf("%w: invalid required step classification", ErrInvalid)
	}
	if !validLocalDirectory(step.WorkingDirectory) {
		return Step{}, fmt.Errorf("%w: invalid step working directory", ErrInvalid)
	}
	if (len(step.Argv) == 0) == (step.Script == "") {
		return Step{}, fmt.Errorf("%w: step requires exactly one invocation", ErrInvalid)
	}
	if len(step.Script) > maximumCommandLen || !utf8.ValidString(step.Script) ||
		strings.ContainsRune(step.Script, 0) {
		return Step{}, fmt.Errorf("%w: invalid step script", ErrInvalid)
	}
	if step.Script != "" && step.Shell != "sh" && step.Shell != "bash" {
		return Step{}, fmt.Errorf("%w: invalid step shell", ErrInvalid)
	}
	if len(step.Argv) > 64 {
		return Step{}, fmt.Errorf("%w: step has too many arguments", ErrInvalid)
	}
	for _, argument := range step.Argv {
		if argument == "" || len(argument) > 8192 || !utf8.ValidString(argument) ||
			strings.ContainsRune(argument, 0) {
			return Step{}, fmt.Errorf("%w: invalid step argument", ErrInvalid)
		}
	}
	if step.TimeoutSeconds < 1 || step.TimeoutSeconds > int64((30*time.Minute)/time.Second) {
		return Step{}, fmt.Errorf("%w: invalid step timeout", ErrInvalid)
	}
	if len(step.Environment) > 64 {
		return Step{}, fmt.Errorf("%w: too many step environment values", ErrInvalid)
	}
	for index := range step.Environment {
		variable := step.Environment[index]
		variable.Name = strings.TrimSpace(variable.Name)
		if !validEnvironmentName(variable.Name) || len(variable.Value) > 8192 ||
			!utf8.ValidString(variable.Value) || strings.ContainsRune(variable.Value, 0) {
			return Step{}, fmt.Errorf("%w: invalid step environment", ErrInvalid)
		}
		step.Environment[index] = variable
	}
	slices.SortFunc(step.Environment, func(left, right EnvironmentVariable) int {
		return strings.Compare(left.Name, right.Name)
	})
	for index := 1; index < len(step.Environment); index++ {
		if step.Environment[index-1].Name == step.Environment[index].Name {
			return Step{}, fmt.Errorf("%w: duplicate step environment name", ErrInvalid)
		}
	}
	return step, nil
}

func normalizeDiagnostic(diagnostic Diagnostic) (Diagnostic, error) {
	diagnostic.Code = strings.TrimSpace(diagnostic.Code)
	diagnostic.Source = strings.TrimSpace(diagnostic.Source)
	diagnostic.Detail = strings.TrimSpace(diagnostic.Detail)
	if !localCIIDPattern.MatchString(diagnostic.Code) || len(diagnostic.Source) > 4096 ||
		len(diagnostic.Detail) > 8192 || !utf8.ValidString(diagnostic.Source) ||
		!utf8.ValidString(diagnostic.Detail) {
		return Diagnostic{}, fmt.Errorf("%w: invalid plan diagnostic", ErrInvalid)
	}
	return diagnostic, nil
}

func validLocalDirectory(value string) bool {
	if value == "" || value == "." {
		return true
	}
	if len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsRune(value, '\\') ||
		strings.ContainsRune(value, 0) || !filepath.IsLocal(filepath.FromSlash(value)) || path.Clean(value) != value {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if strings.EqualFold(segment, ".git") {
			return false
		}
	}
	return true
}

func validStepKind(kind StepKind) bool {
	return kind == StepLint || kind == StepTest || kind == StepBuild || kind == StepCheck
}

func validStepOrigin(origin StepOrigin) bool {
	return origin == OriginExplicit || origin == OriginGitHubAction ||
		origin == OriginMake || origin == OriginPackage || origin == OriginLanguage
}

func validEnvironmentName(value string) bool {
	if value == "" || len(value) > 128 || reservedEnvironmentName(value) {
		return false
	}
	for index, character := range value {
		if character == '_' || character >= 'A' && character <= 'Z' ||
			index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func reservedEnvironmentName(value string) bool {
	switch value {
	case "PATH", "HOME", "TMPDIR",
		"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "GOCACHE",
		"LANG", "LC_ALL", "TZ", "CI", "NO_COLOR",
		"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG", "GIT_CONFIG_COUNT",
		"GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT", "GIT_SSH", "GIT_SSH_COMMAND",
		"PICOCLAW_LOCAL_CI":
		return true
	}
	return strings.HasPrefix(value, "GIT_CONFIG_KEY_") ||
		strings.HasPrefix(value, "GIT_CONFIG_VALUE_") ||
		strings.HasSuffix(value, "_PROXY") ||
		strings.HasSuffix(value, "_TOKEN") ||
		strings.HasSuffix(value, "_PASSWORD") ||
		strings.HasSuffix(value, "_SECRET") ||
		strings.HasSuffix(value, "_CREDENTIALS")
}
