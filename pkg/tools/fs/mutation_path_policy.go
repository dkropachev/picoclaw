//go:build !windows

package fstools

func validateFileMutationPlatformPath(string) error { return nil }
