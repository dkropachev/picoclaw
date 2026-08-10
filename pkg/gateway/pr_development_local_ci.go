package gateway

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/prdevelopment/localci"
)

const prDevelopmentLocalCIDirectory = "pr-development-local-ci"

// prDevelopmentLocalCIRuntime owns the process-local handles behind the
// durable local-CI evidence used by the PR development controller. The runner
// itself receives candidate roots only through gitworkspace's exact callback.
type prDevelopmentLocalCIRuntime struct {
	runner   *localci.Runner
	evidence *localci.FileEvidenceStore
}

func newPRDevelopmentLocalCIRuntime(
	cfg *config.Config,
) (*prDevelopmentLocalCIRuntime, error) {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return nil, errors.New("PR development local CI requires event ingress")
	}
	ingress := config.EffectiveEventIngressConfig(cfg, cfg.WorkspacePath())
	stateRoot := filepath.Join(
		filepath.Dir(ingress.DatabasePath),
		prDevelopmentLocalCIDirectory,
	)
	temporaryRoot := filepath.Join(stateRoot, "tmp")
	evidenceRoot := filepath.Join(stateRoot, "evidence")
	for _, directory := range []string{stateRoot, temporaryRoot, evidenceRoot} {
		if err := ensurePrivatePRDevelopmentDirectory(directory); err != nil {
			return nil, err
		}
	}
	evidence, err := localci.OpenFileEvidenceStore(evidenceRoot)
	if err != nil {
		return nil, fmt.Errorf("open PR development local CI evidence: %w", err)
	}
	sandbox, err := localci.NewSandbox(localci.SandboxConfig{
		TemporaryRoot: temporaryRoot,
	})
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("initialize PR development local CI sandbox: %w", err),
			evidence.Close(),
		)
	}
	return &prDevelopmentLocalCIRuntime{
		runner: &localci.Runner{
			Sandbox: sandbox,
			Store:   evidence,
			Limits:  localci.DefaultLimits(),
		},
		evidence: evidence,
	}, nil
}

func ensurePrivatePRDevelopmentDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve PR development local CI directory: %w", err)
	}
	if err = os.MkdirAll(absolute, 0o700); err != nil {
		return fmt.Errorf("create PR development local CI directory: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("PR development local CI directory must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		return fmt.Errorf("PR development local CI directory must be canonical")
	}
	if err = os.Chmod(absolute, 0o700); err != nil {
		return fmt.Errorf("secure PR development local CI directory: %w", err)
	}
	return nil
}

func (runtime *prDevelopmentLocalCIRuntime) Close() error {
	if runtime == nil || runtime.evidence == nil {
		return nil
	}
	return runtime.evidence.Close()
}
