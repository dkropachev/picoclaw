package config

// ModelAliasCatalogEntry describes a stable task role exposed by every
// management surface. Catalog entries do not select or imply a concrete model;
// a role becomes runnable only after the user creates a matching model_aliases
// mapping.
type ModelAliasCatalogEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

var developerModelAliasCatalog = []ModelAliasCatalogEntry{
	{
		Name:        "chat",
		Description: "General discussion, planning, and technical writing.",
	},
	{
		Name:        "code",
		Description: "Implementation, refactoring, debugging, and tests.",
	},
	{
		Name:        "investigate",
		Description: "Deep research, root-cause analysis, and unfamiliar code.",
	},
	{
		Name:        "review",
		Description: "Correctness, maintainability, and security review.",
	},
	{
		Name:        "fast",
		Description: "Low-latency summaries, classification, and routine automation.",
	},
}

// DeveloperModelAliasCatalog returns a copy of the predefined developer roles.
// The catalog is metadata, not model configuration.
func DeveloperModelAliasCatalog() []ModelAliasCatalogEntry {
	return append([]ModelAliasCatalogEntry(nil), developerModelAliasCatalog...)
}

// IsDeveloperModelAlias reports whether name is one of the predefined roles.
func IsDeveloperModelAlias(name string) bool {
	for i := range developerModelAliasCatalog {
		if developerModelAliasCatalog[i].Name == name {
			return true
		}
	}
	return false
}
