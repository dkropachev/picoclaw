package sqliteprovider

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestCopyImmutableGenerationToStagePreservesSourceAndCopiesWAL(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	stage := filepath.Join(root, "stage.db")
	members := map[string][]byte{
		"":     bytes.Repeat([]byte("main"), 257),
		"-wal": bytes.Repeat([]byte("wal"), 193),
		"-shm": bytes.Repeat([]byte("shm"), 149),
	}
	timestamp := time.Unix(1_700_000_000, 123_456_789)
	before := make(map[string]immutableSourceTestState, len(members))
	for suffix, payload := range members {
		path := source + suffix
		writeImmutableSourceTestFile(t, path, payload, timestamp)
		before[path] = readImmutableSourceTestState(t, path)
	}

	callbackCalls := 0
	sourceCapability := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		callbackCalls++
		return use(ctx, source)
	})
	exists, err := copyImmutableGenerationToStage(t.Context(), sourceCapability, stage)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || callbackCalls != 1 {
		t.Fatalf("copy exists=%t callback calls=%d", exists, callbackCalls)
	}
	for suffix, payload := range members {
		path := stage + suffix
		if got, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(got, payload) {
			t.Fatalf("copied member %q = %x, %v", suffix, got, readErr)
		}
		if info, statErr := os.Lstat(path); statErr != nil ||
			!info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("copied member %q metadata = %v, %v", suffix, info, statErr)
		}
	}
	if _, err := os.Lstat(stage + "-journal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("copy invented rollback journal: %v", err)
	}
	for path, expected := range before {
		if current := readImmutableSourceTestState(t, path); current != expected {
			t.Errorf("source member changed:\n before=%#v\n  after=%#v", expected, current)
		}
	}
}

func TestCopyImmutableGenerationToStagePreservesRollbackJournalRole(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	stage := filepath.Join(root, "stage.db")
	writeImmutableSourceTestFile(t, source, []byte("main"), time.Unix(1_700_000_001, 0))
	writeImmutableSourceTestFile(t, source+"-journal", []byte("journal"), time.Unix(1_700_000_002, 0))

	exists, err := copyImmutableGenerationToStage(
		t.Context(),
		immutableGenerationSourceForTest(func(
			ctx context.Context, use func(context.Context, string) error,
		) error {
			return use(ctx, source)
		}),
		stage,
	)
	if err != nil || !exists {
		t.Fatalf("copy rollback generation exists=%t err=%v", exists, err)
	}
	if payload, err := os.ReadFile(stage + "-journal"); err != nil || string(payload) != "journal" {
		t.Fatalf("rollback journal copy = %q, %v", payload, err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(stage + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rollback copy created %s: %v", suffix, err)
		}
	}
}

func TestCopyImmutableGenerationToStagePreservesMissingGeneration(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "missing.db")
	stage := filepath.Join(root, "stage.db")
	exists, err := copyImmutableGenerationToStage(
		t.Context(),
		immutableGenerationSourceForTest(func(
			ctx context.Context, use func(context.Context, string) error,
		) error {
			return use(ctx, source)
		}),
		stage,
	)
	if err != nil || exists {
		t.Fatalf("missing generation copy exists=%t err=%v", exists, err)
	}
	assertImmutableStageAbsent(t, stage)
}

