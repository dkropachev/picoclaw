//go:build !unix

package workflows

func syncWorkflowRunDirectory(path string) error {
	_ = path
	return nil
}
