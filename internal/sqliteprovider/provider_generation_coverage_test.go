package sqliteprovider

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type providerGenerationCoverageFixture struct {
	main        string
	wal         string
	shm         string
	journal     string
	mainInfo    os.FileInfo
	walInfo     os.FileInfo
	replacement os.FileInfo
}

func newProviderGenerationCoverageFixture(t *testing.T) providerGenerationCoverageFixture {
	t.Helper()
	root := t.TempDir()
	mainPath := filepath.Join(root, "store.db")
	walPath := mainPath + "-wal"
	shmPath := mainPath + "-shm"
	journalPath := mainPath + "-journal"
	replacementPath := filepath.Join(root, "replacement")
	for _, path := range []string{mainPath, walPath, shmPath, journalPath, replacementPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stat := func(path string) os.FileInfo {
		t.Helper()
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	return providerGenerationCoverageFixture{
		main:        mainPath,
		wal:         walPath,
		shm:         shmPath,
		journal:     journalPath,
		mainInfo:    stat(mainPath),
		walInfo:     stat(walPath),
		replacement: stat(replacementPath),
	}
}

func (fixture providerGenerationCoverageFixture) filesystem() providerFilesystem {
	return providerFilesystem{
		validateSyntax:    func(string) error { return nil },
		validateAncestors: func(string) error { return nil },
		lstat:             os.Lstat,
		secureDirectory:   func(string) error { return nil },
		secureFile:        func(string) error { return nil },
		validateLiveInfo:  func(os.FileInfo) error { return nil },
		linkCount: func(string, os.FileInfo) generationLinkClass {
			return generationLinkSingle
		},
		owner: func(string, os.FileInfo) generationOwnerClass {
			return generationOwnerCurrent
		},
	}
}

type providerPrepareCoverageFile struct {
	info       os.FileInfo
	syncErr    error
	closeErr   error
	closeCalls int
}

func (file *providerPrepareCoverageFile) Stat() (os.FileInfo, error) { return file.info, nil }
func (file *providerPrepareCoverageFile) Chmod(os.FileMode) error    { return nil }
func (file *providerPrepareCoverageFile) Sync() error                { return file.syncErr }
func (file *providerPrepareCoverageFile) Close() error {
	file.closeCalls++
	return file.closeErr
}

type providerPrepareCoverageState struct {
	path            string
	parent          string
	parentInfo      os.FileInfo
	mainInfo        os.FileInfo
	replacementInfo os.FileInfo
	created         bool
	secured         bool
	file            *providerPrepareCoverageFile
	createdInfo     func() (os.FileInfo, error)
}

func newProviderPrepareCoverageState(t *testing.T) *providerPrepareCoverageState {
	t.Helper()
	root := t.TempDir()
	identityPath := filepath.Join(root, "identity")
	replacementPath := filepath.Join(root, "replacement")
	for _, path := range []string{identityPath, replacementPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stat := func(path string) os.FileInfo {
		t.Helper()
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	state := &providerPrepareCoverageState{
		path:            filepath.Join(root, "store.db"),
		parent:          root,
		parentInfo:      stat(root),
		mainInfo:        stat(identityPath),
		replacementInfo: stat(replacementPath),
	}
	state.file = &providerPrepareCoverageFile{info: state.mainInfo}
	state.createdInfo = func() (os.FileInfo, error) { return state.mainInfo, nil }
	return state
}

func (state *providerPrepareCoverageState) filesystem() providerFilesystem {
	return providerFilesystem{
		validateSyntax:    func(string) error { return nil },
		validateAncestors: func(string) error { return nil },
		mkdirAll:          func(string, os.FileMode) error { return nil },
		lstat: func(path string) (os.FileInfo, error) {
			switch path {
			case state.parent:
				return state.parentInfo, nil
			case state.path:
				if state.created {
					return state.createdInfo()
				}
			}
			return nil, os.ErrNotExist
		},
		openFile: func(string, int, os.FileMode) (providerFile, error) {
			state.created = true
			return state.file, nil
		},
		secureDirectory: func(string) error { return nil },
		secureFile: func(string) error {
			state.secured = true
			return nil
		},
		validateLiveInfo: func(os.FileInfo) error { return nil },
		syncDirectory:    func(string) error { return nil },
		linkCount: func(string, os.FileInfo) generationLinkClass {
			return generationLinkSingle
		},
		owner: func(string, os.FileInfo) generationOwnerClass {
			return generationOwnerCurrent
		},
	}
}

func TestProviderGenerationCoveragePrepareConcurrentWinnerFailures(t *testing.T) {
	t.Parallel()
	t.Run("invalid winner", func(t *testing.T) {
		t.Parallel()
		state := newProviderPrepareCoverageState(t)
		filesystem := state.filesystem()
		filesystem.openFile = func(string, int, os.FileMode) (providerFile, error) {
			state.created = true
			return nil, os.ErrExist
		}
		state.createdInfo = func() (os.FileInfo, error) { return nil, nil }
		err := prepareStore(state.path, filesystem)
		if err == nil || !strings.Contains(err.Error(), "concurrently created store") {
			t.Fatalf("invalid concurrent winner error = %v", err)
		}
	})
	t.Run("winner generation validation", func(t *testing.T) {
		t.Parallel()
		state := newProviderPrepareCoverageState(t)
		filesystem := state.filesystem()
		filesystem.openFile = func(string, int, os.FileMode) (providerFile, error) {
			state.created = true
			return nil, os.ErrExist
		}
		filesystem.linkCount = func(string, os.FileInfo) generationLinkClass {
			return generationLinkMultiple
		}
		err := prepareStore(state.path, filesystem)
		if err == nil || !errors.Is(err, errProviderUnsafeBoundary) ||
			!strings.Contains(err.Error(), "hardlink alias") {
			t.Fatalf("unsafe concurrent generation error = %v", err)
		}
	})
}

func TestProviderGenerationCoveragePrepareNewMainSecurityFailures(t *testing.T) {
	t.Parallel()
	for _, validationErr := range []error{errProviderGenerationTransition, os.ErrNotExist} {
		t.Run(validationErr.Error(), func(t *testing.T) {
			t.Parallel()
			state := newProviderPrepareCoverageState(t)
			filesystem := state.filesystem()
			filesystem.secureFile = func(string) error { return validationErr }
			err := prepareStore(state.path, filesystem)
			if err == nil || !errors.Is(err, errProviderUnsafeBoundary) ||
				!strings.Contains(err.Error(), "changed while securing") {
				t.Fatalf("security transition error = %v", err)
			}
			if state.file.closeCalls != 1 {
				t.Fatalf("close calls = %d, want 1", state.file.closeCalls)
			}
		})
	}
}

func TestProviderGenerationCoveragePreparePostSecurityFailures(t *testing.T) {
	t.Parallel()
	canary := errors.New("prepare post-security coverage canary")
	tests := []struct {
		name       string
		configure  func(*providerPrepareCoverageState, *providerFilesystem)
		want       error
		wantUnsafe bool
	}{
		{
			name: "missing secured metadata",
			configure: func(state *providerPrepareCoverageState, _ *providerFilesystem) {
				state.createdInfo = func() (os.FileInfo, error) {
					if state.secured {
						return nil, nil
					}
					return state.mainInfo, nil
				}
			},
			wantUnsafe: true,
		},
		{
			name: "replaced secured identity",
			configure: func(state *providerPrepareCoverageState, _ *providerFilesystem) {
				state.createdInfo = func() (os.FileInfo, error) {
					if state.secured {
						return state.replacementInfo, nil
					}
					return state.mainInfo, nil
				}
			},
			wantUnsafe: true,
		},
		{
			name: "secured metadata validation",
			configure: func(_ *providerPrepareCoverageState, filesystem *providerFilesystem) {
				filesystem.validateLiveInfo = func(os.FileInfo) error { return canary }
			},
			want: canary,
		},
		{
			name: "file sync",
			configure: func(state *providerPrepareCoverageState, _ *providerFilesystem) {
				state.file.syncErr = canary
			},
			want: canary,
		},
		{
			name: "file close",
			configure: func(state *providerPrepareCoverageState, _ *providerFilesystem) {
				state.file.closeErr = canary
			},
			want: canary,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := newProviderPrepareCoverageState(t)
			filesystem := state.filesystem()
			test.configure(state, &filesystem)
			err := prepareStore(state.path, filesystem)
			if err == nil {
				t.Fatal("post-security failure unexpectedly succeeded")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want wrapped %v", err, test.want)
			}
			if test.wantUnsafe && !errors.Is(err, errProviderUnsafeBoundary) {
				t.Fatalf("error = %v, want unsafe boundary", err)
			}
			if state.file.closeCalls != 1 {
				t.Fatalf("close calls = %d, want 1", state.file.closeCalls)
			}
		})
	}
}

type providerGenerationCoverageSystemInfo struct {
	os.FileInfo
	system any
}

func (info providerGenerationCoverageSystemInfo) Sys() any { return info.system }

func TestProviderGenerationCoverageUnixOwnerClassification(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	currentInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if classifyGenerationOwner(path, nil) != generationOwnerUnavailable ||
		classifyGenerationOwner(path, currentInfo) != generationOwnerCurrent {
		t.Skip("owner classification is path-scoped or unavailable on this platform")
	}
	missingStat := providerGenerationCoverageSystemInfo{FileInfo: currentInfo}
	if got := classifyGenerationOwner(path, missingStat); got != generationOwnerUnavailable {
		t.Fatalf("missing-stat owner class = %v, want unavailable", got)
	}
	statValue := reflect.ValueOf(currentInfo.Sys())
	if statValue.Kind() != reflect.Pointer || statValue.IsNil() {
		t.Fatal("current owner stat is unavailable")
	}
	foreignStat := reflect.New(statValue.Elem().Type())
	foreignStat.Elem().Set(statValue.Elem())
	uid := foreignStat.Elem().FieldByName("Uid")
	if !uid.IsValid() || !uid.CanSet() {
		t.Fatal("current owner UID is unavailable")
	}
	switch uid.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		uid.SetUint(uid.Uint() + 1)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		uid.SetInt(uid.Int() + 1)
	default:
		t.Fatalf("unexpected UID kind %v", uid.Kind())
	}
	foreignInfo := providerGenerationCoverageSystemInfo{
		FileInfo: currentInfo,
		system:   foreignStat.Interface(),
	}
	if got := classifyGenerationOwner(path, foreignInfo); got != generationOwnerForeign {
		t.Fatalf("foreign owner class = %v, want foreign", got)
	}
}

func TestProviderGenerationCoverageSecurePreflightAndParentFailures(t *testing.T) {
	t.Parallel()
	canary := errors.New("secure generation coverage canary")
	tests := []struct {
		name       string
		mutate     func(*providerFilesystem, providerGenerationCoverageFixture)
		want       error
		wantText   string
		wantUnsafe bool
	}{
		{
			name: "preflight unavailable",
			mutate: func(filesystem *providerFilesystem, _ providerGenerationCoverageFixture) {
				filesystem.lstat = nil
			},
			wantText: "preflight is unavailable",
		},
		{
			name: "nil main metadata",
			mutate: func(filesystem *providerFilesystem, _ providerGenerationCoverageFixture) {
				filesystem.lstat = func(string) (os.FileInfo, error) { return nil, nil }
			},
			wantText: "main metadata is unavailable",
		},
		{
			name: "parent reinspection I/O",
			mutate: func(filesystem *providerFilesystem, fixture providerGenerationCoverageFixture) {
				calls := 0
				filesystem.lstat = func(path string) (os.FileInfo, error) {
					if path != fixture.main {
						return nil, os.ErrNotExist
					}
					calls++
					if calls == 1 {
						return fixture.mainInfo, nil
					}
					return nil, canary
				}
			},
			want: canary,
		},
		{
			name: "post-parent metadata failure",
			mutate: func(filesystem *providerFilesystem, _ providerGenerationCoverageFixture) {
				parentSecured := false
				filesystem.secureDirectory = func(string) error {
					parentSecured = true
					return nil
				}
				filesystem.validateLiveInfo = func(os.FileInfo) error {
					if parentSecured {
						return canary
					}
					return nil
				}
			},
			want: canary,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newProviderGenerationCoverageFixture(t)
			filesystem := fixture.filesystem()
			test.mutate(&filesystem, fixture)
			err := secureGeneration(fixture.main, filesystem)
			if err == nil {
				t.Fatal("secure generation unexpectedly succeeded")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want wrapped %v", err, test.want)
			}
			if test.wantText != "" && !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %v, want text %q", err, test.wantText)
			}
			if test.wantUnsafe && !errors.Is(err, errProviderUnsafeBoundary) {
				t.Fatalf("error = %v, want unsafe boundary", err)
			}
		})
	}
}

