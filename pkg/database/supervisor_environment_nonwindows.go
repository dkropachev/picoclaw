//go:build !windows

package database

func sameSupervisorEnvironmentName(first, second string) bool {
	return first == second
}
