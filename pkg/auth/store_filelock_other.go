//go:build !unix && !windows

package auth

func lockAuthStore(_ string) (func(), error) {
	return func() {}, nil
}
