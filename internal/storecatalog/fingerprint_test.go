package storecatalog

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func fingerprintTestCatalog() *Catalog {
	return fingerprintTestCatalogFromSpecs([]Spec{
		{
			ID:     "workspace/sessions",
			Domain: "sessions",
			Path:   fingerprintTestPath("workspace", "sessions.db"),
			LegacyRoots: []string{
				fingerprintTestPath("workspace", "sessions"),
				fingerprintTestPath("workspace", "threads"),
			},
		},
		{
			ID:          "global/auth",
			Domain:      "auth",
			Path:        fingerprintTestPath("home", "auth.db"),
			LegacyRoots: []string{fingerprintTestPath("home", "auth.json")},
			Required:    true,
		},
	})
}

func fingerprintTestPath(elements ...string) string {
	root := "/trusted"
	if runtime.GOOS == "windows" {
		root = `C:\trusted`
	}
	return filepath.Join(append([]string{root}, elements...)...)
}

func fingerprintTestCatalogFromSpecs(specs []Spec) *Catalog {
	cloned := make([]Spec, len(specs))
	byID := make(map[string]int, len(specs))
	for index, spec := range specs {
		cloned[index] = cloneSpec(spec)
		byID[spec.ID] = index
	}
	return &Catalog{Home: fingerprintTestPath("home"), Specs: cloned, byID: byID}
}

func cloneFingerprintTestCatalog(source *Catalog) *Catalog {
	if source == nil {
		return nil
	}
	clone := fingerprintTestCatalogFromSpecs(source.Specs)
	clone.Home = source.Home
	return clone
}

func TestCatalogFingerprintHasExactStableLowercaseEncoding(t *testing.T) {
	catalog := fingerprintTestCatalog()
	wantOrder := []string{catalog.Specs[0].ID, catalog.Specs[1].ID}
	wantLegacy := append([]string(nil), catalog.Specs[0].LegacyRoots...)

	first, err := catalog.Fingerprint("missing")
	if err != nil {
		t.Fatal(err)
	}
	expected := "sha256:38991d2d4b73bb7cf1273ba5d34b215a4c0e06f1d64d7ef7d621968981adeac4"
	if runtime.GOOS == "windows" {
		expected = "sha256:193a30c2925d8c50674aea92396870be3761f40d8ecc05bb2ed7d789c0969306"
	}
	if first != expected {
		t.Fatalf("Fingerprint() = %q, want %q", first, expected)
	}
	if len(first) != len(catalogFingerprintPrefix)+64 || !strings.HasPrefix(first, catalogFingerprintPrefix) ||
		first != strings.ToLower(first) {
		t.Fatalf("fingerprint is not exact lowercase SHA-256 form: %q", first)
	}
	for _, character := range strings.TrimPrefix(first, catalogFingerprintPrefix) {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			t.Fatalf("fingerprint contains non-lowercase-hex character %q", character)
		}
	}

	second, err := catalog.Fingerprint("missing")
	if err != nil || second != first {
		t.Fatalf("repeat Fingerprint() = %q, %v; want %q", second, err, first)
	}
	if got := []string{catalog.Specs[0].ID, catalog.Specs[1].ID}; !slices.Equal(got, wantOrder) {
		t.Fatalf("Fingerprint sorted retained specs: %q", got)
	}
	if !slices.Equal(catalog.Specs[0].LegacyRoots, wantLegacy) {
		t.Fatalf("Fingerprint mutated retained legacy order: %q", catalog.Specs[0].LegacyRoots)
	}
}

func TestCatalogFingerprintIsStableAcrossSpecPermutation(t *testing.T) {
	first := fingerprintTestCatalog()
	second := fingerprintTestCatalogFromSpecs([]Spec{first.Specs[1], first.Specs[0]})

	firstValue, err := first.Fingerprint("missing")
	if err != nil {
		t.Fatal(err)
	}
	secondValue, err := second.Fingerprint("missing")
	if err != nil {
		t.Fatal(err)
	}
	if firstValue != secondValue {
		t.Fatalf("permuted fingerprints differ: %q != %q", firstValue, secondValue)
	}
}

