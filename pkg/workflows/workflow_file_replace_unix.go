//go:build unix

package workflows

import "os"

func replaceWorkflowFile(source, target string) error {
	return os.Rename(source, target)
}
