//go:build !unix && !windows

package wecom

func lockWecomReqIDFile(_ string) (func(), error) { return func() {}, nil }
