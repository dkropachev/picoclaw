//go:build !unix

package workflows

import "os"

func openWorkflowInspectionDefinition(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY, 0)
}
