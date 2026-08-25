package tools

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/sipeed/picoclaw/pkg/media"
)

// FactoryBackedInstall describes one compatibility-source insert or exact
// occupant replacement. A nil Expected requires the name to be absent;
// otherwise Expected must be the exact currently registered non-nil Tool
// pointer.
// Live remains caller-owned and is never closed by the registry.
type FactoryBackedInstall struct {
	Live     Tool
	Factory  ToolFactory
	Hidden   bool
	Expected Tool
}

// FactoryBackedBatch groups installs for one compatibility registry. A
// registry may occur in only one batch per transaction, and a tool name may
// occur only once in that batch.
type FactoryBackedBatch struct {
	Registry *ToolRegistry
	Installs []FactoryBackedInstall
}

// FactoryBackedAdmission is a detached input-order result. It exposes no
// registry, Tool, factory, occupant, or reservation pointer.
type FactoryBackedAdmission struct {
	BatchIndex   int
	InstallIndex int
	Name         string
	Admitted     bool
	Replaced     bool
}

type factoryBackedInstallPlan struct {
	batchIndex   int
	installIndex int
	registry     *ToolRegistry
	live         Tool
	factory      ToolFactory
	hidden       bool
	expected     Tool
	descriptor   ToolDescriptor
	traits       ToolTraits
	admitted     bool
	replaced     bool
	entry        *ToolEntry
	visibility   uint64
	dependency   *ToolEntry
	mediaStore   media.MediaStore
	mediaGen     uint64
	reservation  toolInstanceReservation
	reserved     bool
}

type factoryBackedRegistrySnapshot struct {
	registry *ToolRegistry
	version  uint64
}

// InstallFactoryBackedTransaction atomically installs caller-owned live tools
// with per-owner factories across every supplied compatibility registry. It
// never calls ToolFactory.New. Allowlist-denied installs are reported but do
// not inspect, inject, reserve, or publish their live tool.
//
// All candidate reservations and descriptors are validated before mutation.
// Every participating registry is locked in deterministic identity order, so
// a late close, media, allowlist, catalog, visibility, or occupant change
// aborts every batch without partially publishing any registry.
func InstallFactoryBackedTransaction(
	batches []FactoryBackedBatch,
) ([]FactoryBackedAdmission, error) {
	plans, registries, admissions, err := planFactoryBackedTransaction(batches)
	if err != nil {
		return nil, err
	}
	if len(registries) == 0 {
		return admissions, nil
	}

	lockFactoryBackedMedia(registries)
	defer unlockFactoryBackedMedia(registries)

	snapshots, err := admitFactoryBackedTransaction(plans, registries, admissions)
	if err != nil {
		return nil, err
	}
	if len(plans) == 0 {
		return admissions, nil
	}

	committed := false
	defer func() {
		if committed {
			return
		}
		for index := len(plans) - 1; index >= 0; index-- {
			if plans[index].reserved {
				plans[index].reservation.release()
			}
		}
	}()

	// Reserve every admitted candidate before reading live metadata or invoking
	// any media-aware setter. A later failure releases all new reservations.
	for _, plan := range plans {
		if !plan.admitted {
			continue
		}
		identity, identityErr := toolInstanceIdentity(plan.live)
		if identityErr != nil {
			return nil, fmt.Errorf(
				"factory-backed live tool %q: %w",
				plan.descriptor.Name,
				identityErr,
			)
		}
		if reserveErr := globalOwnedToolInstances.reserve(identity, plan.live); reserveErr != nil {
			return nil, fmt.Errorf(
				"factory-backed live tool %q: %w",
				plan.descriptor.Name,
				reserveErr,
			)
		}
		plan.reservation = toolInstanceReservation{
			tracker: globalOwnedToolInstances, identity: identity,
		}
		plan.reserved = true
	}

	for _, plan := range plans {
		if !plan.admitted {
			continue
		}
		actual, descriptorErr := safeToolDescriptor(plan.live)
		if descriptorErr != nil {
			return nil, descriptorErr
		}
		if !descriptorsEqual(actual, plan.descriptor) {
			return nil, fmt.Errorf(
				"factory-backed live tool %q does not match its frozen descriptor",
				plan.descriptor.Name,
			)
		}
	}

	for _, plan := range plans {
		if !plan.admitted {
			continue
		}
		if mediaErr := injectFactoryMediaStore(
			plan.live,
			plan.mediaStore,
		); mediaErr != nil {
			return nil, fmt.Errorf(
				"configure factory-backed live tool %q media store: %w",
				plan.descriptor.Name,
				mediaErr,
			)
		}
	}
	// Media-aware setters are arbitrary compatibility code. Re-read the live
	// descriptor afterward so a setter cannot mutate provider metadata between
	// the initial parity check and publication.
	for _, plan := range plans {
		if !plan.admitted {
			continue
		}
		actual, descriptorErr := safeToolDescriptor(plan.live)
		if descriptorErr != nil {
			return nil, descriptorErr
		}
		if !descriptorsEqual(actual, plan.descriptor) {
			return nil, fmt.Errorf(
				"factory-backed live tool %q changed its descriptor during media configuration",
				plan.descriptor.Name,
			)
		}
	}

	lockFactoryBackedState(registries)
	defer unlockFactoryBackedState(registries)
	if err := recheckFactoryBackedTransaction(plans, snapshots); err != nil {
		return nil, err
	}

	// Publish every map and lease before advancing any definition version.
	// Readers of registry maps remain blocked until all registries are complete;
	// lock-free Version readers can observe a new version only after every map
	// mutation in the transaction has already happened.
	for _, plan := range plans {
		if !plan.admitted {
			continue
		}
		frozen := cloneToolDescriptor(plan.descriptor)
		delete(plan.registry.constructionCatalog, plan.descriptor.Name)
		plan.registry.tools[plan.descriptor.Name] = &ToolEntry{
			Tool: plan.live, IsCore: !plan.hidden, TTL: 0,
			descriptor: &frozen, traits: plan.traits, factory: plan.factory,
		}
		plan.registry.compatibilityReservations = append(
			plan.registry.compatibilityReservations,
			plan.reservation,
		)
		plan.reserved = false
	}
	for _, plan := range plans {
		if plan.admitted {
			plan.registry.version.Add(1)
		}
	}

	committed = true
	return admissions, nil
}

