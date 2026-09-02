// Package storecatalog builds the trusted physical-store inventory used by the
// public logical catalog and by privileged database infrastructure. It is
// internal so application packages cannot turn a logical store ID into a path.
package storecatalog

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
)

const databaseFilename = "store.db"

// Spec is a provider-only catalog record. Physical and legacy paths must never
// be projected into application-facing APIs.
type Spec struct {
	ID          string
	Domain      string
	Path        string
	LegacyRoots []string
	Required    bool
}

// Catalog is immutable after Build returns.
type Catalog struct {
	Home  string
	Specs []Spec
	byID  map[string]int
}

// Build resolves every path from a canonical home and a validated config. It
// rejects path aliases, symlinks, non-regular generations, and physical store
// collisions before any SQLite connection is opened.
func Build(home string, cfg *config.Config) (*Catalog, error) {
	return build(home, cfg, true)
}

// Project derives the same trusted physical identities without inspecting any
// database generation member. It exists only for filesystem protection policy;
// broker startup and maintenance must use Build.
func Project(home string, cfg *config.Config) (*Catalog, error) {
	return build(home, cfg, false)
}

func build(home string, cfg *config.Config, inspectGenerations bool) (*Catalog, error) {
	canonicalHome, err := canonicalDirectoryPath(home)
	if err != nil {
		return nil, fmt.Errorf("database catalog home: %w", err)
	}
	if cfg == nil {
		cfg = &config.Config{}
	}

	workspace, err := resolveWorkspace(canonicalHome, cfg.Agents.Defaults.Workspace)
	if err != nil {
		return nil, fmt.Errorf("database catalog workspace: %w", err)
	}

	var specs []Spec
	add := func(spec Spec) {
		specs = append(specs, spec)
	}
	add(Spec{
		ID: "global.auth", Domain: "auth", Path: filepath.Join(canonicalHome, "auth.db"),
		LegacyRoots: []string{filepath.Join(canonicalHome, "auth.json")}, Required: true,
	})
	add(Spec{
		ID: "launcher.auth", Domain: "launcher-auth", Path: filepath.Join(canonicalHome, "launcher-auth.db"),
		LegacyRoots: []string{launcherConfigLegacyPath(canonicalHome)}, Required: true,
	})
	add(Spec{
		ID: "global.model-catalogs", Domain: "model-catalogs",
		Path:        filepath.Join(canonicalHome, "model-catalogs.db"),
		LegacyRoots: []string{filepath.Join(canonicalHome, "model_catalogs.json")},
	})
	add(Spec{
		ID: "global.tool-adaptation", Domain: "tool-adaptation",
		Path:        filepath.Join(canonicalHome, "tool-adaptation.db"),
		LegacyRoots: []string{filepath.Join(canonicalHome, "tool_adaptation_state.json")},
	})
	gitWorkspaceRoot, err := resolveGitWorkspaceRoot(canonicalHome, workspace, cfg)
	if err != nil {
		return nil, fmt.Errorf("database catalog git workspace root: %w", err)
	}
	checkpointRoot := filepath.Join(
		gitWorkspaceRoot,
		".pr-workspace-implementation",
		"active",
	)
	add(Spec{
		ID: "global.git-workspace-inventory", Domain: "git-workspace-inventory",
		Path:        filepath.Join(gitWorkspaceRoot, "inventory.db"),
		LegacyRoots: []string{filepath.Join(gitWorkspaceRoot, "inventory.json")},
		Required:    true,
	})
	add(Spec{
		ID: "global.pr-workspace-checkpoints", Domain: "pr-workspace-checkpoints",
		Path:        filepath.Join(checkpointRoot, "checkpoints.db"),
		LegacyRoots: []string{checkpointRoot},
		Required:    true,
	})
	add(Spec{
		ID: "channel.wecom", Domain: "channel-wecom",
		Path: filepath.Join(canonicalHome, "channels", "wecom", "reqid-store.db"),
		LegacyRoots: []string{
			filepath.Join(canonicalHome, "wecom", "reqid-store.json"),
			filepath.Join(canonicalHome, "channels", "wecom", "reqid-store.json"),
		},
		Required: channelTypeEnabled(cfg, config.ChannelWeCom),
	})
	add(Spec{
		ID: "channel.weixin", Domain: "channel-weixin",
		Path: filepath.Join(canonicalHome, "channels", "weixin", "state.db"),
		LegacyRoots: []string{
			filepath.Join(canonicalHome, "channels", "weixin", "sync"),
			filepath.Join(canonicalHome, "channels", "weixin", "context-tokens"),
		},
		Required: channelTypeEnabled(cfg, config.ChannelWeixin),
	})

	storePath := projectedStorePath
	if inspectGenerations {
		storePath = canonicalStorePath
	}
	eventPath, err := eventDatabasePath(workspace, cfg.Events.Ingress.DatabasePath, storePath)
	if err != nil {
		return nil, fmt.Errorf("database catalog event store: %w", err)
	}
	addWorkspaceSpecs(&specs, "workspace", workspace, eventPath, cfg, true)

	seenWorkspaces := map[string]struct{}{workspace: {}}
	for _, agent := range cfg.Agents.List {
		raw := strings.TrimSpace(agent.Workspace)
		if raw == "" {
			continue
		}
		agentWorkspace, resolveErr := resolveWorkspace(canonicalHome, raw)
		if resolveErr != nil {
			return nil, fmt.Errorf("database catalog agent %q workspace: %w", agent.ID, resolveErr)
		}
		if _, duplicate := seenWorkspaces[agentWorkspace]; duplicate {
			// Multiple agents may intentionally share one exact canonical
			// workspace; it produces one logical store set and one physical pool.
			continue
		}
		seenWorkspaces[agentWorkspace] = struct{}{}
		prefix := "workspace." + shortPathID(agentWorkspace)
		agentEventPath, resolveErr := eventDatabasePath(agentWorkspace, "", storePath)
		if resolveErr != nil {
			return nil, fmt.Errorf("database catalog agent %q event store: %w", agent.ID, resolveErr)
		}
		addWorkspaceSpecs(&specs, prefix, agentWorkspace, agentEventPath, cfg, false)
	}

	channelNames := make([]string, 0, len(cfg.Channels))
	for name := range cfg.Channels {
		channelNames = append(channelNames, name)
	}
	sort.Strings(channelNames)
	for _, name := range channelNames {
		channel := cfg.Channels[name]
		if channel == nil || !channel.Enabled {
			continue
		}
		switch channel.Type {
		case config.ChannelMatrix:
			decoded, decodeErr := channel.GetDecoded()
			if decodeErr != nil {
				return nil, fmt.Errorf("database catalog matrix channel %q: %w", name, decodeErr)
			}
			settings, ok := decoded.(*config.MatrixSettings)
			if !ok || settings == nil {
				return nil, fmt.Errorf("database catalog matrix channel %q has invalid settings", name)
			}
			root := strings.TrimSpace(settings.CryptoDatabasePath)
			if root == "" {
				root = filepath.Join(workspace, "matrix")
			} else if !filepath.IsAbs(root) {
				root = filepath.Join(workspace, root)
			}
			path, pathErr := storePath(filepath.Join(root, databaseFilename))
			if pathErr != nil {
				return nil, fmt.Errorf("database catalog matrix channel %q: %w", name, pathErr)
			}
			add(Spec{
				ID: "channel.matrix." + logicalComponent(name), Domain: "channel-matrix", Path: path,
				Required: strings.TrimSpace(settings.CryptoPassphrase) != "",
			})
		case config.ChannelWhatsAppNative:
			decoded, decodeErr := channel.GetDecoded()
			if decodeErr != nil {
				return nil, fmt.Errorf("database catalog WhatsApp channel %q: %w", name, decodeErr)
			}
			settings, ok := decoded.(*config.WhatsAppSettings)
			if !ok || settings == nil {
				return nil, fmt.Errorf("database catalog WhatsApp channel %q has invalid settings", name)
			}
			root := strings.TrimSpace(settings.SessionStorePath)
			if root == "" {
				root = filepath.Join(workspace, "whatsapp")
			} else if !filepath.IsAbs(root) {
				root = filepath.Join(workspace, root)
			}
			path, pathErr := storePath(filepath.Join(root, databaseFilename))
			if pathErr != nil {
				return nil, fmt.Errorf("database catalog WhatsApp channel %q: %w", name, pathErr)
			}
			add(Spec{
				ID: "channel.whatsapp." + logicalComponent(name), Domain: "channel-whatsapp", Path: path,
				Required: true,
			})
		}
	}

	for index := range specs {
		path, pathErr := storePath(specs[index].Path)
		if pathErr != nil {
			return nil, fmt.Errorf("database catalog store %s: %w", specs[index].ID, pathErr)
		}
		specs[index].Path = path
		legacyPath := canonicalLegacyPath
		if !inspectGenerations {
			// Policy projection must remain constructible while retained inputs are
			// being atomically archived or an unsafe leaf is awaiting identity
			// validation. Canonicalize the trusted parent without inspecting the
			// mutable legacy leaf; broker Build remains strict.
			legacyPath = projectedStorePath
		}
		for legacyIndex := range specs[index].LegacyRoots {
			legacy, legacyErr := legacyPath(specs[index].LegacyRoots[legacyIndex])
			if legacyErr != nil {
				return nil, fmt.Errorf("database catalog store %s legacy input: %w", specs[index].ID, legacyErr)
			}
			specs[index].LegacyRoots[legacyIndex] = legacy
		}
	}
	if err := validateSpecsMode(specs, inspectGenerations); err != nil {
		return nil, err
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	byID := make(map[string]int, len(specs))
	for index := range specs {
		byID[specs[index].ID] = index
	}
	return &Catalog{Home: canonicalHome, Specs: specs, byID: byID}, nil
}

func resolveGitWorkspaceRoot(canonicalHome, workspace string, cfg *config.Config) (string, error) {
	root := cfg.GitWorkspaces.EffectiveRootDir(workspace)
	if !filepath.IsAbs(root) {
		root = filepath.Join(canonicalHome, root)
	}
	return canonicalDirectoryPath(root)
}

func (c *Catalog) Lookup(id string) (Spec, bool) {
	if c == nil {
		return Spec{}, false
	}
	index, ok := c.byID[id]
	if !ok {
		return Spec{}, false
	}
	return cloneSpec(c.Specs[index]), true
}

func (c *Catalog) All() []Spec {
	if c == nil {
		return nil
	}
	result := make([]Spec, len(c.Specs))
	for index := range c.Specs {
		result[index] = cloneSpec(c.Specs[index])
	}
	return result
}

func cloneSpec(spec Spec) Spec {
	spec.LegacyRoots = append([]string(nil), spec.LegacyRoots...)
	return spec
}

func addWorkspaceSpecs(
	specs *[]Spec,
	prefix string,
	workspace string,
	eventPath string,
	cfg *config.Config,
	primary bool,
) {
	requiredWorkflows := cfg.Workflows.Enabled
	requiredEvents := cfg.Events.Ingress.Enabled
	requiredEvolution := cfg.Evolution.Enabled
	evolutionRoot := filepath.Join(workspace, "state", "evolution")
	if primary && strings.TrimSpace(cfg.Evolution.StateDir) != "" {
		evolutionRoot = strings.TrimSpace(cfg.Evolution.StateDir)
		if !filepath.IsAbs(evolutionRoot) {
			evolutionRoot = filepath.Join(workspace, evolutionRoot)
		}
	}
	localCIRoot := filepath.Join(filepath.Dir(eventPath), "pr-workspace-local-ci", "evidence")
	appendSpec := func(suffix, domain, path string, legacy []string, required bool) {
		*specs = append(*specs, Spec{
			ID: prefix + "." + suffix, Domain: domain, Path: path,
			LegacyRoots: legacy, Required: required,
		})
	}
	appendSpec("workflows", "workflows", filepath.Join(workspace, "state", "workflows.db"), []string{
		filepath.Join(workspace, "workflow_runs"), filepath.Join(workspace, "workflow_state"),
		filepath.Join(workspace, "workflow_validations", "manifest.json"),
		filepath.Join(workspace, "workflow_dev"),
	}, requiredWorkflows)
	appendSpec("sessions", "sessions", filepath.Join(workspace, "sessions", "sessions.db"), []string{
		filepath.Join(workspace, "sessions"), filepath.Join(workspace, "threads"),
	}, true)
	appendSpec("eventing", "eventing", eventPath, nil, requiredEvents)
	appendSpec("cron", "cron", filepath.Join(workspace, "cron", "jobs.db"), []string{
		filepath.Join(workspace, "cron", "jobs.json"),
	}, true)
	appendSpec("runtime-state", "runtime-state", filepath.Join(workspace, "state", "runtime.db"), []string{
		filepath.Join(workspace, "state.json"), filepath.Join(workspace, "state", "state.json"),
	}, true)
	if primary {
		appendSpec(
			"account-routing",
			"account-routing",
			filepath.Join(workspace, "state", "account-router.db"),
			[]string{
				filepath.Join(workspace, "account_router_state.json"),
			},
			len(cfg.AccountRouters) != 0,
		)
		appendSpec(
			"repository-reviews",
			"repository-reviews",
			filepath.Join(workspace, "repository_reviews", "repository-reviews.db"),
			[]string{filepath.Join(workspace, "repository_reviews")},
			true,
		)
		appendSpec(
			"repository-evaluations",
			"repository-evaluations",
			filepath.Join(workspace, "repository_evaluations", "evaluations.db"),
			[]string{filepath.Join(workspace, "repository_evaluations")},
			true,
		)
	}
	if !primary {
		appendSpec(
			"repository-reviews",
			"repository-reviews",
			filepath.Join(workspace, "repository_reviews", "repository-reviews.db"),
			[]string{filepath.Join(workspace, "repository_reviews")},
			true,
		)
		appendSpec(
			"repository-evaluations",
			"repository-evaluations",
			filepath.Join(workspace, "repository_evaluations", "evaluations.db"),
			[]string{filepath.Join(workspace, "repository_evaluations")},
			true,
		)
	}
	appendSpec(
		"evolution",
		"evolution",
		filepath.Join(evolutionRoot, "evolution.db"),
		[]string{evolutionRoot},
		requiredEvolution,
	)
	appendSpec("local-ci", "local-ci", filepath.Join(localCIRoot, "cache.db"), []string{
		filepath.Join(localCIRoot, "cache"),
	}, requiredEvents)
	appendSpec("seahorse", "seahorse", filepath.Join(workspace, "sessions", "seahorse.db"), nil,
		strings.EqualFold(strings.TrimSpace(cfg.Agents.Defaults.ContextManager), "seahorse"))
}

func launcherConfigLegacyPath(home string) string {
	configPath := strings.TrimSpace(os.Getenv(config.EnvConfig))
	if configPath == "" {
		return filepath.Join(home, "launcher-config.json")
	}
	if !filepath.IsAbs(configPath) {
		if absolute, err := filepath.Abs(configPath); err == nil {
			configPath = absolute
		}
	}
	return filepath.Join(filepath.Dir(configPath), "launcher-config.json")
}

func eventDatabasePath(
	workspace,
	configured string,
	canonicalize func(string) (string, error),
) (string, error) {
	path := strings.TrimSpace(configured)
	if strings.ContainsRune(path, 0) {
		return "", errors.New("path contains a NUL byte")
	}
	if path == "" {
		path = filepath.Join(workspace, "eventing", "events.db")
	} else if strings.HasPrefix(path, "~/") || path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	return canonicalize(path)
}

func resolveWorkspace(home, configured string) (string, error) {
	path := strings.TrimSpace(configured)
	if path == "" {
		path = filepath.Join(home, "workspace")
	} else if strings.HasPrefix(path, "~/") || path == "~" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = userHome
		} else {
			path = filepath.Join(userHome, path[2:])
		}
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(home, path)
	}
	return canonicalDirectoryPath(path)
}