func TestInvokeImmutableGenerationSourceRejectsInvalidCallbackLifecycle(t *testing.T) {
	t.Parallel()

	operation := func(context.Context, string) error { return nil }
	tests := []struct {
		name   string
		source ImmutableGenerationSource
	}{
		{
			name: "zero calls",
			source: immutableGenerationSourceForTest(func(
				context.Context, func(context.Context, string) error,
			) error {
				return nil
			}),
		},
		{
			name: "two calls",
			source: immutableGenerationSourceForTest(func(
				ctx context.Context, use func(context.Context, string) error,
			) error {
				_ = use(ctx, "first")
				_ = use(ctx, "second")
				return nil
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := invokeImmutableGenerationSource(t.Context(), test.source, operation)
			if !errors.Is(err, errImmutableGenerationSourceContract) {
				t.Fatalf("lifecycle error = %v", err)
			}
		})
	}
}

func TestInvokeImmutableGenerationSourceRejectsLateAndRetainedCallback(t *testing.T) {
	t.Parallel()

	var retained func(context.Context, string) error
	source := immutableGenerationSourceForTest(func(
		_ context.Context,
		use func(context.Context, string) error,
	) error {
		retained = use
		return nil
	})
	err := invokeImmutableGenerationSource(t.Context(), source, func(context.Context, string) error {
		t.Fatal("late callback reached operation")
		return nil
	})
	if !errors.Is(err, errImmutableGenerationSourceContract) || retained == nil {
		t.Fatalf("async source result = %v retained=%t", err, retained != nil)
	}
	if err := retained(t.Context(), "late"); !errors.Is(err, errImmutableGenerationSourceContract) {
		t.Fatalf("retained callback error = %v", err)
	}
}

func TestInvokeImmutableGenerationSourceContainsOperationPanic(t *testing.T) {
	t.Parallel()

	done := make(chan error, 1)
	go func() {
		done <- invokeImmutableGenerationSource(
			t.Context(),
			immutableGenerationSourceForTest(func(
				ctx context.Context, use func(context.Context, string) error,
			) error {
				return use(ctx, "source")
			}),
			func(context.Context, string) error { panic("canary") },
		)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errImmutableGenerationSourceContract) {
			t.Fatalf("panic result = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("operation panic left immutable-source invocation blocked")
	}
}

func TestCopyImmutableGenerationToStageRejectsIncoherentAndReplacedSource(t *testing.T) {
	t.Parallel()

	t.Run("orphan shm", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		source := filepath.Join(root, "source.db")
		writeImmutableSourceTestFile(t, source, []byte("main"), time.Now())
		writeImmutableSourceTestFile(t, source+"-shm", []byte("shm"), time.Now())
		err := copyImmutableSourceTest(t, source, filepath.Join(root, "stage.db"), nil)
		if err == nil {
			t.Fatal("orphan SHM source was copied")
		}
	})

	t.Run("mixed wal and journal", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		source := filepath.Join(root, "source.db")
		for _, suffix := range []string{"", "-wal", "-journal"} {
			writeImmutableSourceTestFile(t, source+suffix, []byte("data"+suffix), time.Now())
		}
		err := copyImmutableSourceTest(t, source, filepath.Join(root, "stage.db"), nil)
		if err == nil {
			t.Fatal("mixed WAL and rollback-journal source was copied")
		}
	})

	t.Run("replacement after open", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		source := filepath.Join(root, "source.db")
		stage := filepath.Join(root, "stage.db")
		writeImmutableSourceTestFile(t, source, []byte("original"), time.Now())
		ops := defaultImmutableGenerationCopyOps()
		ops.afterSourceOpen = func(string) error {
			if err := os.Rename(source, source+".old"); err != nil {
				return err
			}
			return os.WriteFile(source, []byte("replacement"), 0o600)
		}
		err := copyImmutableSourceTest(t, source, stage, &ops)
		if err == nil {
			t.Fatal("replaced immutable source was copied")
		}
		assertImmutableStageAbsent(t, stage)
	})

	t.Run("hardlink", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		source := filepath.Join(root, "source.db")
		writeImmutableSourceTestFile(t, source, []byte("main"), time.Now())
		if err := os.Link(source, source+".alias"); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		if err := copyImmutableSourceTest(
			t, source, filepath.Join(root, "stage.db"), nil,
		); err == nil {
			t.Fatal("hard-linked immutable source was copied")
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("nonprivate mode", func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source := filepath.Join(root, "source.db")
			writeImmutableSourceTestFile(t, source, []byte("main"), time.Now())
			if err := os.Chmod(source, 0o640); err != nil {
				t.Fatal(err)
			}
			if err := copyImmutableSourceTest(
				t, source, filepath.Join(root, "stage.db"), nil,
			); err == nil {
				t.Fatal("nonprivate immutable source was copied")
			}
		})
	}
}