func planFactoryBackedTransaction(
	batches []FactoryBackedBatch,
) (
	plans []*factoryBackedInstallPlan,
	registries []*ToolRegistry,
	admissions []FactoryBackedAdmission,
	err error,
) {
	total := 0
	for _, batch := range batches {
		total += len(batch.Installs)
	}
	plans = make([]*factoryBackedInstallPlan, 0, total)
	admissions = make([]FactoryBackedAdmission, 0, total)
	registrySet := make(map[*ToolRegistry]struct{}, len(batches))
	seenNames := make(map[*ToolRegistry]map[string]struct{}, len(batches))

	for batchIndex, batch := range batches {
		if batch.Registry == nil {
			return nil, nil, nil, fmt.Errorf(
				"factory-backed batch %d registry is nil",
				batchIndex,
			)
		}
		if _, duplicate := registrySet[batch.Registry]; duplicate {
			return nil, nil, nil, fmt.Errorf(
				"factory-backed registry is duplicated across batches",
			)
		}
		registrySet[batch.Registry] = struct{}{}
		if seenNames[batch.Registry] == nil {
			seenNames[batch.Registry] = make(map[string]struct{})
		}
		for installIndex, install := range batch.Installs {
			if isTypedNil(install.Live) {
				return nil, nil, nil, fmt.Errorf(
					"factory-backed batch %d install %d live tool is nil",
					batchIndex,
					installIndex,
				)
			}
			if isTypedNil(install.Factory) {
				return nil, nil, nil, fmt.Errorf(
					"factory-backed batch %d install %d factory is nil",
					batchIndex,
					installIndex,
				)
			}
			if install.Expected != nil && isTypedNil(install.Expected) {
				return nil, nil, nil, fmt.Errorf(
					"factory-backed batch %d install %d expected occupant is typed nil",
					batchIndex,
					installIndex,
				)
			}
			if install.Expected != nil {
				if _, identityErr := toolInstanceIdentity(install.Expected); identityErr != nil {
					return nil, nil, nil, fmt.Errorf(
						"factory-backed batch %d install %d expected occupant must be a non-nil pointer: %w",
						batchIndex,
						installIndex,
						identityErr,
					)
				}
			}
			descriptor, traits, metadataErr := snapshotFactoryMetadata(install.Factory)
			if metadataErr != nil {
				return nil, nil, nil, metadataErr
			}
			if traits.Sharing != ToolSharingPerOwner {
				return nil, nil, nil, fmt.Errorf(
					"factory-backed tool %q must use per-owner sharing",
					descriptor.Name,
				)
			}
			if _, duplicate := seenNames[batch.Registry][descriptor.Name]; duplicate {
				return nil, nil, nil, fmt.Errorf(
					"factory-backed tool %q is duplicated for one registry",
					descriptor.Name,
				)
			}
			seenNames[batch.Registry][descriptor.Name] = struct{}{}
			plans = append(plans, &factoryBackedInstallPlan{
				batchIndex: batchIndex, installIndex: installIndex,
				registry: batch.Registry, live: install.Live,
				factory: install.Factory, hidden: install.Hidden,
				expected: install.Expected, descriptor: descriptor, traits: traits,
			})
			admissions = append(admissions, FactoryBackedAdmission{
				BatchIndex: batchIndex, InstallIndex: installIndex, Name: descriptor.Name,
			})
		}
	}

	registries = make([]*ToolRegistry, 0, len(registrySet))
	for registry := range registrySet {
		registries = append(registries, registry)
	}
	sort.Slice(registries, func(left, right int) bool {
		return reflect.ValueOf(registries[left]).Pointer() <
			reflect.ValueOf(registries[right]).Pointer()
	})
	return plans, registries, admissions, nil
}

