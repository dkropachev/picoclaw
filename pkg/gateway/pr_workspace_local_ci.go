package gateway

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/prworkspace/localci"
)

const prWorkspaceLocalCIDirectory = "pr-workspace-local-ci"

// prWorkspaceLocalCIRuntime owns the process-local handles behind the durable
// local-CI evidence used by the PR workspace. The runner
// itself receives candidate roots only through gitworkspace's exact callback.
type prWorkspaceLocalCIRuntime struct {
	runner   *localci.Runner
	evidence *localci.FileEvidenceStore
}

func newPRWorkspaceLocalCIRuntime(
	cfg *config.Config,
) (*prWorkspaceLocalCIRuntime, error) {
	temporaryRoot, evidenceRoot, err := preparePRWorkspaceLocalCIState(cfg)
	if err != nil {
		return nil, err
	}
	evidence, err := localci.OpenFileEvidenceStore(evidenceRoot)
	if err != nil {
		return nil, fmt.Errorf("open PR workspace local CI evidence: %w", err)
	}
	sandbox, err := localci.NewSandbox(localci.SandboxConfig{
		TemporaryRoot: temporaryRoot,
	})
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("initialize PR workspace local CI sandbox: %w", err),
			evidence.Close(),
		)
	}
	return &prWorkspaceLocalCIRuntime{
		runner: &localci.Runner{
			Sandbox: sandbox,
			Store:   evidence,
			Limits:  localci.DefaultLimits(),
		},
		evidence: evidence,
	}, nil
}

// newPRWorkspaceLocalCIEvidenceRuntime opens only the durable evidence side
// of local CI. A parked review consumes already-persisted evidence and must not
// depend on the current host having a usable mutation-time sandbox.
func newPRWorkspaceLocalCIEvidenceRuntime(
	cfg *config.Config,
) (*prWorkspaceLocalCIRuntime, error) {
	_, evidenceRoot, err := preparePRWorkspaceLocalCIState(cfg)
	if err != nil {
		return nil, err
	}
	evidence, err := localci.OpenFileEvidenceStore(evidenceRoot)
	if err != nil {
		return nil, fmt.Errorf("open PR workspace local CI evidence: %w", err)
	}
	return &prWorkspaceLocalCIRuntime{evidence: evidence}, nil
}

func preparePRWorkspaceLocalCIState(
	cfg *config.Config,
) (string, string, error) {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return "", "", errors.New("PR workspace local CI requires event ingress")
	}
	ingress := config.EffectiveEventIngressConfig(cfg, cfg.WorkspacePath())
	stateRoot := filepath.Join(
		filepath.Dir(ingress.DatabasePath),
		prWorkspaceLocalCIDirectory,
	)
	temporaryRoot := filepath.Join(stateRoot, "tmp")
	evidenceRoot := filepath.Join(stateRoot, "evidence")
	for _, directory := range []string{stateRoot, temporaryRoot, evidenceRoot} {
		if err := ensurePrivatePRWorkspaceDirectory(directory); err != nil {
			return "", "", err
		}
	}
	return temporaryRoot, evidenceRoot, nil
}

func ensurePrivatePRWorkspaceDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve PR workspace local CI directory: %w", err)
	}
	if err = os.MkdirAll(absolute, 0o700); err != nil {
		return fmt.Errorf("create PR workspace local CI directory: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("PR workspace local CI directory must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		return fmt.Errorf("PR workspace local CI directory must be canonical")
	}
	if err = os.Chmod(absolute, 0o700); err != nil {
		return fmt.Errorf("secure PR workspace local CI directory: %w", err)
	}
	return nil
}

func (runtime *prWorkspaceLocalCIRuntime) Close() error {
	if runtime == nil || runtime.evidence == nil {
		return nil
	}
	return runtime.evidence.Close()
}
