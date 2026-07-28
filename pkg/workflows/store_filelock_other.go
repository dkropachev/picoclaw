//go:build !unix

package workflows

func lockWorkflowRunStore(root string) (func(), error) {
	_ = root
	return func() {}, nil
}

func syncWorkflowRunDirectory(path string) error {
	_ = path
	return nil
}