func admitFactoryBackedTransaction(
	plans []*factoryBackedInstallPlan,
	registries []*ToolRegistry,
	admissions []FactoryBackedAdmission,
) ([]factoryBackedRegistrySnapshot, error) {
	lockFactoryBackedState(registries)
	defer unlockFactoryBackedState(registries)

	snapshots := make([]factoryBackedRegistrySnapshot, 0, len(registries))
	for _, registry := range registries {
		if registry.closed {
			return nil, fmt.Errorf("factory-backed transaction registry is closed")
		}
		if registry.owned {
			return nil, fmt.Errorf("factory-backed transaction requires compatibility registries")
		}
		if registry.tools == nil {
			return nil, fmt.Errorf("factory-backed transaction registry is not initialized")
		}
		snapshots = append(snapshots, factoryBackedRegistrySnapshot{
			registry: registry, version: registry.version.Load(),
		})
	}

	for planIndex, plan := range plans {
		plan.admitted = plan.registry.toolAllowedLocked(plan.descriptor.Name)
		admissions[planIndex].Admitted = plan.admitted
		if !plan.admitted {
			continue
		}
		entry := plan.registry.tools[plan.descriptor.Name]
		if plan.expected == nil {
			if entry != nil {
				return nil, fmt.Errorf(
					"factory-backed tool %q expected an empty registry slot",
					plan.descriptor.Name,
				)
			}
		} else if entry == nil || !samePointerIdentity(entry.Tool, plan.expected) {
			return nil, fmt.Errorf(
				"factory-backed tool %q expected occupant changed",
				plan.descriptor.Name,
			)
		}

		dependency, dependencyErr := plan.registry.factoryDependencyForPromotionLocked(
			plan.descriptor,
			plan.traits,
			plan.factory,
		)
		if dependencyErr != nil {
			return nil, dependencyErr
		}
		if entry != nil && dependency != nil {
			return nil, fmt.Errorf(
				"factory-backed tool %q has an ambiguous public and private occupant",
				plan.descriptor.Name,
			)
		}
		plan.entry = entry
		if entry != nil {
			plan.visibility = entry.visibilityRevision
		}
		plan.dependency = dependency
		plan.mediaStore = plan.registry.mediaStore
		plan.mediaGen = plan.registry.mediaGen
		plan.replaced = entry != nil
		admissions[planIndex].Replaced = plan.replaced
	}
	return snapshots, nil
}

func recheckFactoryBackedTransaction(
	plans []*factoryBackedInstallPlan,
	snapshots []factoryBackedRegistrySnapshot,
) error {
	for _, snapshot := range snapshots {
		registry := snapshot.registry
		if registry.closed {
			return fmt.Errorf("factory-backed transaction registry closed during installation")
		}
		if registry.owned {
			return fmt.Errorf("factory-backed transaction registry ownership changed")
		}
		if registry.version.Load() != snapshot.version {
			return fmt.Errorf("factory-backed transaction registry changed during installation")
		}
	}
	for _, plan := range plans {
		admitted := plan.registry.toolAllowedLocked(plan.descriptor.Name)
		if admitted != plan.admitted {
			return fmt.Errorf(
				"factory-backed tool %q allowlist admission changed",
				plan.descriptor.Name,
			)
		}
		if !plan.admitted {
			continue
		}
		if plan.registry.mediaGen != plan.mediaGen {
			return fmt.Errorf(
				"factory-backed tool %q media generation changed during installation",
				plan.descriptor.Name,
			)
		}
		entry := plan.registry.tools[plan.descriptor.Name]
		if entry != plan.entry {
			return fmt.Errorf(
				"factory-backed tool %q occupant changed during installation",
				plan.descriptor.Name,
			)
		}
		if entry != nil && entry.visibilityRevision != plan.visibility {
			return fmt.Errorf(
				"factory-backed tool %q visibility changed during installation",
				plan.descriptor.Name,
			)
		}
		if plan.expected == nil {
			if entry != nil {
				return fmt.Errorf(
					"factory-backed tool %q expected slot changed during installation",
					plan.descriptor.Name,
				)
			}
		} else if entry == nil || !samePointerIdentity(entry.Tool, plan.expected) {
			return fmt.Errorf(
				"factory-backed tool %q expected occupant changed during installation",
				plan.descriptor.Name,
			)
		}
		if err := plan.registry.validateFactoryDependencyPromotionLocked(
			plan.dependency,
			plan.descriptor,
			plan.traits,
			plan.factory,
		); err != nil {
			return err
		}
	}
	return nil
}

func lockFactoryBackedMedia(registries []*ToolRegistry) {
	for _, registry := range registries {
		registry.mediaApplyMu.Lock()
	}
}

func unlockFactoryBackedMedia(registries []*ToolRegistry) {
	for index := len(registries) - 1; index >= 0; index-- {
		registries[index].mediaApplyMu.Unlock()
	}
}

func lockFactoryBackedState(registries []*ToolRegistry) {
	for _, registry := range registries {
		registry.mu.Lock()
	}
}

func unlockFactoryBackedState(registries []*ToolRegistry) {
	for index := len(registries) - 1; index >= 0; index-- {
		registries[index].mu.Unlock()
	}
}
