//go:build !unix && !windows

package state

func lockRuntimeStateFile(_ string) (func(), error) {
	return func() {}, nil
}
