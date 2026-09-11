package storecatalog

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func reviewScopeTestOptions(t *testing.T, cfg *config.Config) Options {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	return Options{Home: home, Config: cfg, UserHome: home}
}

func TestReviewScopeTestOptionsConfineEveryCatalogPath(t *testing.T) {
	options := reviewScopeTestOptions(t, nil)
	scope, err := NewReviewScope(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	full, selected, err := ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	for name, catalog := range map[string]*Catalog{"full": full, "selected": selected} {
		for _, spec := range catalog.All() {
			requireReviewScopeTestPath(t, options.Home, name+" store "+spec.ID.String(), spec.Path)
			for _, legacyRoot := range spec.LegacyRoots {
				requireReviewScopeTestPath(
					t, options.Home, name+" legacy "+spec.ID.String(), legacyRoot,
				)
			}
		}
	}
}

func requireReviewScopeTestPath(t *testing.T, root, name, path string) {
	t.Helper()
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || filepath.IsAbs(relative) ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("%s path %q escapes temporary root %q: relative=%q err=%v", name, path, root, relative, err)
	}
}

func TestReviewScopePublicValueSurfaceIsExactAndOpaque(t *testing.T) {
	bindingType := reflect.TypeOf(ReviewBinding{})
	wantBindingFields := []struct {
		name   string
		typeOf reflect.Type
	}{
		{name: "ID", typeOf: reflect.TypeOf(database.StoreID(""))},
		{name: "Domain", typeOf: reflect.TypeOf("")},
		{name: "Required", typeOf: reflect.TypeOf(false)},
	}
	if bindingType.NumField() != len(wantBindingFields) {
		t.Fatalf("ReviewBinding field count = %d, want %d", bindingType.NumField(), len(wantBindingFields))
	}
	for index, want := range wantBindingFields {
		field := bindingType.Field(index)
		if field.Name != want.name || field.Type != want.typeOf || !field.IsExported() {
			t.Fatalf("ReviewBinding field %d = %s %s", index, field.Name, field.Type)
		}
	}

	scopeType := reflect.TypeOf(ReviewScope{})
	for index := range scopeType.NumField() {
		if field := scopeType.Field(index); field.IsExported() {
			t.Fatalf("ReviewScope exposes retained field %s", field.Name)
		}
	}
	pointerType := reflect.PointerTo(scopeType)
	wantMethods := []string{"Bindings", "Fingerprint", "FullFingerprint", "RequiredStores"}
	if pointerType.NumMethod() != len(wantMethods) {
		t.Fatalf("ReviewScope method count = %d, want %d", pointerType.NumMethod(), len(wantMethods))
	}
	for index, want := range wantMethods {
		if method := pointerType.Method(index); method.Name != want {
			t.Fatalf("ReviewScope method %d = %s, want %s", index, method.Name, want)
		}
	}
}

