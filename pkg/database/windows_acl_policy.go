package database

import (
	"errors"
	"strings"
)

// windowsOwnerOnlySDDL returns a protected security descriptor that grants
// full control solely to the current-user SID. Directory ACEs propagate that
// same owner-only boundary to children created inside .database.
func windowsOwnerOnlySDDL(sid string, directory bool) (string, error) {
	if !validWindowsSIDString(sid) {
		return "", errors.New("Windows owner SID is invalid")
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	return "O:" + sid + "D:P(A;" + flags + ";GA;;;" + sid + ")", nil
}

func validWindowsSIDString(value string) bool {
	if len(value) < 5 || len(value) > 184 || !strings.HasPrefix(value, "S-") {
		return false
	}
	parts := strings.Split(value[2:], "-")
	if len(parts) < 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}
