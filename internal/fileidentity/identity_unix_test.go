//go:build unix && !aix

package fileidentity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type identityTestFileInfo struct {
	name string
	mode os.FileMode
	sys  any
}

func (info identityTestFileInfo) Name() string       { return info.name }
func (info identityTestFileInfo) Size() int64        { return 0 }
func (info identityTestFileInfo) Mode() os.FileMode  { return info.mode }
func (info identityTestFileInfo) ModTime() time.Time { return time.Time{} }
func (info identityTestFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info identityTestFileInfo) Sys() any           { return info.sys }

func TestExistingRejectsNamedPipe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}
	if identity, exists, err := Existing(path); identity.Valid() || exists || !errors.Is(err, ErrUnsafeType) {
		t.Fatalf("Existing(pipe) = %#v, %t, %v", identity, exists, err)
	}
}

func TestExistingFailsClosedAcrossInspectionTransitions(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first")
	secondPath := filepath.Join(root, "second")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte(path), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := os.Lstat(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.Lstat(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("lstat canary")

	tests := []struct {
		name  string
		lstat func(string) (os.FileInfo, error)
		want  error
	}{
		{
			name: "initial lookup error",
			lstat: func(string) (os.FileInfo, error) {
				return nil, canary
			},
			want: canary,
		},
		{
			name: "reinspection error",
			lstat: func() func(string) (os.FileInfo, error) {
				calls := 0
				return func(string) (os.FileInfo, error) {
					calls++
					if calls == 1 {
						return first, nil
					}
					return nil, canary
				}
			}(),
			want: canary,
		},
		{
			name: "object identity changes",
			lstat: func() func(string) (os.FileInfo, error) {
				calls := 0
				return func(string) (os.FileInfo, error) {
					calls++
					if calls == 1 {
						return first, nil
					}
					return second, nil
				}
			}(),
			want: ErrUnsafeType,
		},
		{
			name: "object type changes",
			lstat: func() func(string) (os.FileInfo, error) {
				calls := 0
				return func(string) (os.FileInfo, error) {
					calls++
					if calls == 1 {
						return first, nil
					}
					return identityTestFileInfo{mode: os.ModeSymlink}, nil
				}
			}(),
			want: ErrUnsafeType,
		},
		{
			name: "platform identity unavailable",
			lstat: func() func(string) (os.FileInfo, error) {
				calls := 0
				return func(string) (os.FileInfo, error) {
					calls++
					if calls == 1 {
						return first, nil
					}
					return identityTestFileInfo{name: "first", mode: 0o600}, nil
				}
			}(),
			want: ErrUnsupported,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identity, exists, err := existingWithLstat(firstPath, test.lstat)
			if identity.Valid() || exists || !errors.Is(err, test.want) {
				t.Fatalf("existingWithLstat() = %#v, %t, %v; want %v", identity, exists, err, test.want)
			}
		})
	}
}
