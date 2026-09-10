//go:build windows

package database

import "strings"

func sameSupervisorEnvironmentName(first, second string) bool {
	return strings.EqualFold(first, second)
}