func canonicalDirectoryPath(path string) (string, error) {
	canonical, info, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	if info != nil && !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return canonical, nil
}

func canonicalStorePath(path string) (string, error) {
	canonical, info, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	if info != nil && !info.Mode().IsRegular() {
		return "", errors.New("database generation is not a regular file")
	}
	for _, sidecar := range []string{canonical + "-wal", canonical + "-shm", canonical + "-journal"} {
		_, sidecarInfo, sidecarErr := canonicalPath(sidecar)
		if sidecarErr != nil {
			return "", sidecarErr
		}
		if sidecarInfo != nil && !sidecarInfo.Mode().IsRegular() {
			return "", errors.New("database sidecar is not a regular file")
		}
	}
	return canonical, nil
}

// projectedStorePath canonicalizes only the parent namespace. In particular it
// never stats the database leaf or any WAL/SHM/journal sibling.
func projectedStorePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(path) != path {
		return "", errors.New("path is empty or has surrounding whitespace")
	}
	if strings.ContainsRune(path, 0) {
		return "", errors.New("path contains a NUL byte")
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	directory, err := canonicalDirectoryPath(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, filepath.Base(absolute)), nil
}

func canonicalLegacyPath(path string) (string, error) {
	canonical, info, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	if info != nil && !info.IsDir() && !info.Mode().IsRegular() {
		return "", errors.New("legacy input is not a regular file or directory")
	}
	return canonical, nil
}

