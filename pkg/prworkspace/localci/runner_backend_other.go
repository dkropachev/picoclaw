//go:build !linux

package localci

func isProductionSandbox(Sandbox) bool {
	return false
}
