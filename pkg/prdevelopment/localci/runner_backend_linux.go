//go:build linux

package localci

func isProductionSandbox(sandbox Sandbox) bool {
	_, valid := sandbox.(*linuxSandbox)
	return valid
}
