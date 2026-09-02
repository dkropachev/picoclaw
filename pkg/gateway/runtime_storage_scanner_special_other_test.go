//go:build integration && !unix

package gateway

func runtimeStorageCreateSpecialCanary(string) (bool, error) { return false, nil }