func TestCatalogFingerprintBindsEveryCatalogValue(t *testing.T) {
	base := fingerprintTestCatalog()
	baseValue, err := base.Fingerprint("missing")
	if err != nil {
		t.Fatal(err)
	}

	mutations := []struct {
		name     string
		revision string
		mutate   func(*Catalog)
	}{
		{name: "home", revision: "missing", mutate: func(catalog *Catalog) { catalog.Home += "-other" }},
		{name: "revision", revision: catalogFingerprintPrefix + strings.Repeat("a", 64)},
		{name: "ID", revision: "missing", mutate: func(catalog *Catalog) {
			catalog.Specs[0].ID = "workspace/threads"
		}},
		{name: "domain", revision: "missing", mutate: func(catalog *Catalog) {
			catalog.Specs[0].Domain = "threads"
		}},
		{name: "path", revision: "missing", mutate: func(catalog *Catalog) {
			catalog.Specs[0].Path += "-other"
		}},
		{name: "required", revision: "missing", mutate: func(catalog *Catalog) {
			catalog.Specs[0].Required = !catalog.Specs[0].Required
		}},
		{name: "legacy value", revision: "missing", mutate: func(catalog *Catalog) {
			catalog.Specs[0].LegacyRoots[0] += "-other"
		}},
		{name: "legacy order", revision: "missing", mutate: func(catalog *Catalog) {
			roots := catalog.Specs[0].LegacyRoots
			roots[0], roots[1] = roots[1], roots[0]
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			catalog := cloneFingerprintTestCatalog(base)
			if test.mutate != nil {
				test.mutate(catalog)
			}
			value, fingerprintErr := catalog.Fingerprint(test.revision)
			if fingerprintErr != nil {
				t.Fatal(fingerprintErr)
			}
			if value == baseValue {
				t.Fatalf("%s change retained fingerprint %q", test.name, value)
			}
		})
	}
}

func TestCatalogFingerprintLengthFramesAmbiguousValues(t *testing.T) {
	first := fingerprintTestCatalogFromSpecs([]Spec{{
		ID: "a", Domain: "bc", Path: fingerprintTestPath("home", "auth.db"),
	}})
	second := fingerprintTestCatalogFromSpecs([]Spec{{
		ID: "ab", Domain: "c", Path: fingerprintTestPath("home", "auth.db"),
	}})
	if first.Specs[0].ID+first.Specs[0].Domain !=
		second.Specs[0].ID+second.Specs[0].Domain {
		t.Fatal("length-framing fixtures are not concatenation-ambiguous")
	}
	firstValue, err := first.Fingerprint("missing")
	if err != nil {
		t.Fatal(err)
	}
	secondValue, err := second.Fingerprint("missing")
	if err != nil {
		t.Fatal(err)
	}
	if firstValue == secondValue {
		t.Fatalf("length-ambiguous catalogs share fingerprint %q", firstValue)
	}
}

