// Package storecatalog builds the trusted physical-store inventory used by the
// future logical catalog and by privileged database infrastructure. A repository
// import guard prevents application packages from turning a logical ID into a
// physical path.
package storecatalog

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

const databaseFilename = "store.db"

// Spec is a provider-only catalog record. Physical and legacy paths must never
// be projected into application-facing APIs.
type Spec struct {
	ID          database.StoreID
	Domain      string
	Path        string
	LegacyRoots []string
	Required    bool
}

// Options supplies every path context needed to build a deterministic catalog.
// ConfigPath defaults beneath Home. A configured path beginning with ~ is
// accepted only when UserHome is an explicit absolute path.
type Options struct {
	Home       string
	Config     *config.Config
	ConfigPath string
	UserHome   string
}

// Catalog is immutable after Build returns.
type Catalog struct {
	home  string
	specs []Spec
	byID  map[database.StoreID]int
}

// Build resolves every path from a canonical home and a validated config. It
// rejects path aliases, symlinks, non-regular generations, and physical store
// collisions before any SQLite connection is opened.
func Build(options Options) (*Catalog, error) {
	return build(options, true)
}

// Project derives the same candidate identities without inspecting database
// generation members. It exists only for filesystem protection policy; broker
// startup and maintenance must use Build and providers must revalidate on open.
func Project(options Options) (*Catalog, error) {
	return build(options, false)
}

