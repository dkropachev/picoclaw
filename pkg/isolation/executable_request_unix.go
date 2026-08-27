//go:build !windows

package isolation

func normalizeExecutableRequest(name, _ string) (string, error) {
	return name, nil
}