func TestProviderGenerationCoverageFileValidationAvailability(t *testing.T) {
	t.Parallel()
	fixture := newProviderGenerationCoverageFixture(t)
	tests := []struct {
		name   string
		mutate func(*providerFilesystem)
		text   string
	}{
		{
			name: "live validation unavailable",
			mutate: func(filesystem *providerFilesystem) {
				filesystem.secureFile = nil
			},
			text: "live-file validation is unavailable",
		},
		{
			name: "final metadata validation unavailable",
			mutate: func(filesystem *providerFilesystem) {
				filesystem.validateLiveInfo = nil
			},
			text: "final live-file validation is unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			filesystem := fixture.filesystem()
			test.mutate(&filesystem)
			_, err := validateProviderGenerationFile(filesystem, fixture.main, fixture.mainInfo)
			if err == nil || !strings.Contains(err.Error(), test.text) {
				t.Fatalf("error = %v, want text %q", err, test.text)
			}
		})
	}
}

func TestProviderGenerationCoverageRequiredValidationFailureClassification(t *testing.T) {
	t.Parallel()
	fixture := newProviderGenerationCoverageFixture(t)
	canary := errors.New("validation coverage canary")
	for _, test := range []struct {
		name    string
		current os.FileInfo
		err     error
		text    string
	}{
		{name: "disappeared", err: os.ErrNotExist, text: "main disappeared"},
		{name: "changed", current: fixture.replacement, text: "main changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			transitioned, err := classifyGenerationFileValidationError(
				providerFilesystem{lstat: func(string) (os.FileInfo, error) {
					return test.current, test.err
				}},
				fixture.main,
				fixture.mainInfo,
				false,
				canary,
			)
			if transitioned || err == nil || !errors.Is(err, errProviderUnsafeBoundary) ||
				!strings.Contains(err.Error(), test.text) {
				t.Fatalf("classification = transitioned:%v error:%v", transitioned, err)
			}
		})
	}
}

