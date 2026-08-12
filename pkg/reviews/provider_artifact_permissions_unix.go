//go:build !linux && !windows && !js && !plan9

package reviews

import (
	"fmt"
	"os"
)

func validatePortableProviderArtifactDirectory(
	rootPath string,
	parentRelative string,
	info os.FileInfo,
) error {
	if info.Mode().Perm()&0o022 == 0 {
		return nil
	}
	return fmt.Errorf(
		"provider artifact directory is not privately writable (root=%q parent=%q mode=%#o)",
		rootPath,
		parentRelative,
		info.Mode().Perm(),
	)
}