func TestCatalogFingerprintRejectsInvalidForgedInputsWithoutLeakage(t *testing.T) {
	valid := fingerprintTestCatalog().Specs[0]
	secret := fingerprintTestPath("secret", "provider", "store.db")
	uncleanSecret := secret + string(os.PathSeparator) + ".." + string(os.PathSeparator) + filepath.Base(secret)
	physicalOther := fingerprintTestPath("secret", "provider", "other.db")
	tests := []struct {
		name     string
		catalog  *Catalog
		revision string
	}{
		{name: "nil catalog", revision: "missing"},
		{name: "zero catalog", catalog: &Catalog{}, revision: "missing"},
		{name: "empty home", catalog: &Catalog{Specs: []Spec{valid}}, revision: "missing"},
		{name: "NUL home", catalog: &Catalog{Home: secret + "\x00", Specs: []Spec{valid}}, revision: "missing"},
		{name: "relative home", catalog: &Catalog{Home: "relative-home", Specs: []Spec{valid}}, revision: "missing"},
		{name: "unclean home", catalog: &Catalog{Home: uncleanSecret, Specs: []Spec{valid}}, revision: "missing"},
		{name: "empty specs", catalog: &Catalog{Home: fingerprintTestPath("home")}, revision: "missing"},
		{name: "invalid ID", catalog: fingerprintTestCatalogFromSpecs([]Spec{{
			ID: "bad//id", Domain: "auth", Path: secret,
		}}), revision: "missing"},
		{name: "empty domain", catalog: fingerprintTestCatalogFromSpecs([]Spec{{
			ID: "global/auth", Path: secret,
		}}), revision: "missing"},
		{name: "long domain", catalog: fingerprintTestCatalogFromSpecs([]Spec{{
			ID: "global/auth", Domain: strings.Repeat("a", maximumFingerprintDomainBytes+1), Path: secret,
		}}), revision: "missing"},
		{name: "leading domain hyphen", catalog: fingerprintTestCatalogFromSpecs([]Spec{{
			ID: "global/auth", Domain: "-auth", Path: secret,
		}}), revision: "missing"},
		{name: "trailing domain hyphen", catalog: fingerprintTestCatalogFromSpecs([]Spec{{
			ID: "global/auth", Domain: "auth-", Path: secret,
		}}), revision: "missing"},
		{name: "invalid domain character", catalog: fingerprintTestCatalogFromSpecs([]Spec{{
			ID: "global/auth", Domain: "auth_name", Path: secret,
		}}), revision: "missing"},
		{name: "empty path", catalog: fingerprintTestCatalogFromSpecs([]Spec{{
			ID: "global/auth", Domain: "auth",
		}}), revision: "missing"},
		{name: "NUL path", catalog: fingerprintTestCatalogFromSpecs([]Spec{{
			ID: "global/auth", Domain: "auth", Path: secret + "\x00",
		}}), revision: "missing"},
		{name: "relative path", catalog: fingerprintTestCatalogFromSpecs([]Spec{{
			ID: "global/auth", Domain: "auth", Path: "relative.db",
		}}), revision: "missing"},
		{name: "unclean path", catalog: fingerprintTestCatalogFromSpecs([]Spec{{
			ID: "global/auth", Domain: "auth", Path: uncleanSecret,
		}}), revision: "missing"},
		{name: "duplicate ID", catalog: fingerprintTestCatalogFromSpecs([]Spec{
			{ID: "global/auth", Domain: "auth", Path: secret},
			{ID: "global/auth", Domain: "auth-other", Path: secret + "-other"},
		}), revision: "missing"},
		{name: "duplicate generation", catalog: fingerprintTestCatalogFromSpecs([]Spec{
			{ID: "global/auth", Domain: "auth", Path: secret},
			{ID: "launcher/auth", Domain: "launcher-auth", Path: secret},
		}), revision: "missing"},
		{name: "empty legacy root", catalog: fingerprintTestCatalogFromSpecs([]Spec{{
			ID: "global/auth", Domain: "auth", Path: secret, LegacyRoots: []string{""},
		}}), revision: "missing"},
		{name: "NUL legacy root", catalog: fingerprintTestCatalogFromSpecs([]Spec{{
			ID: "global/auth", Domain: "auth", Path: secret, LegacyRoots: []string{secret + "\x00"},
		}}), revision: "missing"},
		{name: "relative legacy root", catalog: fingerprintTestCatalogFromSpecs([]Spec{{
			ID: "global/auth", Domain: "auth", Path: secret, LegacyRoots: []string{"relative.json"},
		}}), revision: "missing"},
		{name: "unclean legacy root", catalog: fingerprintTestCatalogFromSpecs([]Spec{{
			ID: "global/auth", Domain: "auth", Path: secret, LegacyRoots: []string{uncleanSecret},
		}}), revision: "missing"},
		{name: "physical path collision", catalog: fingerprintTestCatalogFromSpecs([]Spec{
			{ID: "global/auth", Domain: "auth", Path: secret},
			{ID: "global/other", Domain: "other", Path: secret},
		}), revision: "missing"},
		{name: "generation legacy collision", catalog: fingerprintTestCatalogFromSpecs([]Spec{
			{ID: "global/auth", Domain: "auth", Path: secret},
			{ID: "global/other", Domain: "other", Path: physicalOther, LegacyRoots: []string{secret}},
		}), revision: "missing"},
	}
	invalidRevisions := []string{
		"", " missing", "missing ", "sha256:", "SHA256:" + strings.Repeat("a", 64),
		catalogFingerprintPrefix + strings.Repeat("a", 63),
		catalogFingerprintPrefix + strings.Repeat("a", 63) + "g",
		catalogFingerprintPrefix + strings.Repeat("A", 64),
	}
	allTests := make([]struct {
		name     string
		catalog  *Catalog
		revision string
	}, 0, len(tests)+len(invalidRevisions))
	allTests = append(allTests, tests...)
	for _, revision := range invalidRevisions {
		allTests = append(allTests, struct {
			name     string
			catalog  *Catalog
			revision string
		}{name: "invalid revision " + revision, catalog: fingerprintTestCatalog(), revision: revision})
	}

	for _, test := range allTests {
		t.Run(test.name, func(t *testing.T) {
			value, err := test.catalog.Fingerprint(test.revision)
			if value != "" || !errors.Is(err, errCatalogFingerprintInput) {
				t.Fatalf("Fingerprint() = %q, %v", value, err)
			}
			if err.Error() != "database catalog fingerprint input is invalid" ||
				strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "bad//id") {
				t.Fatalf("fingerprint error leaked input: %v", err)
			}
		})
	}
}

func TestProjectedAndBuiltCatalogFingerprintsMatch(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	options := Options{Home: home, Config: cfg, ConfigPath: filepath.Join(home, "config.json")}

	projected, err := Project(options)
	if err != nil {
		t.Fatal(err)
	}
	built, err := Build(options)
	if err != nil {
		t.Fatal(err)
	}
	projectedValue, err := projected.Fingerprint("missing")
	if err != nil {
		t.Fatal(err)
	}
	builtValue, err := built.Fingerprint("missing")
	if err != nil {
		t.Fatal(err)
	}
	if projectedValue != builtValue {
		t.Fatalf("Project/Build fingerprints differ: %q != %q", projectedValue, builtValue)
	}
}

func TestCatalogFingerprintDoesNotInspectChangedGeneration(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	options := Options{Home: home, Config: cfg}
	projected, err := Project(options)
	if err != nil {
		t.Fatal(err)
	}
	before, err := projected.Fingerprint("missing")
	if err != nil {
		t.Fatal(err)
	}

	if mkdirErr := os.Mkdir(filepath.Join(home, "auth.db"), 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	after, err := projected.Fingerprint("missing")
	if err != nil {
		t.Fatalf("Fingerprint inspected changed generation: %v", err)
	}
	if after != before {
		t.Fatalf("generation metadata changed fingerprint: %q != %q", after, before)
	}
	if _, err := Build(options); err == nil {
		t.Fatal("Build accepted changed generation fixture")
	}
}
