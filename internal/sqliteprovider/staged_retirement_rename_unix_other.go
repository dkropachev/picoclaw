//go:build unix && !aix && !linux && !android && !darwin

package sqliteprovider

import "errors"

func renameStagedRetirementNoReplace(int, string, int, string) error {
	return errors.New("atomic no-replace SQLite staged retirement is unsupported")
}
