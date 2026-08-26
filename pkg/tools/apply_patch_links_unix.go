//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package tools

import (
	"fmt"
	"os"
	"reflect"
)

func applyPatchLinkCount(_ *os.File, info os.FileInfo) (uint64, error) {
	if info == nil || info.Sys() == nil {
		return 0, fmt.Errorf("file link metadata is unavailable")
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	field := value.FieldByName("Nlink")
	if !field.IsValid() {
		return 0, fmt.Errorf("file link metadata is unavailable")
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return field.Uint(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		count := field.Int()
		if count < 0 {
			return 0, fmt.Errorf("file link metadata is invalid")
		}
		return uint64(count), nil
	default:
		return 0, fmt.Errorf("file link metadata is unavailable")
	}
}
