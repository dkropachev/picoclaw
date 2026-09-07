package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func logicalCatalogTestOptions(t *testing.T, cfg *config.Config) Options {
	t.Helper()

	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	if cfg.Agents.Defaults.Workspace == "" {
		cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	}
	return Options{Home: home, Config: cfg}
}

func TestCatalogProjectsSortedDetachedLogicalEntries(t *testing.T) {
	cfg := config.DefaultConfig()
	options := logicalCatalogTestOptions(t, cfg)
	catalog, err := New(options)
	if err != nil {
		t.Fatal(err)
	}

	entries := catalog.Entries()
	if len(entries) == 0 {
		t.Fatal("logical catalog is empty")
	}
	if !slices.IsSortedFunc(entries, func(left, right Entry) int {
		return strings.Compare(left.ID.String(), right.ID.String())
	}) {
		t.Fatalf("entries are not ID-sorted: %#v", entries)
	}

	var auth Entry
	for _, entry := range entries {
		if !entry.ID.Valid() {
			t.Errorf("invalid projected ID %q", entry.ID)
		}
		if entry.Domain == "" {
			t.Errorf("empty domain for %q", entry.ID)
		}
		if strings.Contains(entry.ID.String(), `\`) || strings.Contains(entry.ID.String(), ".db") ||
			filepath.IsAbs(entry.ID.String()) {
			t.Errorf("logical entry leaks physical identity: %#v", entry)
		}
		if entry.ID == "global/auth" {
			auth = entry
		}
	}
	if auth != (Entry{ID: "global/auth", Domain: "auth", Required: true}) {
		t.Fatalf("global auth entry = %#v", auth)
	}

	entries[0] = Entry{ID: "forged/id", Domain: "forged", Required: !entries[0].Required}
	if slices.Equal(entries, catalog.Entries()) {
		t.Fatal("Entries returned retained storage")
	}
	if got, found := catalog.Entry("global/auth"); !found || got != auth {
		t.Fatalf("Entry(global/auth) = %#v, %t", got, found)
	}
	if got, found := catalog.Entry("forged/id"); found || got != (Entry{}) {
		t.Fatalf("forged entry became retained: %#v, %t", got, found)
	}
	beforeConfigMutation := catalog.Entries()
	cfg.Agents.Defaults.Workspace = filepath.Join(options.Home, "other-workspace")
	cfg.Workflows.Enabled = !cfg.Workflows.Enabled
	if !slices.Equal(beforeConfigMutation, catalog.Entries()) {
		t.Fatal("catalog retained caller configuration")
	}
}

func TestCatalogExactLookupEntryAndContains(t *testing.T) {
	catalog, err := New(logicalCatalogTestOptions(t, nil))
	if err != nil {
		t.Fatal(err)
	}

	id, err := catalog.Lookup("global/auth")
	if err != nil || id != "global/auth" {
		t.Fatalf("Lookup(global/auth) = %q, %v", id, err)
	}
	if !catalog.Contains(id) || catalog.Contains("global/missing") || catalog.Contains("bad//id") ||
		catalog.Contains("") {
		t.Fatal("Contains did not enforce exact catalog membership")
	}
	if _, found := catalog.Entry("bad//id"); found {
		t.Fatal("Entry accepted invalid ID")
	}

	for _, value := range []string{
		" global/auth", "global/auth ", "GLOBAL/auth", "/tmp/auth.db", "file:auth.db", "",
	} {
		if _, lookupErr := catalog.Lookup(value); database.CodeOf(lookupErr) != database.CodeInvalid {
			t.Errorf("Lookup(%q) error = %v", value, lookupErr)
		}
	}
	for _, value := range []string{"global/missing", "global.auth"} {
		if _, lookupErr := catalog.Lookup(value); database.CodeOf(lookupErr) != database.CodeNotFound {
			t.Errorf("Lookup(%q) error = %v", value, lookupErr)
		}
	}

	var nilCatalog *Catalog
	if nilCatalog.Entries() != nil {
		t.Fatal("nil catalog returned entries")
	}
	if entry, found := nilCatalog.Entry("global/auth"); found || entry != (Entry{}) {
		t.Fatalf("nil Entry = %#v, %t", entry, found)
	}
	if nilCatalog.Contains("global/auth") {
		t.Fatal("nil catalog contains entry")
	}
	if _, lookupErr := nilCatalog.Lookup("global/auth"); database.CodeOf(lookupErr) != database.CodeUnavailable {
		t.Fatalf("nil Lookup error = %v", lookupErr)
	}
}

func TestCatalogLookupChannelRequiresConfiguredExactIdentity(t *testing.T) {
	matrix := &config.Channel{Enabled: true, Type: config.ChannelMatrix}
	if err := matrix.Decode(&config.MatrixSettings{
		CryptoDatabasePath: filepath.Join(t.TempDir(), "matrix"),
		CryptoPassphrase:   "configured",
	}); err != nil {
		t.Fatal(err)
	}
	whatsapp := &config.Channel{Enabled: true, Type: config.ChannelWhatsAppNative}
	if err := whatsapp.Decode(&config.WhatsAppSettings{
		SessionStorePath: filepath.Join(t.TempDir(), "whatsapp"),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Channels = config.ChannelsConfig{
		"secure matrix": matrix,
		"work-phone":    whatsapp,
	}
	catalog, err := New(logicalCatalogTestOptions(t, cfg))
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		channelType string
		name        string
		prefix      string
	}{
		{channelType: config.ChannelMatrix, name: "secure matrix", prefix: "channel/matrix/secure-matrix-"},
		{channelType: config.ChannelWhatsAppNative, name: "work-phone", prefix: "channel/whatsapp/work-phone-"},
	} {
		id, lookupErr := catalog.LookupChannel(test.channelType, test.name)
		if lookupErr != nil || !strings.HasPrefix(id.String(), test.prefix) || !catalog.Contains(id) {
			t.Errorf("LookupChannel(%q, %q) = %q, %v", test.channelType, test.name, id, lookupErr)
		}
	}
	for _, test := range []struct{ channelType, name string }{
		{config.ChannelMatrix, "Secure Matrix"},
		{config.ChannelMatrix, "not configured"},
		{"unsupported", "secure matrix"},
	} {
		_, lookupErr := catalog.LookupChannel(test.channelType, test.name)
		if database.CodeOf(lookupErr) != database.CodeNotFound {
			t.Errorf("LookupChannel(%q, %q) error = %v", test.channelType, test.name, lookupErr)
		}
	}

	var nilCatalog *Catalog
	if _, lookupErr := nilCatalog.LookupChannel(
		config.ChannelMatrix, "secure matrix",
	); database.CodeOf(lookupErr) != database.CodeUnavailable {
		t.Fatalf("nil LookupChannel error = %v", lookupErr)
	}
}

func TestCatalogProjectionDoesNotInspectGenerationAliases(t *testing.T) {
	options := logicalCatalogTestOptions(t, nil)
	workspace := options.Config.Agents.Defaults.Workspace
	eventPath := filepath.Join(workspace, "eventing", "events.db")
	if err := os.MkdirAll(filepath.Dir(eventPath), 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(options.Home, "auth.db")
	if err := os.WriteFile(authPath, []byte("same physical file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(authPath, eventPath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	options.Config.Events.Ingress.Enabled = true
	if _, err := New(options); err != nil {
		t.Fatalf("logical projection inspected generation hardlinks: %v", err)
	}
}

func TestCatalogProjectionErrorsAreStructuredAndSanitized(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret-home") + "\x00"
	_, err := New(Options{Home: secret, Config: config.DefaultConfig()})
	if database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid projection code = %s, error = %v", database.CodeOf(err), err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(strings.ToLower(err.Error()), "sqlite") {
		t.Fatalf("projection error leaked implementation detail: %v", err)
	}

	for _, test := range []struct {
		input error
		code  database.ErrorCode
	}{
		{errors.New("secret /physical/store.db"), database.CodeInvalid},
		{database.NewError(database.CodeUnavailable, "secret /physical/store.db"), database.CodeUnavailable},
		{database.NewError(database.CodeUnauthorized, "secret /physical/store.db"), database.CodeUnauthorized},
		{database.NewError(database.CodeIntegrity, "secret /physical/store.db"), database.CodeIntegrity},
		{database.NewError(database.CodeDeadline, "secret /physical/store.db"), database.CodeInvalid},
	} {
		sanitized := sanitizeProjectionError(test.input)
		if database.CodeOf(sanitized) != test.code || strings.Contains(sanitized.Error(), "secret") ||
			strings.Contains(sanitized.Error(), "/physical") || strings.Contains(sanitized.Error(), ".db") {
			t.Errorf("sanitizeProjectionError(%v) = %v", test.input, sanitized)
		}
	}
}

func TestCatalogPublicValuesExposeOnlyLogicalMetadata(t *testing.T) {
	entryType := reflect.TypeOf(Entry{})
	expectedEntryFields := []struct {
		name   string
		typeOf reflect.Type
	}{
		{name: "ID", typeOf: reflect.TypeOf(database.StoreID(""))},
		{name: "Domain", typeOf: reflect.TypeOf("")},
		{name: "Required", typeOf: reflect.TypeOf(false)},
	}
	if entryType.NumField() != len(expectedEntryFields) {
		t.Fatalf("Entry field count = %d", entryType.NumField())
	}
	for index, expected := range expectedEntryFields {
		field := entryType.Field(index)
		if field.Name != expected.name || field.Type != expected.typeOf || !field.IsExported() {
			t.Fatalf("Entry field %d = %s %s", index, field.Name, field.Type)
		}
	}

	optionsType := reflect.TypeOf(Options{})
	expectedOptionFields := []struct {
		name   string
		typeOf reflect.Type
	}{
		{name: "Home", typeOf: reflect.TypeOf("")},
		{name: "Config", typeOf: reflect.TypeOf((*config.Config)(nil))},
		{name: "ConfigPath", typeOf: reflect.TypeOf("")},
		{name: "UserHome", typeOf: reflect.TypeOf("")},
	}
	if optionsType.NumField() != len(expectedOptionFields) {
		t.Fatalf("Options field count = %d", optionsType.NumField())
	}
	for index, expected := range expectedOptionFields {
		field := optionsType.Field(index)
		if field.Name != expected.name || field.Type != expected.typeOf || !field.IsExported() {
			t.Fatalf("Options field %d = %s %s", index, field.Name, field.Type)
		}
	}

	catalogType := reflect.TypeOf(Catalog{})
	for index := range catalogType.NumField() {
		field := catalogType.Field(index)
		if field.IsExported() {
			t.Fatalf("Catalog exposes retained field %s", field.Name)
		}
		if strings.Contains(field.Type.String(), "storecatalog") {
			t.Fatalf("Catalog retains physical provider type %s", field.Type)
		}
	}
}

func TestProjectedCatalogRejectsForgedPhysicalRecords(t *testing.T) {
	for _, test := range []struct {
		name  string
		specs []storecatalog.Spec
	}{
		{name: "empty"},
		{name: "invalid ID", specs: []storecatalog.Spec{{ID: "bad//id", Domain: "auth"}}},
		{name: "empty domain", specs: []storecatalog.Spec{{ID: "global/auth"}}},
		{name: "long domain", specs: []storecatalog.Spec{{
			ID: "global/auth", Domain: strings.Repeat("a", maxDomainBytes+1),
		}}},
		{name: "leading hyphen", specs: []storecatalog.Spec{{ID: "global/auth", Domain: "-auth"}}},
		{name: "trailing hyphen", specs: []storecatalog.Spec{{ID: "global/auth", Domain: "auth-"}}},
		{name: "invalid domain character", specs: []storecatalog.Spec{{ID: "global/auth", Domain: "auth_name"}}},
		{name: "duplicate ID", specs: []storecatalog.Spec{
			{ID: "global/auth", Domain: "auth"},
			{ID: "global/auth", Domain: "launcher-auth"},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog, err := newProjectedCatalog(test.specs)
			if catalog != nil || database.CodeOf(err) != database.CodeIntegrity ||
				strings.Contains(err.Error(), "bad//id") || strings.Contains(err.Error(), "auth_name") {
				t.Fatalf("newProjectedCatalog() = %#v, %v", catalog, err)
			}
		})
	}
}

func TestProjectedCatalogDropsPhysicalMetadataAndCopiesOrder(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret-store.db")
	specs := []storecatalog.Spec{
		{ID: "workspace/sessions", Domain: "sessions", Path: secret, LegacyRoots: []string{secret + ".json"}},
		{ID: "global/auth", Domain: "auth", Path: secret + "-other", Required: true},
	}
	catalog, err := newProjectedCatalog(specs)
	if err != nil {
		t.Fatal(err)
	}
	specs[0].ID = "forged/id"
	specs[0].Domain = "forged"
	specs[0].LegacyRoots[0] = "forged"

	entries := catalog.Entries()
	if !slices.Equal(entries, []Entry{
		{ID: "global/auth", Domain: "auth", Required: true},
		{ID: "workspace/sessions", Domain: "sessions"},
	}) {
		t.Fatalf("projected entries = %#v", entries)
	}
	if strings.Contains(fmt.Sprintf("%#v", catalog), secret) {
		t.Fatal("catalog retained physical metadata")
	}
}