func TestCopyImmutableGenerationToStageCleansPartialCopy(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	stage := filepath.Join(root, "stage.db")
	writeImmutableSourceTestFile(t, source, bytes.Repeat([]byte("main"), 64), time.Now())
	writeImmutableSourceTestFile(t, source+"-wal", bytes.Repeat([]byte("wal"), 64), time.Now())
	writeImmutableSourceTestFile(t, source+"-shm", bytes.Repeat([]byte("shm"), 64), time.Now())
	canary := errors.New("destination canary")
	ops := defaultImmutableGenerationCopyOps()
	ops.beforeDestinationOpen = func(index int, _ string) error {
		if index == 1 {
			return canary
		}
		return nil
	}
	removed := make(map[string]int)
	var removedMu sync.Mutex
	originalRemove := ops.remove
	ops.remove = func(path string) error {
		removedMu.Lock()
		removed[path]++
		removedMu.Unlock()
		return originalRemove(path)
	}
	err := copyImmutableSourceTest(t, source, stage, &ops)
	if !errors.Is(err, canary) {
		t.Fatalf("partial-copy error = %v", err)
	}
	assertImmutableStageAbsent(t, stage)
	if removed[stage] != 1 {
		t.Fatalf("created main removal count = %d", removed[stage])
	}
}

func TestCopyImmutableGenerationToStageRejectsExistingDestination(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	stage := filepath.Join(root, "stage.db")
	writeImmutableSourceTestFile(t, source, []byte("source"), time.Now())
	writeImmutableSourceTestFile(t, stage, []byte("incumbent"), time.Now())
	called := false
	_, err := copyImmutableGenerationToStage(
		t.Context(),
		immutableGenerationSourceForTest(func(
			ctx context.Context, use func(context.Context, string) error,
		) error {
			called = true
			return use(ctx, source)
		}),
		stage,
	)
	if err == nil || called {
		t.Fatalf("existing stage error=%v callback called=%t", err, called)
	}
	if payload, readErr := os.ReadFile(stage); readErr != nil || string(payload) != "incumbent" {
		t.Fatalf("existing stage changed: %q, %v", payload, readErr)
	}
}

func TestCopyImmutableGenerationToStageHonorsCanceledContextBeforeSourceUse(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancelCause(t.Context())
	canary := errors.New("canceled source canary")
	cancel(canary)
	called := false
	_, err := copyImmutableGenerationToStage(
		ctx,
		immutableGenerationSourceForTest(func(
			ctx context.Context, use func(context.Context, string) error,
		) error {
			called = true
			return use(ctx, filepath.Join(t.TempDir(), "source.db"))
		}),
		filepath.Join(t.TempDir(), "stage.db"),
	)
	if !errors.Is(err, canary) || called {
		t.Fatalf("canceled copy error=%v callback called=%t", err, called)
	}
}

type immutableSourceTestState struct {
	payload string
	mode    os.FileMode
	size    int64
	modTime time.Time
}

func writeImmutableSourceTestFile(
	t *testing.T,
	path string,
	payload []byte,
	modTime time.Time,
) {
	t.Helper()
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func readImmutableSourceTestState(t *testing.T, path string) immutableSourceTestState {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return immutableSourceTestState{
		payload: string(payload), mode: info.Mode(), size: info.Size(), modTime: info.ModTime(),
	}
}

func copyImmutableSourceTest(
	t *testing.T,
	source string,
	stage string,
	ops *immutableGenerationCopyOps,
) error {
	t.Helper()
	capability := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, source)
	})
	if ops == nil {
		_, err := copyImmutableGenerationToStage(t.Context(), capability, stage)
		return err
	}
	_, err := copyImmutableGenerationToStageWithOps(t.Context(), capability, stage, *ops)
	return err
}

func assertImmutableStageAbsent(t *testing.T, stage string) {
	t.Helper()
	for index := 0; index < 4; index++ {
		path := stage + immutableGenerationSuffix(index)
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("partial stage member remains at %s: %v", path, err)
		}
	}
}
