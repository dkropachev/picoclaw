package sqliteprovider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStagedSourceCloseoutInputAndSourceFailures(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	stage := filepath.Join(root, "stage.db")
	valid := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, filepath.Join(root, "missing.db"))
	})
	invalidOps := defaultImmutableGenerationCopyOps()
	invalidOps.remove = nil
	if _, err := copyImmutableGenerationToStageWithOps(t.Context(), valid, stage, invalidOps); err == nil {
		t.Fatal("copy accepted unavailable cleanup operation")
	}

	unsafeRoot := filepath.Join(root, "unsafe")
	if err := os.WriteFile(unsafeRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := copyImmutableGenerationToStage(
		t.Context(), valid, filepath.Join(unsafeRoot, "stage.db"),
	); err == nil {
		t.Fatal("copy accepted an unsafe stage ancestor")
	}
	afterUseContext, cancelAfterUse := context.WithCancelCause(t.Context())
	afterUseCanary := errors.New("cancel after source use")
	afterUse := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		err := use(ctx, filepath.Join(root, "missing-after-use.db"))
		cancelAfterUse(afterUseCanary)
		return err
	})
	if _, err := copyImmutableGenerationToStage(
		afterUseContext, afterUse, filepath.Join(root, "after-use-stage.db"),
	); !errors.Is(err, afterUseCanary) {
		t.Fatalf("post-use cancellation error = %v", err)
	}

	if err := invokeImmutableGenerationSource(
		t.Context(), ImmutableGenerationSource{}, func(context.Context, string) error { return nil },
	); !errors.Is(err, errImmutableGenerationSourceContract) {
		t.Fatalf("invalid source error = %v", err)
	}
	panicking := immutableGenerationSourceForTest(func(
		context.Context,
		func(context.Context, string) error,
	) error {
		panic("source canary")
	})
	if err := invokeImmutableGenerationSource(
		t.Context(), panicking, func(context.Context, string) error { return nil },
	); !errors.Is(err, errImmutableGenerationSourceContract) {
		t.Fatalf("source panic error = %v", err)
	}

	operationStarted := make(chan struct{})
	releaseOperation := make(chan struct{})
	sourceReturned := make(chan struct{})
	asynchronous := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		go func() { _ = use(ctx, "source") }()
		<-operationStarted
		close(sourceReturned)
		return nil
	})
	result := make(chan error, 1)
	go func() {
		result <- invokeImmutableGenerationSource(
			t.Context(), asynchronous, func(context.Context, string) error {
				close(operationStarted)
				<-releaseOperation
				return nil
			},
		)
	}()
	<-sourceReturned
	waitForStagedSourceStack(t, "sync.(*WaitGroup).Wait")
	close(releaseOperation)
	err := <-result
	if !errors.Is(err, errImmutableGenerationSourceContract) {
		t.Fatalf("asynchronous source error = %v", err)
	}
}

func TestStagedSourceCloseoutCopyGenerationBoundaries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	stage := filepath.Join(root, "stage.db")
	created := make([]immutableStageMember, 0, 4)
	ops := defaultImmutableGenerationCopyOps()
	if _, err := copyImmutableGeneration(
		t.Context(), nil, filepath.Join(root, "source.db"), stage, &created, ops,
	); err == nil {
		t.Fatal("copy accepted nil source context")
	}
	canceledSource, cancelSource := context.WithCancel(t.Context())
	cancelSource()
	if _, err := copyImmutableGeneration(
		t.Context(), canceledSource, filepath.Join(root, "source.db"), stage, &created, ops,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("copy source cancellation error = %v", err)
	}
	if _, err := copyImmutableGeneration(
		t.Context(), t.Context(), stage, stage, &created, ops,
	); err == nil {
		t.Fatal("copy accepted an aliased source and stage")
	}
	unsafeParent := filepath.Join(root, "unsafe-parent")
	if err := os.WriteFile(unsafeParent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := copyImmutableGeneration(
		t.Context(), t.Context(), filepath.Join(unsafeParent, "source.db"),
		stage, &created, ops,
	); err == nil {
		t.Fatal("copy accepted an unsafe source ancestor")
	}

	source := filepath.Join(root, "source.db")
	writeImmutableSourceTestFile(t, source, []byte("source"), time.Now())
	canary := errors.New("source-open hook canary")
	ops.afterSourceOpen = func(string) error { return canary }
	if _, err := copyImmutableGeneration(
		t.Context(), t.Context(), source, stage, &created, ops,
	); !errors.Is(err, canary) {
		t.Fatalf("source-open hook error = %v", err)
	}

	created = created[:0]
	ops = defaultImmutableGenerationCopyOps()
	ops.syncDirectory = func(string) error { return canary }
	if _, err := copyImmutableGeneration(
		t.Context(), t.Context(), source, stage, &created, ops,
	); !errors.Is(err, canary) {
		t.Fatalf("stage sync error = %v", err)
	}
	if err := cleanupImmutableStage(created, root, defaultImmutableGenerationCopyOps()); err != nil {
		t.Fatal(err)
	}
}