// canonicalPath rejects symlinks in every existing component. Missing suffixes
// are allowed because a trusted catalog is built before first store creation.
func canonicalPath(path string) (string, os.FileInfo, error) {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(path) != path {
		return "", nil, errors.New("path is empty or has surrounding whitespace")
	}
	if strings.ContainsRune(path, 0) {
		return "", nil, errors.New("path contains a NUL byte")
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", nil, err
	}
	current := absolute
	var suffix []string
	var info os.FileInfo
	for {
		info, err = os.Lstat(current)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", nil, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, errors.New("path contains a symlink")
	}
	// Resolve only an existing directory. A live SQLite sidecar may disappear
	// after Lstat while the broker checkpoints; resolving the regular-file leaf
	// would turn that expected transition into a catalog failure. The final
	// Lstat below still rejects a leaf that is replaced by a symlink.
	resolveTarget := current
	if !info.IsDir() {
		resolveTarget = filepath.Dir(current)
	}
	resolved, err := filepath.EvalSymlinks(resolveTarget)
	if err != nil {
		return "", nil, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", nil, err
	}
	if filepath.Clean(resolved) != filepath.Clean(resolveTarget) {
		return "", nil, errors.New("path contains a symlinked ancestor")
	}
	for left, right := 0, len(suffix)-1; left < right; left, right = left+1, right-1 {
		suffix[left], suffix[right] = suffix[right], suffix[left]
	}
	canonical := current
	for _, component := range suffix {
		canonical = filepath.Join(canonical, component)
	}
	finalInfo, finalErr := os.Lstat(canonical)
	if finalErr == nil {
		if finalInfo.Mode()&os.ModeSymlink != 0 {
			return "", nil, errors.New("path is a symlink")
		}
		return canonical, finalInfo, nil
	}
	if !errors.Is(finalErr, os.ErrNotExist) {
		return "", nil, finalErr
	}
	return canonical, nil, nil
}

