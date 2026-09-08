// Package databaseadapter defines provider-neutral schema contracts for
// broker-owned stores. Registries are assembled explicitly by infrastructure;
// domain packages never register themselves through init-time side effects.
package databaseadapter

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	maximumContractNameBytes = 255
	maximumRequiredObjects   = 1024
	maximumRequiredTables    = 1024
	maximumRequiredColumns   = 4096
	maximumColumnsPerTable   = 1024
	maximumAdapters          = 1024
	maximumSchemaVersion     = 1<<31 - 1
)

// EmptyPolicy states whether a missing or physically empty generation may be
// initialized by the online owner or requires an explicitly fenced migration.
type EmptyPolicy string

const (
	EmptyInitializeOnline EmptyPolicy = "initialize_online"
	EmptyMigrateOffline   EmptyPolicy = "migrate_offline"
)

// Valid reports whether policy is part of the adapter contract.
func (policy EmptyPolicy) Valid() bool {
	switch policy {
	case EmptyInitializeOnline, EmptyMigrateOffline:
		return true
	default:
		return false
	}
}

// ColumnSet declares columns that must exist on one table at CurrentVersion.
type ColumnSet struct {
	Table   string
	Columns []string
}

// SchemaObject declares an exact SQLite catalog object kind and name required
// by one provider-neutral domain contract.
type SchemaObject struct {
	Type string
	Name string
}

// Contract is the complete provider-neutral readiness target for one domain.
// An empty RequiredObjects, RequiredColumns, or ImportHorizon means that no
// corresponding readiness assertion is required.
type Contract struct {
	CurrentVersion  int
	EmptyPolicy     EmptyPolicy
	RequiredObjects []SchemaObject
	RequiredColumns []ColumnSet
	ImportHorizon   string
}

// Target is the detached, trusted physical input supplied to an offline domain
// migration. It is never retained by Registry.
type Target struct {
	ID             database.StoreID
	GenerationPath string
	LegacyRoots    []string
}

// MigrateFunc applies one domain's offline migration to a trusted target.
// Callers establish migration fencing and backups before invoking it.
type MigrateFunc func(context.Context, Target) error

// Adapter binds one domain name to its readiness contract and optional offline
// migration callback.
type Adapter struct {
	Domain   string
	Contract Contract
	Migrate  MigrateFunc
}

// Registry is an immutable, explicitly assembled adapter catalog.
type Registry struct {
	domains  []string
	adapters map[string]Adapter
}

// NewRegistry validates adapters and returns an immutable registry. Domain
// order in the returned registry is deterministic and independent of input.
func NewRegistry(adapters ...Adapter) (*Registry, error) {
	if len(adapters) > maximumAdapters {
		return nil, database.NewError(database.CodeInvalid, "database adapter count limit is exceeded")
	}
	result := &Registry{
		domains:  make([]string, 0, len(adapters)),
		adapters: make(map[string]Adapter, len(adapters)),
	}
	for _, adapter := range adapters {
		if !validDomain(adapter.Domain) {
			return nil, database.NewError(database.CodeInvalid, "database adapter domain is invalid")
		}
		if _, duplicate := result.adapters[adapter.Domain]; duplicate {
			return nil, database.NewError(database.CodeAlreadyExists, "database adapter domain is duplicated")
		}
		contract, err := cloneAndValidateContract(adapter.Contract)
		if err != nil {
			return nil, err
		}
		result.domains = append(result.domains, adapter.Domain)
		result.adapters[adapter.Domain] = Adapter{
			Domain: adapter.Domain, Contract: contract, Migrate: adapter.Migrate,
		}
	}
	sort.Strings(result.domains)
	return result, nil
}

// Lookup returns a detached adapter contract for domain.
func (registry *Registry) Lookup(domain string) (Adapter, bool) {
	if registry == nil {
		return Adapter{}, false
	}
	adapter, ok := registry.adapters[domain]
	if !ok {
		return Adapter{}, false
	}
	adapter.Contract = cloneContract(adapter.Contract)
	return adapter, true
}

// Domains returns a detached, sorted list of registered domains.
func (registry *Registry) Domains() []string {
	if registry == nil {
		return nil
	}
	return append([]string(nil), registry.domains...)
}

