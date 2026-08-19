package localci

import (
	"context"
	"crypto/rand"
	"fmt"
	"path"
	"strings"
	"time"
)

type DependencyMount struct {
	Source string
	Target string
	Digest string
}

type SandboxConfig struct {
	TemporaryRoot    string
	BubblewrapPath   string
	SystemdRunPath   string
	SystemctlPath    string
	DependencyMounts []DependencyMount
}

type Sandbox interface {
	EnvironmentDigest(ctx context.Context, plan Plan) (string, error)
	RunStep(ctx context.Context, candidateRoot string, step Step, limits Limits) (StepResult, error)
	PassingCacheAllowed() bool
	localCISandbox()
}

func NewSandbox(config SandboxConfig) (Sandbox, error) {
	var generation [32]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return nil, fmt.Errorf("create local CI sandbox generation: %w", err)
	}
	for _, mount := range config.DependencyMounts {
		if strings.TrimSpace(mount.Source) != mount.Source || mount.Source == "" ||
			!validDependencyTarget(mount.Target) ||
			!validDigest(mount.Digest) {
			return nil, fmt.Errorf("%w: invalid read-only dependency mount", ErrInvalid)
		}
	}
	return newPlatformSandbox(config, generation)
}

func validDependencyTarget(target string) bool {
	if strings.TrimSpace(target) != target || path.Clean(target) != target ||
		!strings.HasPrefix(target, "/dependencies/") {
		return false
	}
	name := strings.TrimPrefix(target, "/dependencies/")
	if name == "" || len(name) > 64 || strings.ContainsRune(name, '/') || name == "." || name == ".." {
		return false
	}
	for _, character := range name {
		if character == '-' || character == '_' || character == '.' ||
			character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func normalizeLimits(limits Limits) Limits {
	defaults := DefaultLimits()
	if limits.StepTimeout <= 0 {
		limits.StepTimeout = defaults.StepTimeout
	}
	if limits.StepTimeout > 30*time.Minute {
		limits.StepTimeout = 30 * time.Minute
	}
	if limits.TotalTimeout <= 0 {
		limits.TotalTimeout = defaults.TotalTimeout
	}
	if limits.TotalTimeout > 30*time.Minute {
		limits.TotalTimeout = 30 * time.Minute
	}
	if limits.OutputBytes <= 0 {
		limits.OutputBytes = defaults.OutputBytes
	}
	if limits.OutputBytes > 4<<20 {
		limits.OutputBytes = 4 << 20
	}
	return limits
}