func validateSpecs(specs []Spec) error {
	return validateSpecsMode(specs, true)
}

func validateSpecsMode(specs []Spec, inspectGenerations bool) error {
	return validateSpecsWithPathKeyMode(specs, catalogPathKey, inspectGenerations)
}

type generationReservation struct {
	storeID string
	role    string
	path    string
	key     string
	info    os.FileInfo
}

func validateSpecsWithPathKey(specs []Spec, pathKey func(string) string) error {
	return validateSpecsWithPathKeyMode(specs, pathKey, true)
}

func validateSpecsWithPathKeyMode(
	specs []Spec,
	pathKey func(string) string,
	inspectGenerations bool,
) error {
	seenIDs := make(map[string]struct{}, len(specs))
	reservations := make([]generationReservation, 0, len(specs)*3)
	for _, spec := range specs {
		if !validID(spec.ID) {
			return fmt.Errorf("database catalog contains invalid store ID %q", spec.ID)
		}
		if _, duplicate := seenIDs[spec.ID]; duplicate {
			return fmt.Errorf("database catalog contains duplicate store ID %q", spec.ID)
		}
		seenIDs[spec.ID] = struct{}{}

		for _, member := range []struct {
			role   string
			suffix string
		}{
			{role: "main"},
			{role: "wal", suffix: "-wal"},
			{role: "shm", suffix: "-shm"},
			{role: "journal", suffix: "-journal"},
		} {
			reservation := generationReservation{
				storeID: spec.ID,
				role:    member.role,
				path:    spec.Path + member.suffix,
			}
			reservation.key = pathKey(reservation.path)
			for _, previous := range reservations {
				if previous.key == reservation.key {
					if previous.role == "main" && reservation.role == "main" {
						return fmt.Errorf(
							"database catalog stores %s and %s resolve to one path",
							previous.storeID, reservation.storeID,
						)
					}
					return fmt.Errorf(
						"database catalog store %s %s aliases store %s %s",
						previous.storeID, previous.role, reservation.storeID, reservation.role,
					)
				}
				if generationBoundariesOverlap(previous.key, reservation.key) {
					return fmt.Errorf(
						"database catalog stores %s and %s have overlapping physical generation boundaries",
						previous.storeID, reservation.storeID,
					)
				}
			}

			if !inspectGenerations {
				reservations = append(reservations, reservation)
				continue
			}
			info, err := os.Lstat(reservation.path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err == nil {
				if !info.Mode().IsRegular() {
					return fmt.Errorf(
						"database catalog store %s %s generation member is not regular",
						reservation.storeID, reservation.role,
					)
				}
				reservation.info = info
				for _, previous := range reservations {
					if previous.info != nil && os.SameFile(previous.info, reservation.info) {
						return fmt.Errorf(
							"database catalog store %s %s and store %s %s resolve to one physical file",
							previous.storeID, previous.role, reservation.storeID, reservation.role,
						)
					}
				}
			}
			reservations = append(reservations, reservation)
		}
	}
	return nil
}

func generationBoundariesOverlap(first, second string) bool {
	separator := string(os.PathSeparator)
	return strings.HasPrefix(first, second+separator) || strings.HasPrefix(second, first+separator)
}

func catalogPathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(path)
	}
	return path
}