func cloneAndValidateContract(contract Contract) (Contract, error) {
	if contract.CurrentVersion <= 0 || contract.CurrentVersion > maximumSchemaVersion {
		return Contract{}, database.NewError(database.CodeInvalid, "database adapter schema version is invalid")
	}
	if !contract.EmptyPolicy.Valid() {
		return Contract{}, database.NewError(database.CodeInvalid, "database adapter empty-store policy is invalid")
	}
	if !validOptionalContractName(contract.ImportHorizon) {
		return Contract{}, database.NewError(database.CodeInvalid, "database adapter import horizon is invalid")
	}
	if len(contract.RequiredObjects) > maximumRequiredObjects ||
		len(contract.RequiredColumns) > maximumRequiredTables {
		return Contract{}, database.NewError(database.CodeInvalid, "database adapter contract limit is exceeded")
	}

	result := cloneContract(contract)
	seenObjects := make(map[string]struct{}, len(result.RequiredObjects))
	for _, object := range result.RequiredObjects {
		if !validSchemaObjectType(object.Type) || !validRequiredContractName(object.Name) {
			return Contract{}, database.NewError(database.CodeInvalid, "database adapter required object is invalid")
		}
		key := object.Type + "\x00" + object.Name
		if _, duplicate := seenObjects[key]; duplicate {
			return Contract{}, database.NewError(database.CodeInvalid, "database adapter required object is duplicated")
		}
		seenObjects[key] = struct{}{}
	}

	seenTables := make(map[string]struct{}, len(result.RequiredColumns))
	totalColumns := 0
	for _, required := range result.RequiredColumns {
		if !validRequiredContractName(required.Table) || len(required.Columns) == 0 ||
			len(required.Columns) > maximumColumnsPerTable ||
			len(required.Columns) > maximumRequiredColumns-totalColumns {
			return Contract{}, database.NewError(database.CodeInvalid, "database adapter required columns are invalid")
		}
		totalColumns += len(required.Columns)
		if _, duplicate := seenTables[required.Table]; duplicate {
			return Contract{}, database.NewError(database.CodeInvalid, "database adapter required table is duplicated")
		}
		seenTables[required.Table] = struct{}{}
		seenColumns := make(map[string]struct{}, len(required.Columns))
		for _, column := range required.Columns {
			if !validRequiredContractName(column) {
				return Contract{}, database.NewError(database.CodeInvalid, "database adapter required column is invalid")
			}
			if _, duplicate := seenColumns[column]; duplicate {
				return Contract{}, database.NewError(database.CodeInvalid, "database adapter required column is duplicated")
			}
			seenColumns[column] = struct{}{}
		}
	}
	if result.ImportHorizon != "" {
		found := false
		for _, object := range result.RequiredObjects {
			if object.Type == "table" && object.Name == "storage_import_horizons" {
				found = true
				break
			}
		}
		if !found {
			return Contract{}, database.NewError(
				database.CodeInvalid,
				"database adapter import horizon table is not required",
			)
		}
	}
	return result, nil
}

func cloneContract(contract Contract) Contract {
	contract.RequiredObjects = append([]SchemaObject(nil), contract.RequiredObjects...)
	contract.RequiredColumns = append([]ColumnSet(nil), contract.RequiredColumns...)
	for index := range contract.RequiredColumns {
		contract.RequiredColumns[index].Columns = append(
			[]string(nil), contract.RequiredColumns[index].Columns...,
		)
	}
	return contract
}

func validSchemaObjectType(value string) bool {
	switch value {
	case "table", "index", "trigger", "view":
		return true
	default:
		return false
	}
}

func validDomain(domain string) bool {
	if domain == "" || len(domain) > 64 || domain != strings.TrimSpace(domain) || !utf8.ValidString(domain) {
		return false
	}
	for index := 0; index < len(domain); index++ {
		character := domain[index]
		alphaNumeric := character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if alphaNumeric {
			continue
		}
		if character != '-' || index == 0 || index == len(domain)-1 {
			return false
		}
	}
	return true
}

func validOptionalContractName(value string) bool {
	return value == "" || validRequiredContractName(value)
}

func validRequiredContractName(value string) bool {
	return value != "" && len(value) <= maximumContractNameBytes &&
		value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		!strings.ContainsRune(value, 0)
}
