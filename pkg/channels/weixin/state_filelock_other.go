//go:build !unix && !windows

package weixin

func lockWeixinStateFile(_ string) (func(), error) { return func() {}, nil }