func TestProviderGenerationCoverageMetadataTransitionTaxonomy(t *testing.T) {
	t.Parallel()
	fixture := newProviderGenerationCoverageFixture(t)
	canary := errors.New("metadata coverage canary")
	tests := []struct {
		name            string
		optional        bool
		configure       func(*providerFilesystem)
		wantTransition  bool
		wantUnsafe      bool
		wantCanary      bool
		wantUnavailable bool
	}{
		{
			name: "metadata validator unavailable",
			configure: func(filesystem *providerFilesystem) {
				filesystem.validateLiveInfo = nil
			},
			wantUnavailable: true,
		},
		{
			name:     "optional metadata transition",
			optional: true,
			configure: func(filesystem *providerFilesystem) {
				filesystem.validateLiveInfo = func(os.FileInfo) error {
					return errProviderGenerationTransition
				}
			},
			wantTransition: true,
		},
		{
			name: "required metadata transition",
			configure: func(filesystem *providerFilesystem) {
				filesystem.validateLiveInfo = func(os.FileInfo) error {
					return errProviderGenerationTransition
				}
			},
			wantUnsafe: true,
		},
		{
			name: "link classifier and reinspection unavailable",
			configure: func(filesystem *providerFilesystem) {
				filesystem.linkCount = nil
				filesystem.lstat = nil
			},
			wantUnavailable: true,
		},
		{
			name:     "optional disappeared during link reinspection",
			optional: true,
			configure: func(filesystem *providerFilesystem) {
				filesystem.linkCount = nil
				filesystem.lstat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			},
			wantTransition: true,
		},
		{
			name: "required disappeared during link reinspection",
			configure: func(filesystem *providerFilesystem) {
				filesystem.linkCount = nil
				filesystem.lstat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			},
			wantUnsafe: true,
		},
		{
			name: "link reinspection I/O",
			configure: func(filesystem *providerFilesystem) {
				filesystem.linkCount = nil
				filesystem.lstat = func(string) (os.FileInfo, error) { return nil, canary }
			},
			wantCanary: true,
		},
		{
			name:     "optional changed during link reinspection",
			optional: true,
			configure: func(filesystem *providerFilesystem) {
				filesystem.linkCount = nil
				filesystem.lstat = func(string) (os.FileInfo, error) {
					return fixture.replacement, nil
				}
			},
			wantTransition: true,
		},
		{
			name: "required changed during link reinspection",
			configure: func(filesystem *providerFilesystem) {
				filesystem.linkCount = nil
				filesystem.lstat = func(string) (os.FileInfo, error) {
					return fixture.replacement, nil
				}
			},
			wantUnsafe: true,
		},
		{
			name: "owner classifier and reinspection unavailable",
			configure: func(filesystem *providerFilesystem) {
				filesystem.owner = nil
				filesystem.lstat = nil
			},
			wantUnavailable: true,
		},
		{
			name:     "optional disappeared during owner reinspection",
			optional: true,
			configure: func(filesystem *providerFilesystem) {
				filesystem.owner = nil
				filesystem.lstat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			},
			wantTransition: true,
		},
		{
			name: "required disappeared during owner reinspection",
			configure: func(filesystem *providerFilesystem) {
				filesystem.owner = nil
				filesystem.lstat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			},
			wantUnsafe: true,
		},
		{
			name: "owner reinspection I/O",
			configure: func(filesystem *providerFilesystem) {
				filesystem.owner = nil
				filesystem.lstat = func(string) (os.FileInfo, error) { return nil, canary }
			},
			wantCanary: true,
		},
		{
			name:     "optional changed during owner reinspection",
			optional: true,
			configure: func(filesystem *providerFilesystem) {
				filesystem.owner = nil
				filesystem.lstat = func(string) (os.FileInfo, error) {
					return fixture.replacement, nil
				}
			},
			wantTransition: true,
		},
		{
			name: "required changed during owner reinspection",
			configure: func(filesystem *providerFilesystem) {
				filesystem.owner = nil
				filesystem.lstat = func(string) (os.FileInfo, error) {
					return fixture.replacement, nil
				}
			},
			wantUnsafe: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			filesystem := fixture.filesystem()
			test.configure(&filesystem)
			transitioned, err := validateGenerationMemberMetadata(
				fixture.main,
				fixture.mainInfo,
				test.optional,
				filesystem,
			)
			if transitioned != test.wantTransition {
				t.Fatalf("transitioned = %v, want %v (error %v)", transitioned, test.wantTransition, err)
			}
			if test.wantTransition && err != nil {
				t.Fatalf("transition error = %v", err)
			}
			if !test.wantTransition && err == nil {
				t.Fatal("metadata validation unexpectedly succeeded")
			}
			if test.wantUnsafe && !errors.Is(err, errProviderUnsafeBoundary) {
				t.Fatalf("error = %v, want unsafe boundary", err)
			}
			if test.wantCanary && !errors.Is(err, canary) {
				t.Fatalf("error = %v, want wrapped canary", err)
			}
			if test.wantUnavailable && !strings.Contains(err.Error(), "unavailable") {
				t.Fatalf("error = %v, want unavailable classification", err)
			}
		})
	}
}