func TestReviewScopeSelectsClosedFixedAndDynamicPolicy(t *testing.T) {
	options := reviewScopeTestOptions(t, nil)
	firstWorkspace := filepath.Join(options.Home, "agents", "first")
	secondWorkspace := filepath.Join(options.Home, "agents", "second")
	options.Config.Agents.List = []config.AgentConfig{
		{ID: "first", Workspace: firstWorkspace},
		{ID: "shared", Workspace: firstWorkspace},
		{ID: "second", Workspace: secondWorkspace},
		{ID: "primary", Workspace: options.Config.Agents.Defaults.Workspace},
		{ID: "empty"},
	}

	scope, err := NewReviewScope(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	bindings := scope.Bindings()
	if len(bindings) != 11 || !slices.IsSortedFunc(bindings, compareReviewBindings) {
		t.Fatalf("review bindings = %#v, want 5+3*2 sorted", bindings)
	}
	wanted := map[database.StoreID]ReviewBinding{
		"global/git-workspace-inventory": {
			ID: "global/git-workspace-inventory", Domain: "git-workspace-inventory", Required: true,
		},
		"global/pr-workspace-checkpoints": {
			ID: "global/pr-workspace-checkpoints", Domain: "pr-workspace-checkpoints", Required: true,
		},
		"workspace/repository-reviews": {
			ID: "workspace/repository-reviews", Domain: "repository-reviews", Required: true,
		},
		"workspace/repository-evaluations": {
			ID: "workspace/repository-evaluations", Domain: "repository-evaluations", Required: true,
		},
		"workspace/local-ci": {ID: "workspace/local-ci", Domain: "local-ci"},
	}
	for _, workspace := range []string{firstWorkspace, secondWorkspace} {
		prefix := "workspace/" + shortPathID(workspace) + "/"
		wanted[database.StoreID(prefix+"repository-reviews")] = ReviewBinding{
			ID: database.StoreID(prefix + "repository-reviews"), Domain: "repository-reviews", Required: true,
		}
		wanted[database.StoreID(prefix+"repository-evaluations")] = ReviewBinding{
			ID:     database.StoreID(prefix + "repository-evaluations"),
			Domain: "repository-evaluations", Required: true,
		}
		wanted[database.StoreID(prefix+"local-ci")] = ReviewBinding{
			ID: database.StoreID(prefix + "local-ci"), Domain: "local-ci",
		}
	}
	for _, binding := range bindings {
		if expected, found := wanted[binding.ID]; !found || binding != expected {
			t.Errorf("unexpected review binding %#v", binding)
		}
		if strings.Contains(binding.ID.String(), options.Home) || strings.Contains(binding.Domain, options.Home) {
			t.Errorf("review binding leaks path: %#v", binding)
		}
	}
	if len(wanted) != len(bindings) {
		t.Fatalf("wanted bindings not exhausted: %#v", wanted)
	}
	if scope.Fingerprint() == "" || scope.FullFingerprint() == "" ||
		scope.Fingerprint() == scope.FullFingerprint() {
		t.Fatalf("scope/full fingerprints = %q/%q", scope.Fingerprint(), scope.FullFingerprint())
	}

	wantRequired := make([]database.StoreID, 0)
	for _, binding := range bindings {
		if binding.Required {
			wantRequired = append(wantRequired, binding.ID)
		}
	}
	if required := scope.RequiredStores(); !slices.Equal(required, wantRequired) {
		t.Fatalf("required stores = %#v, want %#v", required, wantRequired)
	}

	bindings[0] = ReviewBinding{ID: "forged/id", Domain: "forged"}
	required := scope.RequiredStores()
	required[0] = "forged/id"
	if scope.Bindings()[0].ID == "forged/id" || scope.RequiredStores()[0] == "forged/id" {
		t.Fatal("review scope retained caller mutation")
	}

	slices.Reverse(options.Config.Agents.List)
	reordered, err := NewReviewScope(options, "missing")
	if err != nil || reordered.Fingerprint() != scope.Fingerprint() ||
		reordered.FullFingerprint() != scope.FullFingerprint() ||
		!slices.Equal(reordered.Bindings(), scope.Bindings()) {
		t.Fatalf("reordered scope = %#v, %v", reordered, err)
	}
}

func compareReviewBindings(left, right ReviewBinding) int {
	return strings.Compare(left.ID.String(), right.ID.String())
}

func TestReviewScopeCatalogBridgeReturnsDetachedFullAndSelectedCatalogs(t *testing.T) {
	options := reviewScopeTestOptions(t, nil)
	scope, err := NewReviewScope(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	full, selected, err := ReviewScopeCatalogs(scope)
	if err != nil {
		t.Fatal(err)
	}
	if full == nil || selected == nil || len(full.All()) <= len(selected.All()) ||
		len(selected.All()) != len(scope.Bindings()) {
		t.Fatalf("scope catalogs full=%#v selected=%#v", full, selected)
	}
	for _, binding := range scope.Bindings() {
		spec, found := selected.Lookup(binding.ID)
		if !found || spec.Domain != binding.Domain || spec.Required != binding.Required || spec.Path == "" {
			t.Fatalf("selected spec for %#v = %#v, %t", binding, spec, found)
		}
	}
	full.specs[0].ID = "forged/full"
	selected.specs[0].LegacyRoots = append(selected.specs[0].LegacyRoots, "forged")
	repeatedFull, repeatedSelected, err := ReviewScopeCatalogs(scope)
	if err != nil || repeatedFull.specs[0].ID == "forged/full" ||
		slices.Contains(repeatedSelected.specs[0].LegacyRoots, "forged") {
		t.Fatalf("catalog bridge retained mutation: %#v %#v %v", repeatedFull, repeatedSelected, err)
	}

	strict, err := RevalidateReviewScope(scope)
	if err != nil || strict.Fingerprint() != scope.Fingerprint() ||
		strict.FullFingerprint() != scope.FullFingerprint() {
		t.Fatalf("revalidated scope = %#v, %v", strict, err)
	}
}

func TestReviewScopeFingerprintBindsFullCatalogAndRequiredPolicy(t *testing.T) {
	options := reviewScopeTestOptions(t, nil)
	first, err := NewReviewScope(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := NewReviewScope(options, "missing")
	if err != nil || repeated.Fingerprint() != first.Fingerprint() ||
		repeated.FullFingerprint() != first.FullFingerprint() {
		t.Fatalf("repeated scope = %#v, %v", repeated, err)
	}

	options.Config.Workflows.Enabled = !options.Config.Workflows.Enabled
	unselectedChange, err := NewReviewScope(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if unselectedChange.Fingerprint() == first.Fingerprint() ||
		unselectedChange.FullFingerprint() == first.FullFingerprint() ||
		!slices.Equal(unselectedChange.Bindings(), first.Bindings()) {
		t.Fatalf("unselected change scope = %#v, first %#v", unselectedChange, first)
	}

	options.Config.Events.Ingress.Enabled = true
	requiredChange, err := NewReviewScope(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if requiredChange.Fingerprint() == unselectedChange.Fingerprint() ||
		len(requiredChange.RequiredStores()) <= len(unselectedChange.RequiredStores()) {
		t.Fatalf("required policy did not bind scope: before=%#v after=%#v",
			unselectedChange.Bindings(), requiredChange.Bindings())
	}
	if first.Fingerprint() == (mustReviewScope(t, options, "sha256:"+strings.Repeat("a", 64))).Fingerprint() {
		t.Fatal("configuration revision did not bind review scope")
	}
}

func mustReviewScope(t *testing.T, options Options, revision string) *ReviewScope {
	t.Helper()
	scope, err := NewReviewScope(options, revision)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func TestReviewScopeRejectsMalformedOrIncompletePolicy(t *testing.T) {
	options := reviewScopeTestOptions(t, nil)
	options.Config.Agents.List = []config.AgentConfig{{
		ID: "dynamic", Workspace: filepath.Join(options.Home, "dynamic"),
	}}
	projected, err := Project(options)
	if err != nil {
		t.Fatal(err)
	}
	dynamicPrefix := database.StoreID("workspace/" + shortPathID(options.Config.Agents.List[0].Workspace) + "/")
	for _, test := range []struct {
		name   string
		mutate func(*Catalog)
	}{
		{name: "missing fixed", mutate: func(catalog *Catalog) {
			removeCatalogSpec(catalog, "workspace/repository-reviews")
		}},
		{name: "wrong domain", mutate: func(catalog *Catalog) {
			setCatalogSpecDomain(catalog, "workspace/repository-reviews", "workflows")
		}},
		{name: "incomplete dynamic", mutate: func(catalog *Catalog) {
			removeCatalogSpec(catalog, dynamicPrefix+"local-ci")
		}},
		{name: "omitted dynamic group", mutate: func(catalog *Catalog) {
			removeCatalogSpec(catalog, dynamicPrefix+"repository-reviews")
			removeCatalogSpec(catalog, dynamicPrefix+"repository-evaluations")
			removeCatalogSpec(catalog, dynamicPrefix+"local-ci")
		}},
		{name: "malformed dynamic", mutate: func(catalog *Catalog) {
			setCatalogSpecID(catalog, dynamicPrefix+"local-ci", "workspace/not-hex/local-ci")
		}},
		{name: "uppercase dynamic key", mutate: func(catalog *Catalog) {
			setCatalogSpecID(catalog, dynamicPrefix+"local-ci", "workspace/0123456789abcdeF/local-ci")
		}},
		{name: "unknown selected identity", mutate: func(catalog *Catalog) {
			setCatalogSpecID(catalog, dynamicPrefix+"local-ci", "global/unknown/local-ci")
		}},
		{name: "selected domain outside policy", mutate: func(catalog *Catalog) {
			setCatalogSpecDomain(catalog, "workspace/workflows", "repository-reviews")
		}},
		{name: "duplicate", mutate: func(catalog *Catalog) {
			catalog.specs = append(catalog.specs, cloneSpec(catalog.specs[0]))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneCatalog(projected)
			test.mutate(candidate)
			if scope, err := newReviewScope(candidate, "missing"); scope != nil || err == nil {
				t.Fatalf("malformed scope = %#v, %v", scope, err)
			}
		})
	}
	if scope, err := NewReviewScope(options, "bad revision"); scope != nil || err == nil {
		t.Fatalf("invalid revision scope = %#v, %v", scope, err)
	}
	if scope, err := newReviewScope(nil, "missing"); scope != nil || err == nil {
		t.Fatalf("nil catalog scope = %#v, %v", scope, err)
	}
}

func TestNewReviewScopeRejectsSelectedUnselectedLexicalAlias(t *testing.T) {
	options := reviewScopeTestOptions(t, nil)
	options.Config.Events.Ingress.DatabasePath = filepath.Join(
		options.Config.Agents.Defaults.Workspace,
		"repository_reviews",
		"repository-reviews.db",
	)
	if scope, err := NewReviewScope(options, "missing"); scope != nil || err == nil {
		t.Fatalf("lexically aliased review scope = %#v, %v", scope, err)
	}
}

func removeCatalogSpec(catalog *Catalog, id database.StoreID) {
	for index := range catalog.specs {
		if catalog.specs[index].ID == id {
			catalog.specs = append(catalog.specs[:index], catalog.specs[index+1:]...)
			return
		}
	}
}

func setCatalogSpecDomain(catalog *Catalog, id database.StoreID, domain string) {
	for index := range catalog.specs {
		if catalog.specs[index].ID == id {
			catalog.specs[index].Domain = domain
			return
		}
	}
}

func setCatalogSpecID(catalog *Catalog, id, replacement database.StoreID) {
	for index := range catalog.specs {
		if catalog.specs[index].ID == id {
			catalog.specs[index].ID = replacement
			return
		}
	}
}

func TestReviewScopeRejectsSelectedUnselectedPhysicalAlias(t *testing.T) {
	options := reviewScopeTestOptions(t, nil)
	reviewPath := filepath.Join(options.Config.Agents.Defaults.Workspace, "repository_reviews", "repository-reviews.db")
	workflowPath := filepath.Join(options.Config.Agents.Defaults.Workspace, "state", "workflows.db")
	requireReviewScopeTestPath(t, options.Home, "review alias fixture", reviewPath)
	requireReviewScopeTestPath(t, options.Home, "workflow alias fixture", workflowPath)
	if err := os.MkdirAll(filepath.Dir(reviewPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(workflowPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reviewPath, []byte("shared"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(reviewPath, workflowPath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	scope, err := NewReviewScope(options, "missing")
	if err != nil {
		t.Fatalf("projected scope inspected generation alias: %v", err)
	}
	if strict, err := RevalidateReviewScope(scope); strict != nil || err == nil {
		t.Fatalf("revalidated aliased scope = %#v, %v", strict, err)
	}
}

func TestReviewScopeInvalidValuesAndFingerprintBoundaries(t *testing.T) {
	var nilScope *ReviewScope
	if nilScope.Fingerprint() != "" || nilScope.FullFingerprint() != "" ||
		nilScope.Bindings() != nil || nilScope.RequiredStores() != nil {
		t.Fatal("nil review scope exposed state")
	}
	if full, selected, err := ReviewScopeCatalogs(nil); full != nil || selected != nil || err == nil {
		t.Fatalf("nil scope catalogs = %#v, %#v, %v", full, selected, err)
	}
	if scope, err := RevalidateReviewScope(nil); scope != nil || err == nil {
		t.Fatalf("nil revalidation = %#v, %v", scope, err)
	}
	if cloneCatalog(nil) != nil {
		t.Fatal("nil catalog clone returned data")
	}

	valid := []ReviewBinding{{ID: "global/a", Domain: "a", Required: true}}
	validFingerprint, err := reviewScopeFingerprint("sha256:"+strings.Repeat("a", 64), valid)
	if err != nil || len(validFingerprint) != len("sha256:")+64 ||
		!strings.HasPrefix(validFingerprint, "sha256:") {
		t.Fatalf("valid review fingerprint = %q, %v", validFingerprint, err)
	}
	optional := append([]ReviewBinding(nil), valid...)
	optional[0].Required = false
	optionalFingerprint, err := reviewScopeFingerprint("sha256:"+strings.Repeat("a", 64), optional)
	if err != nil || optionalFingerprint == validFingerprint {
		t.Fatalf("required policy fingerprints = %q/%q, %v", validFingerprint, optionalFingerprint, err)
	}
	for _, bindings := range [][]ReviewBinding{
		nil,
		{{ID: "bad id", Domain: "a"}},
		{{ID: "global/a", Domain: "bad_domain"}},
		{{ID: "global/b", Domain: "b"}, {ID: "global/a", Domain: "a"}},
		{{ID: "global/a", Domain: "a"}, {ID: "global/a", Domain: "a"}},
	} {
		if fingerprint, err := reviewScopeFingerprint(
			"sha256:"+strings.Repeat("a", 64),
			bindings,
		); fingerprint != "" ||
			err == nil {
			t.Errorf("invalid review fingerprint = %q, %v for %#v", fingerprint, err, bindings)
		}
	}
	if fingerprint, err := reviewScopeFingerprint("missing", valid); fingerprint != "" || err == nil {
		t.Fatalf("missing full fingerprint accepted: %q, %v", fingerprint, err)
	}
}

func TestReviewScopeRejectsRetainedStateTampering(t *testing.T) {
	options := reviewScopeTestOptions(t, nil)
	scope, err := NewReviewScope(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*ReviewScope)
	}{
		{name: "nil full catalog", mutate: func(candidate *ReviewScope) {
			candidate.full = nil
		}},
		{name: "nil selected catalog", mutate: func(candidate *ReviewScope) {
			candidate.selected = nil
		}},
		{name: "empty scope fingerprint", mutate: func(candidate *ReviewScope) {
			candidate.fingerprint = ""
		}},
		{name: "empty full fingerprint", mutate: func(candidate *ReviewScope) {
			candidate.fullFingerprint = ""
		}},
		{name: "invalid revision", mutate: func(candidate *ReviewScope) {
			candidate.configRevision = "invalid"
		}},
		{name: "changed scope fingerprint", mutate: func(candidate *ReviewScope) {
			candidate.fingerprint = "sha256:" + strings.Repeat("a", 64)
		}},
		{name: "changed full fingerprint", mutate: func(candidate *ReviewScope) {
			candidate.fullFingerprint = "sha256:" + strings.Repeat("a", 64)
		}},
		{name: "missing binding", mutate: func(candidate *ReviewScope) {
			candidate.bindings = candidate.bindings[:len(candidate.bindings)-1]
		}},
		{name: "changed binding", mutate: func(candidate *ReviewScope) {
			candidate.bindings[0].Required = !candidate.bindings[0].Required
		}},
		{name: "missing required store", mutate: func(candidate *ReviewScope) {
			candidate.requiredStores = candidate.requiredStores[:len(candidate.requiredStores)-1]
		}},
		{name: "changed required store", mutate: func(candidate *ReviewScope) {
			candidate.requiredStores[0] = "forged/id"
		}},
		{name: "changed selected home", mutate: func(candidate *ReviewScope) {
			candidate.selected.home += "-forged"
		}},
		{name: "missing selected spec", mutate: func(candidate *ReviewScope) {
			candidate.selected.specs = candidate.selected.specs[:len(candidate.selected.specs)-1]
		}},
		{name: "changed selected spec ID", mutate: func(candidate *ReviewScope) {
			candidate.selected.specs[0].ID = "forged/id"
		}},
		{name: "changed selected spec domain", mutate: func(candidate *ReviewScope) {
			candidate.selected.specs[0].Domain = "forged"
		}},
		{name: "changed selected spec path", mutate: func(candidate *ReviewScope) {
			candidate.selected.specs[0].Path += "-forged"
		}},
		{name: "changed selected required policy", mutate: func(candidate *ReviewScope) {
			candidate.selected.specs[0].Required = !candidate.selected.specs[0].Required
		}},
		{name: "changed selected legacy roots", mutate: func(candidate *ReviewScope) {
			candidate.selected.specs[0].LegacyRoots = append(
				candidate.selected.specs[0].LegacyRoots,
				filepath.Join(options.Home, "forged"),
			)
		}},
		{name: "changed selected legacy root", mutate: func(candidate *ReviewScope) {
			candidate.selected.specs[0].LegacyRoots[0] += "-forged"
		}},
		{name: "changed full catalog", mutate: func(candidate *ReviewScope) {
			candidate.full.specs[0].Required = !candidate.full.specs[0].Required
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneReviewScopeForTest(scope)
			test.mutate(candidate)
			if full, selected, err := ReviewScopeCatalogs(candidate); full != nil || selected != nil || err == nil {
				t.Fatalf("tampered catalog bridge = %#v, %#v, %v", full, selected, err)
			}
			if revalidated, err := RevalidateReviewScope(candidate); revalidated != nil || err == nil {
				t.Fatalf("tampered revalidation = %#v, %v", revalidated, err)
			}
		})
	}
}

func TestValidReviewWorkspaceKeyRejectsInvalidHexCharacter(t *testing.T) {
	if !validReviewWorkspaceKey("0123456789abcdef") {
		t.Fatal("valid lowercase hexadecimal workspace key was rejected")
	}
	if validReviewWorkspaceKey("0123456789abcdeg") {
		t.Fatal("workspace key with non-hexadecimal character was accepted")
	}
}

func TestCatalogRejectsInvalidLstatInputsAndOverlongAgentEventPath(t *testing.T) {
	root := t.TempDir()
	badGeneration := filepath.Join(root, "bad\x00-generation.db")
	badLegacy := filepath.Join(root, "bad\x00-legacy.json")
	requireReviewScopeTestPath(t, root, "invalid generation fixture", badGeneration)
	requireReviewScopeTestPath(t, root, "invalid legacy fixture", badLegacy)
	if err := validateSpecsWithPathKeyMode([]Spec{{
		ID: "workspace/bad-generation", Path: badGeneration,
	}}, catalogPathKey, true); err == nil {
		t.Fatal("generation path with an invalid Lstat input was accepted")
	}
	if err := validateSpecsWithPathKeyMode([]Spec{{
		ID: "workspace/bad-legacy", Path: filepath.Join(root, "valid.db"),
		LegacyRoots: []string{badLegacy},
	}}, catalogPathKey, true); err == nil {
		t.Fatal("legacy path with an invalid Lstat input was accepted")
	}

	if runtime.GOOS != "linux" {
		t.Skip("overlong path boundary is Linux-specific")
	}
	options := reviewScopeTestOptions(t, nil)
	workspace := options.Home
	for len(workspace) < 4080 {
		componentLength := min(200, 4080-len(workspace)-1)
		if componentLength <= 0 {
			break
		}
		workspace = filepath.Join(workspace, strings.Repeat("a", componentLength))
	}
	requireReviewScopeTestPath(t, options.Home, "overlong agent workspace", workspace)
	options.Config.Agents.List = []config.AgentConfig{{ID: "overlong", Workspace: workspace}}
	projected, err := Project(options)
	if err != nil {
		t.Fatalf("lexical projection of bounded workspace: %v", err)
	}
	for _, spec := range projected.All() {
		requireReviewScopeTestPath(t, options.Home, "projected store "+spec.ID.String(), spec.Path)
		for _, legacyRoot := range spec.LegacyRoots {
			requireReviewScopeTestPath(t, options.Home, "projected legacy "+spec.ID.String(), legacyRoot)
		}
	}
	if catalog, err := Build(options); catalog != nil || err == nil ||
		!strings.Contains(err.Error(), "agent \"overlong\" event store") {
		t.Fatalf("overlong agent event store = %#v, %v", catalog, err)
	}
}

func cloneReviewScopeForTest(scope *ReviewScope) *ReviewScope {
	if scope == nil {
		return nil
	}
	clone := *scope
	clone.full = cloneCatalog(scope.full)
	clone.selected = cloneCatalog(scope.selected)
	clone.bindings = append([]ReviewBinding(nil), scope.bindings...)
	clone.requiredStores = append([]database.StoreID(nil), scope.requiredStores...)
	return &clone
}
