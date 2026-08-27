package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/sipeed/picoclaw/pkg/config"
)

const agentApplyPatchTransactionStateDirectory = "apply_patch_transactions"

// agentApplyPatchTransactionStateRoot snapshots the process home-derived
// location once at AgentInstance construction. Factory products must receive
// this value instead of consulting the environment again.
func agentApplyPatchTransactionStateRoot() (string, error) {
	root := filepath.Join(config.GetHome(), agentApplyPatchTransactionStateDirectory)
	absolute, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve apply-patch transaction state root: %w", err)
	}
	return absolute, nil
}

// agentApplyPatchAdmissionSafe reports whether the enabled sibling filesystem
// tools are unable to read or mutate apply-patch's authenticated transaction
// state. Patterns are the already-compiled, detached effective policies that
// will be installed in the sibling factories.
func agentApplyPatchAdmissionSafe(
	defaults *config.AgentDefaults,
	cfg *config.Config,
	transactionStateRoot string,
	readPatterns []*regexp.Regexp,
	writePatterns []*regexp.Regexp,
) bool {
	if defaults == nil || cfg == nil {
		return false
	}
	readEnabled := cfg.Tools.IsToolEnabled("read_file") ||
		cfg.Tools.IsToolEnabled("list_dir") ||
		cfg.Tools.IsToolEnabled("message") && cfg.Tools.Message.MediaEnabled ||
		cfg.Tools.IsToolEnabled("send_file") ||
		cfg.Tools.IsToolEnabled("load_image")
	writeEnabled := cfg.Tools.IsToolEnabled("write_file") ||
		cfg.Tools.IsToolEnabled("edit_file") ||
		cfg.Tools.IsToolEnabled("append_file")

	if !defaults.RestrictToWorkspace && (readEnabled || writeEnabled) {
		return false
	}
	if defaults.AllowReadOutsideWorkspace && readEnabled {
		return false
	}
	if readEnabled && agentApplyPatchPatternsMayReachStateRoot(
		transactionStateRoot,
		readPatterns,
	) {
		return false
	}
	if writeEnabled && agentApplyPatchPatternsMayReachStateRoot(
		transactionStateRoot,
		writePatterns,
	) {
		return false
	}
	return true
}

// agentApplyPatchPatternsMayReachStateRoot accepts a pattern only when its
// anchored literal prefix proves that every possible match starts in a path
// namespace disjoint from the transaction root. Arbitrary, unanchored, or
// alternated patterns fail closed because sampling random transaction names
// cannot prove that no control descendant is admitted.
func agentApplyPatchPatternsMayReachStateRoot(
	transactionStateRoot string,
	patterns []*regexp.Regexp,
) bool {
	if !filepath.IsAbs(transactionStateRoot) {
		return true
	}
	root := agentApplyPatchAuthorityPathKey(transactionStateRoot)
	for _, pattern := range patterns {
		if pattern == nil || !strings.HasPrefix(pattern.String(), "^") {
			return true
		}
		// Removing a parsed leading begin-text assertion leaves a valid regexp.
		unanchored := regexp.MustCompile(strings.TrimPrefix(pattern.String(), "^"))
		prefix, _ := unanchored.LiteralPrefix()
		if prefix == "" || !filepath.IsAbs(prefix) {
			return true
		}
		prefix = agentApplyPatchAuthorityPathKey(prefix)
		if agentApplyPatchLiteralPrefixesMayOverlap(root, prefix) {
			return true
		}
	}
	return false
}

// agentApplyPatchAuthorityPathKey applies conservative filesystem-alias
// normalization before proving two authority prefixes disjoint. Treating
// case, Unicode normalization, and trailing-dot/space variants as aliases can
// omit apply_patch on a case-sensitive volume, but cannot expose its state on
// common case-insensitive Darwin or Windows filesystems.
func agentApplyPatchAuthorityPathKey(path string) string {
	cleaned := filepath.Clean(path)
	volume := filepath.VolumeName(cleaned)
	remainder := strings.TrimPrefix(cleaned, volume)
	components := strings.FieldsFunc(remainder, func(character rune) bool {
		return character == '/' || character == '\\'
	})
	normalized := components[:0]
	for index := range components {
		component := strings.TrimRight(components[index], " .")
		if component != "" {
			normalized = append(normalized, component)
		}
	}
	key := volume + string(os.PathSeparator) +
		strings.Join(normalized, string(os.PathSeparator))
	return norm.NFD.String(cases.Fold().String(key))
}

func agentApplyPatchLiteralPrefixesMayOverlap(root, prefix string) bool {
	if strings.HasPrefix(root, prefix) {
		return true
	}
	if !strings.HasPrefix(prefix, root) {
		return false
	}
	if os.IsPathSeparator(root[len(root)-1]) {
		return true
	}
	return os.IsPathSeparator(prefix[len(root)])
}

// agentApplyPatchProtectedRoots returns only control paths whose locations are
// authoritative at AgentInstance construction. The transaction state root is
// supplied separately through ApplyPatchPreflightPolicy.TransactionStateRoot,
// whose constructor adds it to the exact protected roots. Other runtime owners
// inject their own exact roots when they construct a tool.
func agentApplyPatchProtectedRoots(workspace string, cfg *config.Config) []string {
	roots := []string{
		filepath.Join(workspace, "sessions"),
		filepath.Join(workspace, "account_router_state.json"),
	}
	if cfg != nil {
		roots = append(roots, cfg.GitWorkspaceRootPath())
	}
	return roots
}