func TestProviderGenerationCoverageMemberOrchestrationTransitions(t *testing.T) {
	t.Parallel()
	fixture := newProviderGenerationCoverageFixture(t)
	canary := errors.New("member orchestration coverage canary")
	t.Run("main metadata fails while validating sidecar", func(t *testing.T) {
		t.Parallel()
		filesystem := fixture.filesystem()
		mainValidations := 0
		filesystem.validateLiveInfo = func(info os.FileInfo) error {
			if os.SameFile(info, fixture.mainInfo) {
				mainValidations++
				if mainValidations == 4 {
					return canary
				}
			}
			return nil
		}
		transitioned, err := validateGenerationMembersOnce(fixture.main, true, filesystem)
		if transitioned || !errors.Is(err, canary) {
			t.Fatalf("result = transitioned:%v error:%v", transitioned, err)
		}
	})
	t.Run("optional final metadata transitions", func(t *testing.T) {
		t.Parallel()
		filesystem := fixture.filesystem()
		walValidations := 0
		filesystem.validateLiveInfo = func(info os.FileInfo) error {
			if os.SameFile(info, fixture.walInfo) {
				walValidations++
				if walValidations == 3 {
					return errProviderGenerationTransition
				}
			}
			return nil
		}
		transitioned, err := validateGenerationMembersOnce(fixture.main, true, filesystem)
		if !transitioned || err != nil {
			t.Fatalf("result = transitioned:%v error:%v", transitioned, err)
		}
	})
}