func TestStagedSourceCloseoutCopyGenerationFilesystemFailures(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "source")
	if err := os.Mkdir(sourceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDirectory, "source.db")
	writeImmutableSourceTestFile(t, source, []byte("source"), time.Now())
	if err := os.Chmod(sourceDirectory, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sourceDirectory, 0o700) })
	created := make([]immutableStageMember, 0, 4)
	if _, err := copyImmutableGeneration(
		t.Context(), t.Context(), source, filepath.Join(root, "stage.db"),
		&created, defaultImmutableGenerationCopyOps(),
	); err == nil {
		t.Fatal("copy opened an unreadable source root")
	}

	if err := os.Chmod(sourceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	destinationDirectory := filepath.Join(root, "destination")
	if err := os.Mkdir(destinationDirectory, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(destinationDirectory, 0o700) })
	if _, err := copyImmutableGeneration(
		t.Context(), t.Context(), source, filepath.Join(destinationDirectory, "stage.db"),
		&created, defaultImmutableGenerationCopyOps(),
	); err == nil {
		t.Fatal("copy opened an unreadable destination root")
	}

	parentCtx, cancelParent := context.WithCancel(t.Context())
	ops := defaultImmutableGenerationCopyOps()
	ops.afterSourceOpen = func(string) error {
		cancelParent()
		return nil
	}
	if _, err := copyImmutableGeneration(
		parentCtx, t.Context(), source, filepath.Join(root, "canceled-stage.db"), &created, ops,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-copy cancellation error = %v", err)
	}

	existingStage := filepath.Join(root, "existing-stage.db")
	writeImmutableSourceTestFile(t, existingStage, []byte("existing"), time.Now())
	if _, err := copyImmutableGeneration(
		t.Context(), t.Context(), source, existingStage, &created,
		defaultImmutableGenerationCopyOps(),
	); err == nil {
		t.Fatal("copy overwrote an existing destination")
	}
}

func TestStagedSourceCloseoutDetectsPostCopyDrift(t *testing.T) {
	t.Parallel()

	t.Run("source", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		source := filepath.Join(root, "source.db")
		stage := filepath.Join(root, "stage.db")
		timestamp := time.Unix(1_700_000_123, 0)
		writeImmutableSourceTestFile(t, source, []byte("original"), timestamp)
		writeImmutableSourceTestFile(t, source+"-wal", []byte("wal"), timestamp)
		ops := defaultImmutableGenerationCopyOps()
		ops.beforeDestinationOpen = func(index int, _ string) error {
			if index != 1 {
				return nil
			}
			if err := os.WriteFile(source, []byte("modified"), 0o600); err != nil {
				return err
			}
			return os.Chtimes(source, timestamp, timestamp)
		}
		created := make([]immutableStageMember, 0, 4)
		if _, err := copyImmutableGeneration(
			t.Context(), t.Context(), source, stage, &created, ops,
		); err == nil {
			t.Fatal("copy accepted source bytes changed after their copy")
		}
	})

	t.Run("destination", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		source := filepath.Join(root, "source.db")
		stage := filepath.Join(root, "stage.db")
		timestamp := time.Unix(1_700_000_124, 0)
		writeImmutableSourceTestFile(t, source, []byte("original"), timestamp)
		writeImmutableSourceTestFile(t, source+"-wal", []byte("wal"), timestamp)
		ops := defaultImmutableGenerationCopyOps()
		ops.beforeDestinationOpen = func(index int, _ string) error {
			if index != 1 {
				return nil
			}
			return os.WriteFile(stage, []byte("modified"), 0o600)
		}
		created := make([]immutableStageMember, 0, 4)
		if _, err := copyImmutableGeneration(
			t.Context(), t.Context(), source, stage, &created, ops,
		); err == nil {
			t.Fatal("copy accepted destination bytes changed before verification")
		}
	})
}

