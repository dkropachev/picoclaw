//go:build !unix && !windows

package database

func syncDirectory(string) error { return nil }
