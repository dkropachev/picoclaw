package databasemigration

import "strings"

func validWindowsBackupPathString(path string, absolute bool) bool {
	lower := strings.ToLower(path)
	if strings.HasPrefix(lower, `\\?\`) || strings.HasPrefix(lower, `\\.\`) ||
		strings.HasPrefix(lower, `\??\`) {
		return false
	}
	drive := len(path) >= 2 && ((path[0] >= 'A' && path[0] <= 'Z') ||
		(path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':'
	unc := strings.HasPrefix(path, `\\`)
	if !absolute {
		return !drive && !unc
	}
	if drive {
		return len(path) >= 3 && (path[2] == '\\' || path[2] == '/')
	}
	if !unc {
		return false
	}
	parts := strings.FieldsFunc(strings.TrimPrefix(path, `\\`), func(character rune) bool {
		return character == '\\' || character == '/'
	})
	return len(parts) >= 2 && validWindowsBackupComponent(parts[0]) &&
		validWindowsBackupComponent(parts[1])
}

func validWindowsBackupPathComponents(path string, absolute bool) bool {
	if !validWindowsBackupPathString(path, absolute) {
		return false
	}
	remainder := path
	if len(path) >= 2 && path[1] == ':' {
		remainder = path[2:]
	} else if strings.HasPrefix(path, `\\`) {
		remainder = strings.TrimPrefix(path, `\\`)
	}
	for _, component := range strings.FieldsFunc(remainder, func(character rune) bool {
		return character == '\\' || character == '/'
	}) {
		if !validWindowsBackupComponent(component) {
			return false
		}
	}
	return true
}

func validWindowsBackupComponent(component string) bool {
	if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") ||
		strings.ContainsAny(component, `<>:"/\|?*`) || windowsShortNameComponent(component) {
		return false
	}
	for _, character := range component {
		if character < 32 {
			return false
		}
	}
	stem := component
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	stem = strings.ToUpper(stem)
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" ||
		stem == "CLOCK$" || stem == "CONIN$" || stem == "CONOUT$" {
		return false
	}
	if strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT") {
		suffix := strings.TrimPrefix(strings.TrimPrefix(stem, "COM"), "LPT")
		if suffix == "¹" || suffix == "²" || suffix == "³" ||
			len(suffix) == 1 && suffix[0] >= '1' && suffix[0] <= '9' {
			return false
		}
	}
	return true
}

func windowsShortNameComponent(component string) bool {
	for index := 0; index < len(component); index++ {
		if component[index] != '~' || index+1 == len(component) ||
			component[index+1] < '0' || component[index+1] > '9' {
			continue
		}
		end := index + 1
		for end < len(component) && component[end] >= '0' && component[end] <= '9' {
			end++
		}
		if end == len(component) || component[end] == '.' {
			return true
		}
	}
	return false
}
