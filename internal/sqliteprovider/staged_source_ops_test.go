//go:build unix

package sqliteprovider

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestImmutableStageWriteOperationFailures(t *testing.T) {
	canary := errors.New("stage write operation failed")
	tests := []struct {
		name   string
		mutate func(*immutableStageWriteOps, *os.File)
	}{
		{
			name: "initial stat",
			mutate: func(ops *immutableStageWriteOps, _ *os.File) {
				ops.stat = func(*os.File) (os.FileInfo, error) { return nil, canary }
			},
		},
		{
			name: "chmod",
			mutate: func(ops *immutableStageWriteOps, _ *os.File) {
				ops.chmod = func(*os.File, os.FileMode) error { return canary }
			},
		},
		{
			name: "copied size invariant",
			mutate: func(ops *immutableStageWriteOps, _ *os.File) {
				ops.copy = func(
					context.Context, context.Context, io.Writer, io.Reader, int64,
				) ([32]byte, int64, error) {
					return [32]byte{}, 0, nil
				}
			},
		},
		{
			name: "sync",
			mutate: func(ops *immutableStageWriteOps, _ *os.File) {
				ops.sync = func(*os.File) error { return canary }
			},
		},
		{
			name: "destination rewind",
			mutate: func(ops *immutableStageWriteOps, source *os.File) {
				ops.seek = func(file *os.File, offset int64, whence int) (int64, error) {
					if file != source {
						return 0, canary
					}
					return file.Seek(offset, whence)
				}
			},
		},
		{
			name: "destination hash",
			mutate: func(ops *immutableStageWriteOps, _ *os.File) {
				ops.hash = func(
					context.Context, context.Context, *os.File, int64,
				) ([32]byte, int64, error) {
					return [32]byte{}, 0, canary
				}
			},
		},
		{
			name: "final identity stat",
			mutate: func(ops *immutableStageWriteOps, _ *os.File) {
				calls := 0
				ops.stat = func(file *os.File) (os.FileInfo, error) {
					calls++
					if calls == 3 {
						return nil, canary
					}
					return file.Stat()
				}
			},
		},
		{
			name: "final metadata validation",
			mutate: func(ops *immutableStageWriteOps, _ *os.File) {
				ops.validate = func(string, os.FileInfo) error { return canary }
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rootPath := t.TempDir()
			sourcePath := filepath.Join(rootPath, "source.db")
			writeImmutableSourceTestFile(t, sourcePath, []byte("source bytes"), time.Now())
			sourceFile, err := os.Open(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			defer sourceFile.Close()
			sourceInfo, err := sourceFile.Stat()
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			stagePath := filepath.Join(rootPath, "stage.db")
			ops := defaultImmutableStageWriteOps()
			test.mutate(&ops, sourceFile)
			_, writeErr := writeImmutableStageMemberWithOps(
				t.Context(), t.Context(), root, filepath.Base(stagePath), stagePath,
				&immutableGenerationOpenMember{path: sourcePath, info: sourceInfo, file: sourceFile},
				ops,
			)
			if writeErr == nil {
				t.Fatal("injected stage write failure was accepted")
			}
			if test.name != "copied size invariant" && !errors.Is(writeErr, canary) {
				t.Fatalf("stage write error = %v", writeErr)
			}
		})
	}
}

func TestImmutableGenerationMemberFilesystemClassifications(t *testing.T) {
	valid := func(os.FileInfo) error { return nil }
	tests := []struct {
		name      string
		linkCount generationLinkClass
		owner     generationOwnerClass
	}{
		{name: "transitioning link", linkCount: generationLinkZero, owner: generationOwnerCurrent},
		{name: "multiple links", linkCount: generationLinkMultiple, owner: generationOwnerCurrent},
		{name: "unsafe links", linkCount: generationLinkUnsafe, owner: generationOwnerCurrent},
		{name: "unavailable links", linkCount: generationLinkUnavailable, owner: generationOwnerCurrent},
		{name: "foreign owner", linkCount: generationLinkSingle, owner: generationOwnerForeign},
		{name: "unavailable owner", linkCount: generationLinkSingle, owner: generationOwnerUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filesystem := providerFilesystem{
				validateLiveInfo: valid,
				linkCount: func(string, os.FileInfo) generationLinkClass {
					return test.linkCount
				},
				owner: func(string, os.FileInfo) generationOwnerClass { return test.owner },
			}
			if err := validateImmutableGenerationMemberWithFilesystem(
				"source.db", nil, filesystem,
			); err == nil {
				t.Fatal("unsafe immutable member classification was accepted")
			}
		})
	}
}
