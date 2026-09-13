//nolint:golines // Table fixtures keep each invalid contract beside its expected code.
package databaseadapter

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

type registryValidationGeneration struct{}

func (registryValidationGeneration) StoreID() database.StoreID { return "workspace/workflows" }
func (registryValidationGeneration) Domain() string            { return "workflows" }
func (registryValidationGeneration) ReadScalar(string, ...string) (ValidationScalar, error) {
	return ValidationScalar{Kind: ValidationInt64, Int64: 1}, nil
}

func TestRegistryIsExplicitSortedAndImmutable(t *testing.T) {
	objects := []SchemaObject{
		{Type: "table", Name: "storage_imports"},
		{Type: "table", Name: "storage_import_horizons"},
	}
	columns := []string{"component", "closed_at"}
	called := false
	validated := false
	migrate := func(context.Context, Target) error {
		called = true
		return nil
	}
	validate := func(_ context.Context, generation ExactReadOnlyGeneration) error {
		validated = generation.StoreID() == "workspace/workflows" &&
			generation.Domain() == "workflows"
		return nil
	}
	registry, err := NewRegistry(
		Adapter{
			Domain: "workflows",
			Contract: Contract{
				CurrentVersion:  3,
				EmptyPolicy:     EmptyInitializeOnline,
				RequiredObjects: objects,
				RequiredColumns: []ColumnSet{{Table: "storage_import_horizons", Columns: columns}},
				ImportHorizon:   "workflows",
			},
			Migrate: migrate, Validate: validate,
		},
		Adapter{
			Domain:   "auth",
			Contract: Contract{CurrentVersion: 1, EmptyPolicy: EmptyMigrateOffline},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	objects[0].Name = "changed"
	columns[0] = "changed"

	domains := registry.Domains()
	if len(domains) != 2 || domains[0] != "auth" || domains[1] != "workflows" {
		t.Fatalf("Domains() = %#v", domains)
	}
	domains[0] = "changed"
	if fresh := registry.Domains(); fresh[0] != "auth" {
		t.Fatalf("Domains() exposed registry storage: %#v", fresh)
	}

	adapter, ok := registry.Lookup("workflows")
	if !ok || adapter.Domain != "workflows" || adapter.Contract.CurrentVersion != 3 ||
		adapter.Migrate == nil || adapter.Validate == nil {
		t.Fatalf("Lookup(workflows) = %#v, %t", adapter, ok)
	}
	if adapter.Contract.RequiredObjects[0].Name != "storage_imports" ||
		adapter.Contract.RequiredColumns[0].Columns[0] != "component" {
		t.Fatalf("registry retained mutable caller data: %#v", adapter.Contract)
	}
	adapter.Contract.RequiredObjects[0].Name = "mutated"
	adapter.Contract.RequiredColumns[0].Columns[0] = "mutated"
	fresh, _ := registry.Lookup("workflows")
	if fresh.Contract.RequiredObjects[0].Name != "storage_imports" ||
		fresh.Contract.RequiredColumns[0].Columns[0] != "component" {
		t.Fatalf("Lookup exposed registry storage: %#v", fresh.Contract)
	}
	if err := fresh.Migrate(t.Context(), Target{ID: database.StoreID("workspace/workflows")}); err != nil || !called {
		t.Fatalf("migration callback = %v, called %t", err, called)
	}
	if err := fresh.Validate(t.Context(), registryValidationGeneration{}); err != nil || !validated {
		t.Fatalf("validation callback = %v, validated %t", err, validated)
	}
	if missing, found := registry.Lookup("missing"); found || missing.Domain != "" ||
		missing.Migrate != nil || missing.Validate != nil {
		t.Fatalf("Lookup(missing) = %#v, %t", missing, found)
	}
}

func TestRegistryRejectsInvalidAndDuplicateContracts(t *testing.T) {
	valid := Adapter{
		Domain:   "valid-domain",
		Contract: Contract{CurrentVersion: 1, EmptyPolicy: EmptyInitializeOnline},
	}
	aggregateColumns := make([]ColumnSet, 5)
	for tableIndex := range aggregateColumns {
		columnCount := maximumColumnsPerTable
		if tableIndex == len(aggregateColumns)-1 {
			columnCount = 1
		}
		aggregateColumns[tableIndex].Table = fmt.Sprintf("table_%d", tableIndex)
		aggregateColumns[tableIndex].Columns = make([]string, columnCount)
		for columnIndex := range aggregateColumns[tableIndex].Columns {
			aggregateColumns[tableIndex].Columns[columnIndex] = fmt.Sprintf("column_%d", columnIndex)
		}
	}
	tests := []struct {
		name     string
		adapters []Adapter
		code     database.ErrorCode
	}{
		{name: "empty domain", adapters: []Adapter{{Contract: valid.Contract}}, code: database.CodeInvalid},
		{name: "uppercase domain", adapters: []Adapter{{Domain: "Bad", Contract: valid.Contract}}, code: database.CodeInvalid},
		{name: "zero version", adapters: []Adapter{{Domain: "bad", Contract: Contract{EmptyPolicy: EmptyInitializeOnline}}}, code: database.CodeInvalid},
		{name: "unknown policy", adapters: []Adapter{{Domain: "bad", Contract: Contract{CurrentVersion: 1, EmptyPolicy: "bad"}}}, code: database.CodeInvalid},
		{name: "invalid horizon", adapters: []Adapter{{Domain: "bad", Contract: Contract{CurrentVersion: 1, EmptyPolicy: EmptyInitializeOnline, ImportHorizon: " bad "}}}, code: database.CodeInvalid},
		{name: "horizon table absent", adapters: []Adapter{{Domain: "bad", Contract: Contract{CurrentVersion: 1, EmptyPolicy: EmptyInitializeOnline, ImportHorizon: "items"}}}, code: database.CodeInvalid},
		{name: "empty object", adapters: []Adapter{{Domain: "bad", Contract: Contract{CurrentVersion: 1, EmptyPolicy: EmptyInitializeOnline, RequiredObjects: []SchemaObject{{Type: "table"}}}}}, code: database.CodeInvalid},
		{name: "invalid object type", adapters: []Adapter{{Domain: "bad", Contract: Contract{CurrentVersion: 1, EmptyPolicy: EmptyInitializeOnline, RequiredObjects: []SchemaObject{{Type: "sequence", Name: "items"}}}}}, code: database.CodeInvalid},
		{name: "duplicate object", adapters: []Adapter{{Domain: "bad", Contract: Contract{CurrentVersion: 1, EmptyPolicy: EmptyInitializeOnline, RequiredObjects: []SchemaObject{{Type: "table", Name: "items"}, {Type: "table", Name: "items"}}}}}, code: database.CodeInvalid},
		{name: "empty column set", adapters: []Adapter{{Domain: "bad", Contract: Contract{CurrentVersion: 1, EmptyPolicy: EmptyInitializeOnline, RequiredColumns: []ColumnSet{{Table: "items"}}}}}, code: database.CodeInvalid},
		{
			name: "invalid column",
			adapters: []Adapter{{
				Domain: "bad",
				Contract: Contract{
					CurrentVersion: 1,
					EmptyPolicy:    EmptyInitializeOnline,
					RequiredColumns: []ColumnSet{{
						Table: "items", Columns: []string{" bad "},
					}},
				},
			}},
			code: database.CodeInvalid,
		},
		{
			name: "duplicate table",
			adapters: []Adapter{{
				Domain: "bad",
				Contract: Contract{
					CurrentVersion: 1,
					EmptyPolicy:    EmptyInitializeOnline,
					RequiredColumns: []ColumnSet{
						{Table: "items", Columns: []string{"id"}},
						{Table: "items", Columns: []string{"name"}},
					},
				},
			}},
			code: database.CodeInvalid,
		},
		{
			name: "duplicate column",
			adapters: []Adapter{{
				Domain: "bad",
				Contract: Contract{
					CurrentVersion: 1,
					EmptyPolicy:    EmptyInitializeOnline,
					RequiredColumns: []ColumnSet{{
						Table: "items", Columns: []string{"id", "id"},
					}},
				},
			}},
			code: database.CodeInvalid,
		},
		{name: "duplicate domain", adapters: []Adapter{valid, valid}, code: database.CodeAlreadyExists},
		{name: "too many objects", adapters: []Adapter{{Domain: "bad", Contract: Contract{
			CurrentVersion: 1, EmptyPolicy: EmptyInitializeOnline,
			RequiredObjects: make([]SchemaObject, maximumRequiredObjects+1),
		}}}, code: database.CodeInvalid},
		{name: "too many tables", adapters: []Adapter{{Domain: "bad", Contract: Contract{
			CurrentVersion: 1, EmptyPolicy: EmptyInitializeOnline,
			RequiredColumns: make([]ColumnSet, maximumRequiredTables+1),
		}}}, code: database.CodeInvalid},
		{name: "too many columns", adapters: []Adapter{{Domain: "bad", Contract: Contract{
			CurrentVersion: 1, EmptyPolicy: EmptyInitializeOnline,
			RequiredColumns: []ColumnSet{{
				Table: "items", Columns: make([]string, maximumColumnsPerTable+1),
			}},
		}}}, code: database.CodeInvalid},
		{name: "too many aggregate columns", adapters: []Adapter{{Domain: "bad", Contract: Contract{
			CurrentVersion: 1, EmptyPolicy: EmptyInitializeOnline, RequiredColumns: aggregateColumns,
		}}}, code: database.CodeInvalid},
		{name: "oversized name", adapters: []Adapter{{Domain: "bad", Contract: Contract{
			CurrentVersion: 1, EmptyPolicy: EmptyInitializeOnline,
			RequiredObjects: []SchemaObject{{Type: "table", Name: strings.Repeat("x", maximumContractNameBytes+1)}},
		}}}, code: database.CodeInvalid},
	}
	if strconv.IntSize > 32 {
		oversizedVersion := int64(maximumSchemaVersion) + 1
		tests = append(tests, struct {
			name     string
			adapters []Adapter
			code     database.ErrorCode
		}{
			name: "oversized version",
			adapters: []Adapter{{Domain: "bad", Contract: Contract{
				CurrentVersion: int(oversizedVersion),
				EmptyPolicy:    EmptyInitializeOnline,
			}}},
			code: database.CodeInvalid,
		})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, err := NewRegistry(test.adapters...)
			if registry != nil || database.CodeOf(err) != test.code {
				t.Fatalf("NewRegistry() = %#v, %v; want nil, %s", registry, err, test.code)
			}
		})
	}
	if registry, err := NewRegistry(make([]Adapter, maximumAdapters+1)...); registry != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("oversized NewRegistry() = %#v, %v", registry, err)
	}
	registry, err := NewRegistry(Adapter{
		Domain: "typed-objects",
		Contract: Contract{
			CurrentVersion: 1,
			EmptyPolicy:    EmptyInitializeOnline,
			RequiredObjects: []SchemaObject{
				{Type: "table", Name: "shared"},
				{Type: "trigger", Name: "shared"},
			},
		},
	})
	if err != nil || registry == nil {
		t.Fatalf("typed same-name objects = %#v, %v", registry, err)
	}
}

func TestNilAndEmptyRegistry(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if domains := registry.Domains(); len(domains) != 0 {
		t.Fatalf("empty Domains() = %#v", domains)
	}
	var nilRegistry *Registry
	if domains := nilRegistry.Domains(); domains != nil {
		t.Fatalf("nil Domains() = %#v", domains)
	}
	if adapter, ok := nilRegistry.Lookup("auth"); ok || adapter.Domain != "" ||
		adapter.Migrate != nil || adapter.Validate != nil {
		t.Fatalf("nil Lookup() = %#v, %t", adapter, ok)
	}
}