func build(options Options, inspectGenerations bool) (*Catalog, error) {
	if options.Config == nil {
		return nil, errors.New("database catalog config is required")
	}
	home, err := exactCatalogHome(options.Home)
	if err != nil {
		return nil, fmt.Errorf("database catalog home: %w", err)
	}
	canonicalHome, err := database.CanonicalHome(home)
	if err != nil {
		return nil, fmt.Errorf("database catalog home: %w", err)
	}
	userHome, err := explicitUserHome(options.UserHome)
	if err != nil {
		return nil, err
	}
	configPath, err := catalogConfigPath(canonicalHome, options.ConfigPath, userHome)
	if err != nil {
		return nil, err
	}
	cfg := options.Config
	workspacePath := resolveWorkspace
	gitWorkspacePath := resolveGitWorkspaceRoot
	if !inspectGenerations {
		workspacePath = projectWorkspace
		gitWorkspacePath = projectGitWorkspaceRoot
	}
	workspace, err := workspacePath(canonicalHome, cfg.Agents.Defaults.Workspace, userHome)
	if err != nil {
		return nil, fmt.Errorf("database catalog workspace: %w", err)
	}

	var specs []Spec
	add := func(spec Spec) {
		specs = append(specs, spec)
	}
	add(Spec{
		ID: "global/auth", Domain: "auth", Path: filepath.Join(canonicalHome, "auth.db"),
		LegacyRoots: []string{filepath.Join(canonicalHome, "auth.json")}, Required: true,
	})
	add(Spec{
		ID: "launcher/auth", Domain: "launcher-auth", Path: filepath.Join(canonicalHome, "launcher-auth.db"),
		LegacyRoots: []string{launcherConfigLegacyPath(configPath)}, Required: true,
	})
	add(Spec{
		ID: "global/model-catalogs", Domain: "model-catalogs",
		Path:        filepath.Join(canonicalHome, "model-catalogs.db"),
		LegacyRoots: []string{filepath.Join(canonicalHome, "model_catalogs.json")},
	})
	add(Spec{
		ID: "global/tool-adaptation", Domain: "tool-adaptation",
		Path:        filepath.Join(canonicalHome, "tool-adaptation.db"),
		LegacyRoots: []string{filepath.Join(canonicalHome, "tool_adaptation_state.json")},
	})
	gitWorkspaceRoot, err := gitWorkspacePath(canonicalHome, workspace, cfg, userHome)
	if err != nil {
		return nil, fmt.Errorf("database catalog git workspace root: %w", err)
	}
	checkpointRoot := filepath.Join(
		gitWorkspaceRoot,
		".pr-workspace-implementation",
		"active",
	)
	add(Spec{
		ID: "global/git-workspace-inventory", Domain: "git-workspace-inventory",
		Path:        filepath.Join(gitWorkspaceRoot, "inventory.db"),
		LegacyRoots: []string{filepath.Join(gitWorkspaceRoot, "inventory.json")},
		Required:    true,
	})
	add(Spec{
		ID: "global/pr-workspace-checkpoints", Domain: "pr-workspace-checkpoints",
		Path:        filepath.Join(checkpointRoot, "checkpoints.db"),
		LegacyRoots: []string{checkpointRoot},
		Required:    true,
	})
	weComRequired, err := channelTypeActive(cfg, config.ChannelWeCom)
	if err != nil {
		return nil, fmt.Errorf("database catalog WeCom channel: %w", err)
	}
	add(Spec{
		ID: "channel/wecom", Domain: "channel-wecom",
		Path: filepath.Join(canonicalHome, "channels", "wecom", "reqid-store.db"),
		LegacyRoots: []string{
			filepath.Join(canonicalHome, "wecom", "reqid-store.json"),
			filepath.Join(canonicalHome, "channels", "wecom", "reqid-store.json"),
		},
		Required: weComRequired,
	})
	weixinRequired, err := channelTypeActive(cfg, config.ChannelWeixin)
	if err != nil {
		return nil, fmt.Errorf("database catalog Weixin channel: %w", err)
	}
	add(Spec{
		ID: "channel/weixin", Domain: "channel-weixin",
		Path: filepath.Join(canonicalHome, "channels", "weixin", "state.db"),
		LegacyRoots: []string{
			filepath.Join(canonicalHome, "channels", "weixin", "sync"),
			filepath.Join(canonicalHome, "channels", "weixin", "context-tokens"),
		},
		Required: weixinRequired,
	})

	storePath := projectedStorePath
	if inspectGenerations {
		storePath = canonicalStorePath
	}
	eventPath, err := eventDatabasePath(workspace, cfg.Events.Ingress.DatabasePath, userHome, storePath)
	if err != nil {
		return nil, fmt.Errorf("database catalog event store: %w", err)
	}
	primaryEvolutionRoot, err := catalogEvolutionRoot(workspace, cfg.Evolution.StateDir, userHome)
	if err != nil {
		return nil, fmt.Errorf("database catalog workspace stores: %w", err)
	}
	addWorkspaceSpecs(&specs, "workspace", workspace, eventPath, primaryEvolutionRoot, cfg, true)

	seenWorkspaces := map[string]struct{}{catalogPathKey(workspace): {}}
	for _, agent := range cfg.Agents.List {
		raw := agent.Workspace
		if raw == "" {
			continue
		}
		agentWorkspace, resolveErr := workspacePath(canonicalHome, raw, userHome)
		if resolveErr != nil {
			return nil, fmt.Errorf("database catalog agent %q workspace: %w", agent.ID, resolveErr)
		}
		agentWorkspaceKey := catalogPathKey(agentWorkspace)
		if _, duplicate := seenWorkspaces[agentWorkspaceKey]; duplicate {
			// Multiple agents may intentionally share one exact canonical
			// workspace; it produces one logical store set and one physical pool.
			continue
		}
		seenWorkspaces[agentWorkspaceKey] = struct{}{}
		prefix := "workspace/" + shortPathID(agentWorkspace)
		agentEventPath, resolveErr := eventDatabasePath(agentWorkspace, "", userHome, storePath)
		if resolveErr != nil {
			return nil, fmt.Errorf("database catalog agent %q event store: %w", agent.ID, resolveErr)
		}
		addWorkspaceSpecs(
			&specs, prefix, agentWorkspace, agentEventPath,
			filepath.Join(agentWorkspace, "state", "evolution"), cfg, false,
		)
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
		channelType := channel.Type
		if channelType == "" {
			channelType = name
		}
		switch channelType {
		case config.ChannelMatrix:
			decoded, decodeErr := clonedChannelSettings(name, channel)
			if decodeErr != nil {
				return nil, fmt.Errorf("database catalog matrix channel %q: %w", name, decodeErr)
			}
			settings, ok := decoded.(*config.MatrixSettings)
			if !ok || settings == nil {
				return nil, fmt.Errorf("database catalog matrix channel %q has invalid settings", name)
			}
			root, pathErr := configuredCatalogPath(
				workspace, settings.CryptoDatabasePath, userHome, filepath.Join(workspace, "matrix"),
			)
			if pathErr != nil {
				return nil, fmt.Errorf("database catalog matrix channel %q: %w", name, pathErr)
			}
			path, pathErr := storePath(filepath.Join(root, databaseFilename))
			if pathErr != nil {
				return nil, fmt.Errorf("database catalog matrix channel %q: %w", name, pathErr)
			}
			add(Spec{
				ID:     database.StoreID("channel/matrix/" + logicalComponent(name)),
				Domain: "channel-matrix", Path: path,
				Required: matrixChannelActive(settings) && settings.CryptoPassphrase != "",
			})
		case config.ChannelWhatsAppNative:
			decoded, decodeErr := clonedChannelSettings(name, channel)
			if decodeErr != nil {
				return nil, fmt.Errorf("database catalog WhatsApp channel %q: %w", name, decodeErr)
			}
			settings, ok := decoded.(*config.WhatsAppSettings)
			if !ok || settings == nil {
				return nil, fmt.Errorf("database catalog WhatsApp channel %q has invalid settings", name)
			}
			root, pathErr := configuredCatalogPath(
				workspace, settings.SessionStorePath, userHome, filepath.Join(workspace, "whatsapp"),
			)
			if pathErr != nil {
				return nil, fmt.Errorf("database catalog WhatsApp channel %q: %w", name, pathErr)
			}
			path, pathErr := storePath(filepath.Join(root, databaseFilename))
			if pathErr != nil {
				return nil, fmt.Errorf("database catalog WhatsApp channel %q: %w", name, pathErr)
			}
			add(Spec{
				ID:     database.StoreID("channel/whatsapp/" + logicalComponent(name)),
				Domain: "channel-whatsapp", Path: path,
				Required: settings.UseNative,
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
	byID := make(map[database.StoreID]int, len(specs))
	for index := range specs {
		byID[specs[index].ID] = index
	}
	return &Catalog{home: canonicalHome, specs: specs, byID: byID}, nil
}

func resolveGitWorkspaceRoot(
	canonicalHome, workspace string,
	cfg *config.Config,
	userHome string,
) (string, error) {
	root, err := gitWorkspaceRoot(canonicalHome, workspace, cfg, userHome)
	if err != nil {
		return "", err
	}
	return canonicalDirectoryPath(root)
}

func projectGitWorkspaceRoot(
	canonicalHome, workspace string,
	cfg *config.Config,
	userHome string,
) (string, error) {
	root, err := gitWorkspaceRoot(canonicalHome, workspace, cfg, userHome)
	if err != nil {
		return "", err
	}
	return projectedStorePath(root)
}

func gitWorkspaceRoot(canonicalHome, workspace string, cfg *config.Config, userHome string) (string, error) {
	configured := cfg.GitWorkspaces.RootDir
	if configured == "" {
		return filepath.Join(workspace, ".git-workspaces"), nil
	}
	return configuredCatalogPath(canonicalHome, configured, userHome, "")
}

func (c *Catalog) Lookup(id database.StoreID) (Spec, bool) {
	if c == nil {
		return Spec{}, false
	}
	index, ok := c.byID[id]
	if !ok {
		return Spec{}, false
	}
	return cloneSpec(c.specs[index]), true
}

// Home returns the canonical home used to build the catalog.
func (c *Catalog) Home() string {
	if c == nil {
		return ""
	}
	return c.home
}

func (c *Catalog) All() []Spec {
	if c == nil {
		return nil
	}
	result := make([]Spec, len(c.specs))
	for index := range c.specs {
		result[index] = cloneSpec(c.specs[index])
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
	evolutionRoot string,
	cfg *config.Config,
	primary bool,
) {
	requiredWorkflows := cfg.Workflows.Enabled
	requiredEvents := cfg.Events.Ingress.Enabled
	requiredEvolution := cfg.Evolution.Enabled
	localCIRoot := filepath.Join(filepath.Dir(eventPath), "pr-workspace-local-ci", "evidence")
	appendSpec := func(suffix, domain, path string, legacy []string, required bool) {
		*specs = append(*specs, Spec{
			ID: database.StoreID(prefix + "/" + suffix), Domain: domain, Path: path,
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
	}
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

func catalogEvolutionRoot(workspace, configured, userHome string) (string, error) {
	return configuredCatalogPath(
		workspace, configured, userHome, filepath.Join(workspace, "state", "evolution"),
	)
}

func launcherConfigLegacyPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "launcher-config.json")
}

func eventDatabasePath(
	workspace,
	configured string,
	userHome string,
	canonicalize func(string) (string, error),
) (string, error) {
	path, err := configuredCatalogPath(
		workspace, configured, userHome, filepath.Join(workspace, "eventing", "events.db"),
	)
	if err != nil {
		return "", err
	}
	return canonicalize(path)
}

func resolveWorkspace(home, configured, userHome string) (string, error) {
	return resolveWorkspaceWith(home, configured, userHome, canonicalDirectoryPath)
}

func projectWorkspace(home, configured, userHome string) (string, error) {
	return resolveWorkspaceWith(home, configured, userHome, projectedStorePath)
}

func resolveWorkspaceWith(
	home,
	configured string,
	userHome string,
	resolve func(string) (string, error),
) (string, error) {
	path, err := configuredCatalogPath(home, configured, userHome, filepath.Join(home, "workspace"))
	if err != nil {
		return "", err
	}
	return resolve(path)
}

func explicitUserHome(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if value != strings.TrimSpace(value) {
		return "", errors.New("database catalog user home is invalid")
	}
	path, err := absoluteCatalogPath(value)
	if err != nil {
		return "", fmt.Errorf("database catalog user home: %w", err)
	}
	return path, nil
}

func exactCatalogHome(path string) (string, error) {
	if filepath.Clean(path) != path {
		return "", errors.New("database catalog home must be lexically clean")
	}
	return absoluteCatalogPath(path)
}

func catalogConfigPath(home, configured, userHome string) (string, error) {
	if configured == "" {
		return filepath.Join(home, "config.json"), nil
	}
	if configured != strings.TrimSpace(configured) {
		return "", errors.New("database catalog config path is invalid")
	}
	path, err := configuredCatalogPath(home, configured, userHome, "")
	if err != nil {
		return "", fmt.Errorf("database catalog config path: %w", err)
	}
	return path, nil
}

func configuredCatalogPath(base, configured, userHome, fallback string) (string, error) {
	path := strings.TrimSpace(configured)
	if configured != path || !utf8.ValidString(path) || strings.ContainsRune(path, 0) {
		return "", errors.New("path is invalid")
	}
	if path == "" {
		path = fallback
	}
	expanded, err := expandCatalogTilde(path, userHome)
	if err != nil {
		return "", err
	}
	path = expanded
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return absoluteCatalogPath(path)
}

func expandCatalogTilde(path, userHome string) (string, error) {
	tildePrefix := strings.HasPrefix(path, "~/") ||
		(os.PathSeparator == '\\' && strings.HasPrefix(path, `~\`))
	if path != "~" && !tildePrefix {
		if strings.HasPrefix(path, "~") {
			return "", errors.New("path has an unsupported home expansion")
		}
		return path, nil
	}
	if userHome == "" {
		return "", errors.New("path requires an explicit user home")
	}
	if path == "~" {
		return userHome, nil
	}
	return filepath.Join(userHome, path[2:]), nil
}

func absoluteCatalogPath(path string) (string, error) {
	if path == "" || path != strings.TrimSpace(path) || !utf8.ValidString(path) ||
		strings.ContainsRune(path, 0) || !filepath.IsAbs(path) {
		return "", errors.New("path must be an absolute canonical string")
	}
	path = filepath.Clean(path)
	if err := validateCatalogPlatformPath(path); err != nil {
		return "", err
	}
	return path, nil
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

// projectedStorePath derives a lexical absolute artifact identity without
// inspecting mutable configured namespaces. The model-facing identity catalog
// performs bounded physical validation; broker Build remains strict.
func projectedStorePath(path string) (string, error) {
	return absoluteCatalogPath(path)
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
	absolute, err := absoluteCatalogPath(path)
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
	if len(suffix) != 0 && !info.IsDir() {
		return "", nil, errors.New("path contains a non-directory ancestor")
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
	resolved, err = absoluteCatalogPath(resolved)
	if err != nil {
		return "", nil, err
	}
	if !sameCatalogResolvedPath(resolved, resolveTarget) {
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
	storeID database.StoreID
	role    string
	path    string
	key     string
	info    os.FileInfo
}

type legacyReservation struct {
	storeID database.StoreID
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
	seenIDs := make(map[database.StoreID]struct{}, len(specs))
	reservations := make([]generationReservation, 0, len(specs)*4)
	legacyReservations, err := catalogLegacyReservations(specs, pathKey, inspectGenerations)
	if err != nil {
		return err
	}
	for _, spec := range specs {
		if !spec.ID.Valid() {
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
			for _, legacy := range legacyReservations {
				if legacy.key == reservation.key {
					return fmt.Errorf(
						"database catalog store %s %s aliases store %s legacy input",
						reservation.storeID, reservation.role, legacy.storeID,
					)
				}
			}
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
				for _, legacy := range legacyReservations {
					if legacy.info != nil && os.SameFile(legacy.info, reservation.info) {
						return fmt.Errorf(
							"database catalog store %s %s and store %s legacy input resolve to one physical file",
							reservation.storeID, reservation.role, legacy.storeID,
						)
					}
				}
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

func catalogLegacyReservations(
	specs []Spec,
	pathKey func(string) string,
	inspect bool,
) ([]legacyReservation, error) {
	var reservations []legacyReservation
	for _, spec := range specs {
		for _, path := range spec.LegacyRoots {
			reservation := legacyReservation{storeID: spec.ID, key: pathKey(path)}
			if inspect {
				info, err := os.Lstat(path)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return nil, err
				}
				if err == nil {
					if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
						return nil, fmt.Errorf(
							"database catalog store %s legacy input is not a regular file or directory",
							reservation.storeID,
						)
					}
					reservation.info = info
				}
			}
			for _, previous := range reservations {
				if previous.key == reservation.key {
					return nil, fmt.Errorf(
						"database catalog stores %s and %s declare one legacy input",
						previous.storeID, reservation.storeID,
					)
				}
				if inspect && previous.info != nil && reservation.info != nil &&
					os.SameFile(previous.info, reservation.info) {
					return nil, fmt.Errorf(
						"database catalog stores %s and %s legacy inputs resolve to one physical file",
						previous.storeID, reservation.storeID,
					)
				}
			}
			reservations = append(reservations, reservation)
		}
	}
	return reservations, nil
}

func generationBoundariesOverlap(first, second string) bool {
	separator := string(os.PathSeparator)
	return strings.HasPrefix(first, second+separator) || strings.HasPrefix(second, first+separator)
}

func logicalComponent(value string) string {
	raw := value
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
	digest := sha256.Sum256([]byte(raw))
	return component + "-" + fmt.Sprintf("%x", digest[:8])
}

// ChannelStoreID returns the trusted logical ID shape used while building
// dynamic channel catalog entries. It is internal so callers cannot use it to
// invent authority without a subsequent public Catalog lookup.
func ChannelStoreID(channelType, name string) (database.StoreID, bool) {
	var prefix string
	switch channelType {
	case config.ChannelMatrix:
		prefix = "channel/matrix/"
	case config.ChannelWhatsAppNative:
		prefix = "channel/whatsapp/"
	default:
		return "", false
	}
	id, err := database.ParseStoreID(prefix + logicalComponent(name))
	return id, err == nil
}

func shortPathID(path string) string {
	digest := sha256.Sum256([]byte(catalogPathKey(path)))
	return fmt.Sprintf("%x", digest[:8])
}

func clonedChannelSettings(name string, channel *config.Channel) (any, error) {
	clone := *channel
	clone.Settings = append(config.RawNode(nil), channel.Settings...)
	if clone.Type == "" {
		clone.Type = name
	}
	return clone.GetDecoded()
}

func channelTypeActive(cfg *config.Config, wanted string) (bool, error) {
	names := make([]string, 0, len(cfg.Channels))
	for name := range cfg.Channels {
		names = append(names, name)
	}
	sort.Strings(names)
	active := false
	for _, name := range names {
		channel := cfg.Channels[name]
		if channel == nil || !channel.Enabled {
			continue
		}
		channelType := channel.Type
		if channelType == "" {
			channelType = name
		}
		if channelType != wanted {
			continue
		}
		decoded, err := clonedChannelSettings(name, channel)
		if err != nil {
			return false, err
		}
		switch settings := decoded.(type) {
		case *config.WeComSettings:
			if wanted != config.ChannelWeCom {
				return false, errors.New("channel settings do not match type")
			}
			if settings != nil && settings.BotID != "" && settings.Secret.String() != "" {
				active = true
			}
		case *config.WeixinSettings:
			if wanted != config.ChannelWeixin {
				return false, errors.New("channel settings do not match type")
			}
			if settings != nil && settings.Token.String() != "" {
				active = true
			}
		default:
			return false, errors.New("channel settings do not match type")
		}
	}
	return active, nil
}

func matrixChannelActive(settings *config.MatrixSettings) bool {
	return settings != nil && settings.Homeserver != "" && settings.UserID != "" &&
		settings.AccessToken.String() != ""
}