func TestStagedSourceCloseoutCaptureAndOpenFailures(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	nondirectory := filepath.Join(root, "not-directory")
	if err := os.WriteFile(nondirectory, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := captureImmutableGeneration(filepath.Join(nondirectory, "source.db")); err == nil {
		t.Fatal("capture accepted an invalid parent")
	}

	oversized := filepath.Join(root, "oversized.db")
	file, err := os.OpenFile(oversized, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if truncateErr := file.Truncate(maximumImmutableGenerationMemberBytes + 1); truncateErr != nil {
		_ = file.Close()
		t.Skipf("sparse files unavailable: %v", truncateErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if _, captureErr := captureImmutableGeneration(oversized); captureErr == nil {
		t.Fatal("capture accepted an oversized member")
	}

	orphan := filepath.Join(root, "orphan.db")
	writeImmutableSourceTestFile(t, orphan+"-wal", []byte("wal"), time.Now())
	if _, orphanErr := captureImmutableGeneration(orphan); orphanErr == nil {
		t.Fatal("capture accepted an orphan sidecar")
	}

	source := filepath.Join(root, "source.db")
	writeImmutableSourceTestFile(t, source, []byte("main"), time.Now())
	writeImmutableSourceTestFile(t, source+"-wal", []byte("wal"), time.Now())
	snapshot, err := captureImmutableGeneration(source)
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceRoot.Close()
	if removeErr := os.Remove(source + "-wal"); removeErr != nil {
		t.Fatal(removeErr)
	}
	if _, openErr := openImmutableGenerationMembers(
		t.Context(), t.Context(), sourceRoot, snapshot,
	); openErr == nil {
		t.Fatal("open accepted a removed snapshot member")
	}

	snapshot, err = captureImmutableGeneration(source)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, canceledErr := openImmutableGenerationMembers(
		canceled, t.Context(), sourceRoot, snapshot,
	); !errors.Is(canceledErr, context.Canceled) {
		t.Fatalf("open cancellation error = %v", canceledErr)
	}

	writeImmutableSourceTestFile(t, source, []byte("replacement"), time.Now())
	snapshot, err = captureImmutableGeneration(source)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(source, []byte("changed-now"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, changedErr := openImmutableGenerationMembers(
		t.Context(), t.Context(), sourceRoot, snapshot,
	); changedErr == nil {
		t.Fatal("open accepted metadata changed after capture")
	}

	unsafe := filepath.Join(root, "unsafe.db")
	if writeErr := os.WriteFile(unsafe, []byte("unsafe"), 0o640); writeErr != nil {
		t.Fatal(writeErr)
	}
	unsafeInfo, err := os.Lstat(unsafe)
	if err != nil {
		t.Fatal(err)
	}
	unsafeSnapshot := immutableGenerationSnapshot{path: unsafe}
	unsafeSnapshot.infos[0] = unsafeInfo
	if _, err := openImmutableGenerationMembers(
		t.Context(), t.Context(), sourceRoot, unsafeSnapshot,
	); err == nil {
		t.Fatal("open accepted unsafe captured member metadata")
	}
}

func TestStagedSourceCloseoutWriteMemberBoundaries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	destinationRoot, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer destinationRoot.Close()
	if _, writeErr := writeImmutableStageMember(
		t.Context(), t.Context(), destinationRoot, "unused.db",
		filepath.Join(root, "unused.db"), nil,
	); writeErr == nil {
		t.Fatal("write accepted nil source")
	}

	sourcePath := filepath.Join(root, "source.db")
	writeImmutableSourceTestFile(t, sourcePath, []byte("source"), time.Now())
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceFile.Close()
	sourceInfo, err := sourceFile.Stat()
	if err != nil {
		t.Fatal(err)
	}
	source := &immutableGenerationOpenMember{file: sourceFile, info: sourceInfo}
	existing := filepath.Join(root, "existing.db")
	writeImmutableSourceTestFile(t, existing, []byte("existing"), time.Now())
	if _, writeErr := writeImmutableStageMember(
		t.Context(), t.Context(), destinationRoot, filepath.Base(existing), existing, source,
	); writeErr == nil {
		t.Fatal("write overwrote existing stage member")
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()
	defer writeEnd.Close()
	pipeSource := &immutableGenerationOpenMember{file: readEnd, info: sourceInfo}
	if _, writeErr := writeImmutableStageMember(
		t.Context(), t.Context(), destinationRoot, "pipe-stage.db",
		filepath.Join(root, "pipe-stage.db"), pipeSource,
	); writeErr == nil {
		t.Fatal("write accepted an unseekable source")
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, seekErr := sourceFile.Seek(0, io.SeekStart); seekErr != nil {
		t.Fatal(seekErr)
	}
	if _, writeErr := writeImmutableStageMember(
		canceled, t.Context(), destinationRoot, "canceled-stage.db",
		filepath.Join(root, "canceled-stage.db"), source,
	); !errors.Is(writeErr, context.Canceled) {
		t.Fatalf("write cancellation error = %v", writeErr)
	}

	originalDirectory := filepath.Join(root, "renamed")
	if mkdirErr := os.Mkdir(originalDirectory, 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	renamedRoot, err := os.OpenRoot(originalDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer renamedRoot.Close()
	movedDirectory := originalDirectory + "-moved"
	if err := os.Rename(originalDirectory, movedDirectory); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceFile.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := writeImmutableStageMember(
		t.Context(), t.Context(), renamedRoot, "missing-path.db",
		filepath.Join(originalDirectory, "missing-path.db"), source,
	); err == nil {
		t.Fatal("write accepted a destination outside its retained root path")
	}

	if err := os.Mkdir(originalDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	replacementPath := filepath.Join(originalDirectory, "different-identity.db")
	writeImmutableSourceTestFile(t, replacementPath, []byte("replacement"), time.Now())
	if _, err := sourceFile.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := writeImmutableStageMember(
		t.Context(), t.Context(), renamedRoot, "different-identity.db", replacementPath, source,
	); err == nil {
		t.Fatal("write accepted a pathname with a different identity")
	}
}

func TestStagedSourceCloseoutByteCopyFailures(t *testing.T) {
	t.Parallel()

	if _, _, err := copyImmutableGenerationBytes(
		t.Context(), t.Context(), nil, bytes.NewReader(nil), 0,
	); err == nil {
		t.Fatal("byte copy accepted nil destination")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := copyImmutableGenerationBytes(
		canceled, t.Context(), io.Discard, bytes.NewReader([]byte("x")), 1,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("byte copy cancellation error = %v", err)
	}

	tests := []struct {
		name   string
		dst    io.Writer
		source io.Reader
		limit  int64
		want   error
	}{
		{
			name: "growth", dst: io.Discard,
			source: bytes.NewReader([]byte("xy")), limit: 1,
		},
		{
			name: "terminal read error", dst: io.Discard,
			source: &stagedSourceErrorReader{err: io.ErrClosedPipe}, limit: 0, want: io.ErrClosedPipe,
		},
		{
			name: "early eof", dst: io.Discard,
			source: bytes.NewReader(nil), limit: 1, want: io.ErrUnexpectedEOF,
		},
		{
			name: "read error", dst: io.Discard,
			source: &stagedSourceErrorReader{err: io.ErrClosedPipe}, limit: 1, want: io.ErrClosedPipe,
		},
		{
			name: "no progress", dst: io.Discard,
			source: stagedSourceNoProgressReader{}, limit: 1, want: io.ErrNoProgress,
		},
		{
			name: "write error", dst: stagedSourceErrorWriter{},
			source: bytes.NewReader([]byte("x")), limit: 1, want: io.ErrClosedPipe,
		},
		{
			name: "zero write", dst: stagedSourceNoProgressWriter{},
			source: bytes.NewReader([]byte("x")), limit: 1, want: io.ErrShortWrite,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := copyImmutableGenerationBytes(
				t.Context(), t.Context(), test.dst, test.source, test.limit,
			)
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("byte copy error = %v, want %v", err, test.want)
			}
			if test.want == nil && err == nil {
				t.Fatal("byte copy unexpectedly succeeded")
			}
		})
	}
}

func TestStagedSourceCloseoutRevalidationAndInventory(t *testing.T) {
	t.Parallel()

	if err := revalidateImmutableGeneration(nil, immutableGenerationSnapshot{}, nil); err == nil {
		t.Fatal("revalidation accepted nil context")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := revalidateImmutableGeneration(
		canceled, immutableGenerationSnapshot{}, nil,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("revalidation cancellation error = %v", err)
	}

	root := t.TempDir()
	nondirectory := filepath.Join(root, "not-directory")
	if err := os.WriteFile(nondirectory, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := revalidateImmutableGeneration(t.Context(), immutableGenerationSnapshot{
		path: filepath.Join(nondirectory, "source.db"),
	}, nil); err == nil {
		t.Fatal("revalidation accepted invalid source parent")
	}

	original := filepath.Join(root, "original.db")
	writeImmutableSourceTestFile(t, original, []byte("original"), time.Now())
	originalFile, err := os.Open(original)
	if err != nil {
		t.Fatal(err)
	}
	defer originalFile.Close()
	originalInfo, err := originalFile.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if renameErr := os.Rename(original, original+".old"); renameErr != nil {
		t.Fatal(renameErr)
	}
	writeImmutableSourceTestFile(t, original, []byte("replacement"), time.Now())
	replacementSnapshot, err := captureImmutableGeneration(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := revalidateImmutableGeneration(t.Context(), replacementSnapshot, []immutableGenerationOpenMember{
		{path: original, info: originalInfo, file: originalFile},
	}); err == nil {
		t.Fatal("revalidation accepted a replaced open member")
	}

	stage := filepath.Join(root, "stage.db")
	if err := validateCopiedImmutableStage(t.Context(), t.Context(), stage, []immutableGenerationOpenMember{
		{index: -1},
	}); err == nil {
		t.Fatal("stage validation accepted invalid source inventory")
	}
	if err := validateCopiedImmutableStage(t.Context(), t.Context(), stage, []immutableGenerationOpenMember{
		{index: 0},
	}); err == nil {
		t.Fatal("stage validation accepted missing destination member")
	}
	if err := validateCopiedImmutableStage(
		t.Context(), t.Context(), filepath.Join(nondirectory, "stage.db"), nil,
	); err == nil {
		t.Fatal("stage validation accepted invalid parent")
	}

	unreadableDirectory := filepath.Join(root, "unreadable")
	if err := os.Mkdir(unreadableDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	unreadableStage := filepath.Join(unreadableDirectory, "stage.db")
	writeImmutableSourceTestFile(t, unreadableStage, []byte("stage"), time.Now())
	if err := os.Chmod(unreadableDirectory, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadableDirectory, 0o700) })
	if err := validateCopiedImmutableStage(
		t.Context(), t.Context(), unreadableStage, []immutableGenerationOpenMember{{index: 0}},
	); err == nil {
		t.Fatal("stage validation opened an unreadable destination root")
	}

	if err := requireUnusedImmutableStage(filepath.Join(nondirectory, "stage.db")); err == nil {
		t.Fatal("unused-stage check accepted invalid parent")
	}
}

func TestStagedSourceCloseoutCleanupFailures(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	missing := filepath.Join(root, "missing.db")
	infoPath := filepath.Join(root, "identity.db")
	writeImmutableSourceTestFile(t, infoPath, []byte("identity"), time.Now())
	info, err := os.Lstat(infoPath)
	if err != nil {
		t.Fatal(err)
	}
	nondirectory := filepath.Join(root, "not-directory")
	if writeErr := os.WriteFile(nondirectory, []byte("file"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	different := filepath.Join(root, "different.db")
	writeImmutableSourceTestFile(t, different, []byte("different"), time.Now())
	differentInfo, err := os.Lstat(different)
	if err != nil {
		t.Fatal(err)
	}
	removeCanary := errors.New("remove canary")
	ops := defaultImmutableGenerationCopyOps()
	ops.remove = func(path string) error {
		if path == infoPath {
			return removeCanary
		}
		return os.Remove(path)
	}
	err = cleanupImmutableStage([]immutableStageMember{
		{path: missing, info: info},
		{path: filepath.Join(nondirectory, "child"), info: info},
		{path: different, info: info},
		{path: infoPath, info: differentInfo},
		{path: infoPath, info: info},
	}, root, ops)
	if !errors.Is(err, removeCanary) {
		t.Fatalf("cleanup result = %v", err)
	}

	parentCanceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := immutableGenerationContextError(parentCanceled, t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("parent context error = %v", err)
	}
	if err := immutableGenerationContextError(nil, t.Context()); err == nil {
		t.Fatal("context helper accepted nil parent")
	}
}

type stagedSourceErrorReader struct{ err error }

func (reader *stagedSourceErrorReader) Read([]byte) (int, error) { return 0, reader.err }

type stagedSourceNoProgressReader struct{}

func (stagedSourceNoProgressReader) Read([]byte) (int, error) { return 0, nil }

type stagedSourceErrorWriter struct{}

func (stagedSourceErrorWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type stagedSourceNoProgressWriter struct{}

func (stagedSourceNoProgressWriter) Write([]byte) (int, error) { return 0, nil }

func waitForStagedSourceStack(t *testing.T, wanted string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		buffer := make([]byte, 1<<20)
		length := runtime.Stack(buffer, true)
		if strings.Contains(string(buffer[:length]), wanted) {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("timed out waiting for goroutine stack containing %q", wanted)
}