func validID(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '.' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func logicalComponent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9',
			character == '-', character == '.':
			builder.WriteRune(character)
		default:
			builder.WriteByte('-')
		}
	}
	component := strings.Trim(builder.String(), "-.")
	if component == "" {
		component = "unnamed"
	}
	if len(component) > 72 {
		component = strings.Trim(component[:72], "-.")
	}
	digest := sha256.Sum256([]byte(value))
	return component + "-" + fmt.Sprintf("%x", digest[:4])
}

// ChannelStoreID returns the trusted logical ID shape used while building
// dynamic channel catalog entries. It is internal so callers cannot use it to
// invent authority without a subsequent public Catalog lookup.
func ChannelStoreID(channelType, name string) (string, bool) {
	var prefix string
	switch channelType {
	case config.ChannelMatrix:
		prefix = "channel.matrix."
	case config.ChannelWhatsAppNative:
		prefix = "channel.whatsapp."
	default:
		return "", false
	}
	return prefix + logicalComponent(name), true
}

func shortPathID(path string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(path)))
	return fmt.Sprintf("%x", digest[:8])
}

func channelTypeEnabled(cfg *config.Config, channelType string) bool {
	for _, channel := range cfg.Channels {
		if channel != nil && channel.Enabled && channel.Type == channelType {
			return true
		}
	}
	return false
}
