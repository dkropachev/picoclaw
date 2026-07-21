//go:build !unix

package workflows

func lockWorkflowRunStore(root string) (func(), error) {
	_ = root
	return func() {}, nil
}