func TestProviderGenerationCoverageSnapshotTransitions(t *testing.T) {
	t.Parallel()
	fixture := newProviderGenerationCoverageFixture(t)
	members := [4]string{fixture.main, fixture.wal, fixture.shm, fixture.journal}
	canary := errors.New("snapshot coverage canary")
	tests := []struct {
		name           string
		observed       [4]os.FileInfo
		configure      func(*providerFilesystem)
		wantTransition bool
		wantUnsafe     bool
		wantCanary     bool
	}{
		{
			name:     "required disappeared",
			observed: [4]os.FileInfo{fixture.mainInfo},
			configure: func(filesystem *providerFilesystem) {
				filesystem.lstat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			},
			wantUnsafe: true,
		},
		{
			name:     "stable generation reinspection I/O",
			observed: [4]os.FileInfo{fixture.mainInfo},
			configure: func(filesystem *providerFilesystem) {
				filesystem.lstat = func(string) (os.FileInfo, error) { return nil, canary }
			},
			wantCanary: true,
		},
		{
			name:     "required changed",
			observed: [4]os.FileInfo{fixture.mainInfo},
			configure: func(filesystem *providerFilesystem) {
				filesystem.lstat = func(string) (os.FileInfo, error) {
					return fixture.replacement, nil
				}
			},
			wantUnsafe: true,
		},
		{
			name:     "required file validation I/O",
			observed: [4]os.FileInfo{fixture.mainInfo},
			configure: func(filesystem *providerFilesystem) {
				filesystem.lstat = func(string) (os.FileInfo, error) {
					return fixture.mainInfo, nil
				}
				filesystem.secureFile = func(string) error { return canary }
			},
			wantCanary: true,
		},
		{
			name:     "required final metadata I/O",
			observed: [4]os.FileInfo{fixture.mainInfo},
			configure: func(filesystem *providerFilesystem) {
				validations := 0
				filesystem.lstat = func(string) (os.FileInfo, error) {
					return fixture.mainInfo, nil
				}
				filesystem.validateLiveInfo = func(os.FileInfo) error {
					validations++
					if validations == 3 {
						return canary
					}
					return nil
				}
			},
			wantCanary: true,
		},
		{
			name:     "optional disappeared",
			observed: [4]os.FileInfo{fixture.mainInfo, fixture.walInfo},
			configure: func(filesystem *providerFilesystem) {
				filesystem.lstat = func(path string) (os.FileInfo, error) {
					if path == fixture.main {
						return fixture.mainInfo, nil
					}
					return nil, os.ErrNotExist
				}
			},
			wantTransition: true,
		},
		{
			name:     "optional changed",
			observed: [4]os.FileInfo{fixture.mainInfo, fixture.walInfo},
			configure: func(filesystem *providerFilesystem) {
				filesystem.lstat = func(path string) (os.FileInfo, error) {
					switch path {
					case fixture.main:
						return fixture.mainInfo, nil
					case fixture.wal:
						return fixture.replacement, nil
					default:
						return nil, os.ErrNotExist
					}
				}
			},
			wantTransition: true,
		},
		{
			name:     "optional final metadata transitions",
			observed: [4]os.FileInfo{fixture.mainInfo, fixture.walInfo},
			configure: func(filesystem *providerFilesystem) {
				walValidations := 0
				filesystem.lstat = func(path string) (os.FileInfo, error) {
					switch path {
					case fixture.main:
						return fixture.mainInfo, nil
					case fixture.wal:
						return fixture.walInfo, nil
					default:
						return nil, os.ErrNotExist
					}
				}
				filesystem.validateLiveInfo = func(info os.FileInfo) error {
					if os.SameFile(info, fixture.walInfo) {
						walValidations++
						if walValidations == 3 {
							return errProviderGenerationTransition
						}
					}
					return nil
				}
			},
			wantTransition: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			filesystem := fixture.filesystem()
			test.configure(&filesystem)
			transitioned, err := generationSnapshotTransitioned(members, test.observed, filesystem)
			if transitioned != test.wantTransition {
				t.Fatalf("transitioned = %v, want %v (error %v)", transitioned, test.wantTransition, err)
			}
			if test.wantTransition && err != nil {
				t.Fatalf("transition error = %v", err)
			}
			if !test.wantTransition && err == nil {
				t.Fatal("snapshot validation unexpectedly succeeded")
			}
			if test.wantUnsafe && !errors.Is(err, errProviderUnsafeBoundary) {
				t.Fatalf("error = %v, want unsafe boundary", err)
			}
			if test.wantCanary && !errors.Is(err, canary) {
				t.Fatalf("error = %v, want wrapped canary", err)
			}
		})
	}
}

func TestProviderGenerationCoverageSnapshotCoherenceForms(t *testing.T) {
	t.Parallel()
	fixture := newProviderGenerationCoverageFixture(t)
	for _, generation := range [][4]os.FileInfo{
		{fixture.mainInfo, fixture.walInfo, nil, fixture.replacement},
		{fixture.mainInfo, nil, fixture.replacement, nil},
	} {
		if err := validateGenerationSnapshotCoherence(generation); err == nil ||
			!errors.Is(err, errProviderUnsafeBoundary) {
			t.Fatalf("incoherent generation error = %v", err)
		}
	}
}
