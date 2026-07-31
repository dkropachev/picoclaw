package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeveloperModelAliasCatalogIsStableAndModelFree(t *testing.T) {
	catalog := DeveloperModelAliasCatalog()
	require.Equal(t, []string{"chat", "code", "investigate", "review", "fast"},
		[]string{catalog[0].Name, catalog[1].Name, catalog[2].Name, catalog[3].Name, catalog[4].Name})
	for _, entry := range catalog {
		require.NotEmpty(t, entry.Description)
		require.True(t, IsDeveloperModelAlias(entry.Name))
	}
	require.False(t, IsDeveloperModelAlias("gpt-5.4"))
	require.Empty(t, DefaultConfig().ModelAliases, "catalog roles must not imply model mappings")
}
