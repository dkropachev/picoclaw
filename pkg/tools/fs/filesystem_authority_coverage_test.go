package fstools

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
)

var errFilesystemBehaviorRead = errors.New("stream read failed")

type filesystemBehaviorFS struct {
	open func(string) (fs.File, error)
}

type rejectingPublicMediaStore struct{}

func (rejectingPublicMediaStore) Store(string, media.MediaMeta, string) (string, error) {
	return "", errors.New("media registration rejected")
}

func (rejectingPublicMediaStore) Resolve(string) (string, error) {
	return "", errors.New("not implemented")
}

func (rejectingPublicMediaStore) ResolveWithMeta(string) (string, media.MediaMeta, error) {
	return "", media.MediaMeta{}, errors.New("not implemented")
}

func (rejectingPublicMediaStore) ReleaseAll(string) error { return nil }

func (f *filesystemBehaviorFS) ReadFile(string) ([]byte, error) {
	return nil, errors.New("unexpected ReadFile call")
}

func (f *filesystemBehaviorFS) WriteFile(string, []byte) error {
	return errors.New("unexpected WriteFile call")
}

func (f *filesystemBehaviorFS) ReadDir(string) ([]os.DirEntry, error) {
	return nil, errors.New("unexpected ReadDir call")
}

func (f *filesystemBehaviorFS) Open(path string) (fs.File, error) {
	return f.open(path)
}

type filesystemBehaviorFile struct {
	reader      *bytes.Reader
	first       []byte
	readCalls   int
	terminalErr error
	info        os.FileInfo
	statErr     error
	seekCalls   int
	seekFailAt  int
}

func (f *filesystemBehaviorFile) Read(buffer []byte) (int, error) {
	if f.reader != nil {
		return f.reader.Read(buffer)
	}
	if f.readCalls == 0 {
		f.readCalls++
		return copy(buffer, f.first), nil
	}
	f.readCalls++
	return 0, f.terminalErr
}

func (f *filesystemBehaviorFile) Close() error { return nil }

func (f *filesystemBehaviorFile) Stat() (os.FileInfo, error) {
	return f.info, f.statErr
}

type filesystemBehaviorSeekFile struct {
	*filesystemBehaviorFile
}

func (f *filesystemBehaviorSeekFile) Seek(offset int64, whence int) (int64, error) {
	f.seekCalls++
	if f.seekFailAt == f.seekCalls {
		return 0, errors.New("stream seek failed")
	}
	return f.reader.Seek(offset, whence)
}

func TestReadFileBytesValidationPaginationAndMetadata(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "visible.txt")
	if err := os.WriteFile(path, []byte("abcdefgh"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewReadFileTool(workspace, true, 4)
	if tool.Name() != "read_file" || tool.Description() == "" ||
		tool.Parameters()["type"] != "object" {
		t.Fatalf("read tool metadata = %q / %#v", tool.Description(), tool.Parameters())
	}
	for name, args := range map[string]map[string]any{
		"missing path":      {},
		"invalid offset":    {"path": path, "offset": struct{}{}},
		"fractional offset": {"path": path, "offset": 1.5},
		"negative offset":   {"path": path, "offset": -1},
		"invalid length":    {"path": path, "length": "bad"},
		"zero length":       {"path": path, "length": 0},
	} {
		t.Run(name, func(t *testing.T) {
			result := tool.Execute(context.Background(), args)
			if result == nil || !result.IsError {
				t.Fatalf("Execute(%s) = %#v", name, result)
			}
		})
	}
	result := tool.Execute(context.Background(), map[string]any{
		"path": path, "offset": "2", "length": int64(100),
	})
	if result == nil || result.IsError || !strings.Contains(result.ForLLM, "cdef") ||
		!strings.Contains(result.ForLLM, "TRUNCATED") ||
		!strings.Contains(result.ForLLM, "bytes 2-5") {
		t.Fatalf("paginated read result = %#v", result)
	}
	result = tool.Execute(context.Background(), map[string]any{
		"path": path, "offset": 8, "length": 1,
	})
	if result == nil || result.IsError || !strings.Contains(result.ForLLM, "no content") {
		t.Fatalf("EOF read result = %#v", result)
	}

	defaulted := NewReadFileTool(workspace, true, 0, nil)
	if defaulted.maxSize != MaxReadFileSize {
		t.Fatalf("default read size = %d", defaulted.maxSize)
	}
}

func TestWhitelistFilesystemRoutesReadAndListByExactAuthority(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	insideFile := filepath.Join(workspace, "inside.txt")
	outsideFile := filepath.Join(outside, "outside.txt")
	for path, content := range map[string]string{insideFile: "inside", outsideFile: "outside"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	separator := regexp.QuoteMeta(string(os.PathSeparator))
	patterns := []*regexp.Regexp{regexp.MustCompile(
		"^" + regexp.QuoteMeta(filepath.Clean(outside)) + "(?:" + separator + "|$)",
	)}
	filesystem := buildFs(workspace, true, patterns)
	whitelist, ok := filesystem.(*whitelistFs)
	if !ok {
		t.Fatalf("buildFs() = %T", filesystem)
	}
	for path, want := range map[string]string{insideFile: "inside", outsideFile: "outside"} {
		content, err := whitelist.ReadFile(path)
		if err != nil || string(content) != want {
			t.Fatalf("ReadFile(%q) = %q, %v", filepath.Base(path), content, err)
		}
		opened, err := whitelist.Open(path)
		if err != nil {
			t.Fatalf("Open(%q) error = %v", filepath.Base(path), err)
		}
		openedContent, readErr := io.ReadAll(opened)
		_ = opened.Close()
		if readErr != nil || string(openedContent) != want {
			t.Fatalf("Open(%q) content = %q, %v", filepath.Base(path), openedContent, readErr)
		}
	}
	for path := range map[string]struct{}{workspace: {}, outside: {}} {
		entries, err := whitelist.ReadDir(path)
		if err != nil || len(entries) != 1 {
			t.Fatalf("ReadDir(%q) = %#v, %v", filepath.Base(path), entries, err)
		}
	}
	insideNew := filepath.Join(workspace, "inside-new.txt")
	outsideNew := filepath.Join(outside, "outside-new.txt")
	if err := whitelist.WriteFile(insideNew, []byte("inside-new")); err != nil {
		t.Fatal(err)
	}
	if err := whitelist.WriteFile(outsideNew, []byte("outside-new")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{insideNew: "inside-new", outsideNew: "outside-new"} {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != want {
			t.Fatalf("written file %q = %q, %v", filepath.Base(path), content, err)
		}
	}

	sandbox := buildFs(workspace, true, nil)
	if _, err := sandbox.ReadDir(filepath.Join(workspace, "missing")); err == nil {
		t.Fatal("sandbox ReadDir accepted a missing directory")
	}
}

func TestReadFileIntegerAndBinaryValidationMatrix(t *testing.T) {
	for name, test := range map[string]struct {
		value any
		want  int64
		err   bool
	}{
		"absent":       {value: nil, want: 7},
		"float":        {value: float64(3), want: 3},
		"max overflow": {value: math.Inf(1), err: true},
		"int":          {value: int(4), want: 4},
		"int64":        {value: int64(5), want: 5},
		"string":       {value: "6", want: 6},
		"bad string":   {value: "six", err: true},
		"unsupported":  {value: []byte("7"), err: true},
	} {
		t.Run(name, func(t *testing.T) {
			args := map[string]any{}
			if name != "absent" {
				args["value"] = test.value
			}
			got, err := getInt64Arg(args, "value", 7)
			if (err != nil) != test.err || !test.err && got != test.want {
				t.Fatalf("getInt64Arg() = %d, %v; want %d, error=%t", got, err, test.want, test.err)
			}
		})
	}

	for name, test := range map[string]struct {
		data []byte
		want bool
	}{
		"empty":        {data: nil, want: false},
		"text":         {data: []byte("plain text\n"), want: false},
		"nul":          {data: []byte{'a', 0, 'b'}, want: true},
		"json":         {data: []byte(`{"value":1}`), want: false},
		"invalid utf8": {data: []byte{1, 0xff, 2, 0xfe, 3, 0xfd}, want: true},
		"controls":     {data: []byte{1, 2, 3, 'a'}, want: true},
		"long text":    {data: []byte(strings.Repeat("x", 600)), want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := isBinaryReadFileData(test.data); got != test.want {
				t.Fatalf("isBinaryReadFileData() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestReadFileLinesValidationAndBoundaries(t *testing.T) {
	workspace := t.TempDir()
	textPath := filepath.Join(workspace, "lines.txt")
	binaryPath := filepath.Join(workspace, "binary.dat")
	if err := os.WriteFile(textPath, []byte("first\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewReadFileLinesTool(workspace, true, 128, nil)
	if tool.Name() != "read_file" || tool.Description() == "" ||
		tool.Parameters()["type"] != "object" {
		t.Fatalf("line reader metadata = %q / %#v", tool.Description(), tool.Parameters())
	}
	for name, args := range map[string]map[string]any{
		"missing path":      {},
		"invalid start":     {"path": textPath, "start_line": struct{}{}},
		"zero start":        {"path": textPath, "start_line": 0},
		"legacy offset":     {"path": textPath, "offset": 1},
		"legacy length":     {"path": textPath, "length": 1},
		"legacy limit":      {"path": textPath, "limit": 1},
		"invalid max lines": {"path": textPath, "max_lines": "bad"},
		"zero max lines":    {"path": textPath, "max_lines": 0},
		"directory":         {"path": workspace},
		"binary":            {"path": binaryPath},
	} {
		t.Run(name, func(t *testing.T) {
			result := tool.Execute(context.Background(), args)
			if result == nil || !result.IsError {
				t.Fatalf("Execute(%s) = %#v", name, result)
			}
		})
	}
	result := tool.Execute(context.Background(), map[string]any{
		"path": textPath, "start_line": 9,
	})
	if result == nil || result.IsError || !strings.Contains(result.ForLLM, "no content") {
		t.Fatalf("past-EOF line read = %#v", result)
	}
	result = tool.Execute(context.Background(), map[string]any{
		"path": textPath, "max_lines": 1,
	})
	if result == nil || result.IsError || !strings.Contains(result.ForLLM, "PARTIAL") ||
		!strings.Contains(result.ForLLM, "1|first") {
		t.Fatalf("partial line read = %#v", result)
	}
}

func TestReadFileBytesReportsStreamPositionAndReadFailures(t *testing.T) {
	info := filesystemBehaviorRegularInfo(t)
	newTool := func(open func(string) (fs.File, error)) *ReadFileTool {
		tool := NewReadFileTool("", false, 16)
		tool.fs = &filesystemBehaviorFS{open: open}
		return tool
	}

	t.Run("seek reset", func(t *testing.T) {
		tool := newTool(func(string) (fs.File, error) {
			return &filesystemBehaviorSeekFile{filesystemBehaviorFile: &filesystemBehaviorFile{
				reader: bytes.NewReader([]byte("content")), info: info, seekFailAt: 1,
			}}, nil
		})
		result := tool.Execute(context.Background(), map[string]any{"path": "stream.txt"})
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "reset file position") {
			t.Fatalf("seek-reset result = %#v", result)
		}
	})

	t.Run("requested seek", func(t *testing.T) {
		tool := newTool(func(string) (fs.File, error) {
			return &filesystemBehaviorSeekFile{filesystemBehaviorFile: &filesystemBehaviorFile{
				reader: bytes.NewReader([]byte("content")), info: info, seekFailAt: 2,
			}}, nil
		})
		result := tool.Execute(context.Background(), map[string]any{
			"path": "stream.txt", "offset": 2,
		})
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "seek to offset 2") {
			t.Fatalf("requested-seek result = %#v", result)
		}
	})

	t.Run("offset inside consumed prefix", func(t *testing.T) {
		tool := newTool(func(string) (fs.File, error) {
			return &filesystemBehaviorFile{
				reader: bytes.NewReader([]byte("content")), info: info,
			}, nil
		})
		result := tool.Execute(context.Background(), map[string]any{
			"path": "stream.txt", "offset": 2,
		})
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "non-seekable") {
			t.Fatalf("consumed-prefix result = %#v", result)
		}
	})

	t.Run("non-seekable offset", func(t *testing.T) {
		content := strings.Repeat("a", 520) + "payload"
		tool := newTool(func(string) (fs.File, error) {
			return &filesystemBehaviorFile{
				reader:  bytes.NewReader([]byte(content)),
				statErr: errors.New("stream size is unavailable"),
			}, nil
		})
		result := tool.Execute(context.Background(), map[string]any{
			"path": "stream.txt", "offset": 520, "length": 7,
		})
		if result == nil || result.IsError || !strings.Contains(result.ForLLM, "payload") ||
			!strings.Contains(result.ForLLM, "total size unknown") {
			t.Fatalf("non-seekable read result = %#v", result)
		}
	})

	t.Run("non-seekable short stream", func(t *testing.T) {
		tool := newTool(func(string) (fs.File, error) {
			return &filesystemBehaviorFile{
				reader: bytes.NewReader([]byte("short stream")), info: info,
			}, nil
		})
		result := tool.Execute(context.Background(), map[string]any{
			"path": "stream.txt", "offset": 600,
		})
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "advance to offset") {
			t.Fatalf("short-stream result = %#v", result)
		}
	})

	t.Run("content read", func(t *testing.T) {
		tool := newTool(func(string) (fs.File, error) {
			return &filesystemBehaviorFile{
				first:       []byte(strings.Repeat("a", 512)),
				terminalErr: errFilesystemBehaviorRead,
				info:        info,
			}, nil
		})
		result := tool.Execute(context.Background(), map[string]any{"path": "stream.txt"})
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "read file content") {
			t.Fatalf("content-read result = %#v", result)
		}
	})
}

func TestReadFileLinesReportsStreamingFailures(t *testing.T) {
	info := filesystemBehaviorRegularInfo(t)
	newTool := func(fileFactory func() fs.File) *ReadFileLinesTool {
		tool := NewReadFileLinesTool("", false, 128)
		tool.fs = &filesystemBehaviorFS{open: func(string) (fs.File, error) {
			return fileFactory(), nil
		}}
		return tool
	}
	assertReadError := func(t *testing.T, result *ToolResult, fragment string) {
		t.Helper()
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, fragment) {
			t.Fatalf("streaming read result = %#v; want %q", result, fragment)
		}
	}

	t.Run("initial sample", func(t *testing.T) {
		tool := newTool(func() fs.File {
			return &filesystemBehaviorFile{
				readCalls: 1, terminalErr: errFilesystemBehaviorRead, info: info,
			}
		})
		assertReadError(t, tool.Execute(context.Background(), map[string]any{
			"path": "stream.txt",
		}), "failed to read file")
	})

	t.Run("skipped line", func(t *testing.T) {
		tool := newTool(func() fs.File {
			return &filesystemBehaviorFile{
				first: []byte("partial"), terminalErr: errFilesystemBehaviorRead, info: info,
			}
		})
		assertReadError(t, tool.Execute(context.Background(), map[string]any{
			"path": "stream.txt", "start_line": 2,
		}), "read file content")
	})

	t.Run("selected line", func(t *testing.T) {
		tool := newTool(func() fs.File {
			return &filesystemBehaviorFile{
				first: []byte("partial"), terminalErr: errFilesystemBehaviorRead, info: info,
			}
		})
		assertReadError(t, tool.Execute(context.Background(), map[string]any{
			"path": "stream.txt",
		}), "read file content")
	})

	t.Run("remaining content", func(t *testing.T) {
		tool := newTool(func() fs.File {
			return &filesystemBehaviorFile{
				first: []byte("first\n"), terminalErr: errFilesystemBehaviorRead, info: info,
			}
		})
		assertReadError(t, tool.Execute(context.Background(), map[string]any{
			"path": "stream.txt", "max_lines": 1,
		}), "inspect remaining file content")
	})
}

func TestReadFileLinesHandlesLargeSkippedLinesAndByteBudget(t *testing.T) {
	workspace := t.TempDir()
	largePath := filepath.Join(workspace, "large.txt")
	if err := os.WriteFile(
		largePath,
		[]byte(strings.Repeat("x", 40<<10)+"\nselected\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	tool := NewReadFileLinesTool(workspace, true, MaxReadFileSize)
	result := tool.Execute(context.Background(), map[string]any{
		"path": largePath, "start_line": 2,
	})
	if result == nil || result.IsError || !strings.Contains(result.ForLLM, "2|selected") {
		t.Fatalf("large skipped-line result = %#v", result)
	}

	budgetPath := filepath.Join(workspace, "budget.txt")
	if err := os.WriteFile(budgetPath, []byte("a\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool = NewReadFileLinesTool(workspace, true, 4)
	result = tool.Execute(context.Background(), map[string]any{
		"path": budgetPath, "max_lines": 10,
	})
	if result == nil || result.IsError || !strings.Contains(result.ForLLM, "byte budget reached") ||
		!strings.Contains(result.ForLLM, "max_lines=10") {
		t.Fatalf("byte-budget result = %#v", result)
	}
}

func TestFilesystemToolsReportHostAndSandboxFailures(t *testing.T) {
	t.Run("host read missing", func(t *testing.T) {
		tool := NewEditFileTool("", false)
		result := tool.Execute(context.Background(), map[string]any{
			"path":     filepath.Join(t.TempDir(), "missing.txt"),
			"old_text": "before",
			"new_text": "after",
		})
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "file not found") {
			t.Fatalf("host missing-read result = %#v", result)
		}
	})

	t.Run("host open generic", func(t *testing.T) {
		tool := NewReadFileTool("", false, 16)
		result := tool.Execute(context.Background(), map[string]any{
			"path": strings.Repeat("x", 5000),
		})
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "failed to open file") {
			t.Fatalf("host generic-open result = %#v", result)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("host open permission", func(t *testing.T) {
			requireEnforcedReadMode(t)
			path := filepath.Join(t.TempDir(), "private.txt")
			if err := os.WriteFile(path, []byte("private"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
			result := NewReadFileTool("", false, 16).Execute(
				context.Background(),
				map[string]any{"path": path},
			)
			if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "access denied") {
				t.Fatalf("host permission result = %#v", result)
			}
		})
	}

	t.Run("missing sandbox workspace", func(t *testing.T) {
		workspace := filepath.Join(t.TempDir(), "removed")
		tool := NewReadFileTool(workspace, true, 16)
		result := tool.Execute(context.Background(), map[string]any{"path": "file.txt"})
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "open workspace") {
			t.Fatalf("missing-workspace result = %#v", result)
		}
	})

	t.Run("sandbox read missing", func(t *testing.T) {
		workspace := t.TempDir()
		tool := NewEditFileTool(workspace, true)
		result := tool.Execute(context.Background(), map[string]any{
			"path": "missing.txt", "old_text": "before", "new_text": "after",
		})
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "file not found") {
			t.Fatalf("sandbox missing-read result = %#v", result)
		}
	})

	t.Run("sandbox read escape", func(t *testing.T) {
		workspace := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.txt")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		tool := NewEditFileTool(workspace, true)
		result := tool.Execute(context.Background(), map[string]any{
			"path": "escape", "old_text": "outside", "new_text": "changed",
		})
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "access denied") {
			t.Fatalf("sandbox escape result = %#v", result)
		}
	})

	t.Run("sandbox parent is file", func(t *testing.T) {
		workspace := t.TempDir()
		if err := os.WriteFile(filepath.Join(workspace, "parent"), []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		result := NewWriteFileTool(workspace, true).Execute(
			context.Background(),
			map[string]any{
				"path": "parent/child.txt", "content": "content", "overwrite": true,
			},
		)
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "parent directories") {
			t.Fatalf("sandbox parent result = %#v", result)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("sandbox temp permission", func(t *testing.T) {
			requireEnforcedDirectoryWriteMode(t)
			workspace := t.TempDir()
			if err := os.Chmod(workspace, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(workspace, 0o700) })
			result := NewWriteFileTool(workspace, true).Execute(
				context.Background(),
				map[string]any{"path": "new.txt", "content": "content", "overwrite": true},
			)
			if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "open temp file") {
				t.Fatalf("sandbox temp-permission result = %#v", result)
			}
		})
	}

	t.Run("sandbox rename over directory", func(t *testing.T) {
		workspace := t.TempDir()
		target := filepath.Join(workspace, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "child"), []byte("child"), 0o600); err != nil {
			t.Fatal(err)
		}
		result := NewWriteFileTool(workspace, true).Execute(
			context.Background(),
			map[string]any{"path": "target", "content": "content", "overwrite": true},
		)
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "rename temp file") {
			t.Fatalf("sandbox rename result = %#v", result)
		}
	})

	t.Run("sandbox open symlink loop", func(t *testing.T) {
		workspace := t.TempDir()
		if err := os.Symlink("loop-b", filepath.Join(workspace, "loop-a")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := os.Symlink("loop-a", filepath.Join(workspace, "loop-b")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		result := NewReadFileTool(workspace, true, 16).Execute(
			context.Background(),
			map[string]any{"path": "loop-a"},
		)
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "failed to open file") {
			t.Fatalf("sandbox symlink-loop result = %#v", result)
		}
	})
}

func TestFilesystemPublicPathChecksRejectUnresolvedAuthority(t *testing.T) {
	if _, err := ValidatePathWithAllowPaths("file.txt", "", true, nil); err == nil ||
		!strings.Contains(err.Error(), "workspace is not defined") {
		t.Fatalf("empty-workspace validation error = %v", err)
	}

	candidate := filepath.Join(t.TempDir(), "candidate")
	if IsAllowedPath(candidate, []*regexp.Regexp{regexp.MustCompile("no-match")}) {
		t.Fatal("unanchored unmatched pattern allowed a path")
	}
	if IsAllowedPath(candidate, []*regexp.Regexp{regexp.MustCompile(`^/never.*$`)}) {
		t.Fatal("operator-bearing unmatched pattern allowed a path")
	}
	if IsAllowedPath(candidate, []*regexp.Regexp{regexp.MustCompile(`^relative$`)}) {
		t.Fatal("relative anchored pattern allowed an absolute path")
	}
	if IsAllowedPath(candidate, []*regexp.Regexp{regexp.MustCompile(`^(?:\\|$)`)}) {
		t.Fatal("empty platform-root pattern allowed an absolute path")
	}

	workspace := t.TempDir()
	if err := os.Symlink("loop-b", filepath.Join(workspace, "loop-a")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("loop-a", filepath.Join(workspace, "loop-b")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	loop := filepath.Join(workspace, "loop-a")
	if IsAllowedPath(loop, []*regexp.Regexp{regexp.MustCompile(`.*`)}) {
		t.Fatal("unresolvable matching path was allowed")
	}
}

func TestReadFileLinesUsesDefaultAndExactBufferBoundary(t *testing.T) {
	workspace := t.TempDir()
	defaulted := NewReadFileLinesTool(workspace, true, 0)
	if defaulted.maxSize != MaxReadFileSize {
		t.Fatalf("default line-reader size = %d", defaulted.maxSize)
	}

	path := filepath.Join(workspace, "buffer-boundary.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 32<<10)+"z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewReadFileLinesTool(workspace, true, (32<<10)+2)
	result := tool.Execute(context.Background(), map[string]any{"path": path})
	if result == nil || result.IsError || !strings.Contains(result.ForLLM, "cut mid-line") {
		t.Fatalf("exact-buffer-boundary result = %#v", result)
	}
}

func TestFilesystemValidationReportsRemovedWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit removing the current directory")
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	removed := filepath.Join(parent, "removed")
	if err := os.Mkdir(removed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(removed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	_, validationErr := ValidatePathWithAllowPaths("file.txt", "relative-workspace", true, nil)
	if validationErr == nil || !strings.Contains(validationErr.Error(), "resolve workspace path") {
		t.Fatalf("removed-working-directory validation error = %v", validationErr)
	}
	if err := os.Chdir(original); err != nil {
		t.Fatal(err)
	}
}

func TestEditAndAppendToolsRouteAllowedExternalFiles(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	editPath := filepath.Join(outside, "edit.txt")
	appendPath := filepath.Join(outside, "append.txt")
	for path, content := range map[string]string{editPath: "before", appendPath: "before"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	separator := regexp.QuoteMeta(string(os.PathSeparator))
	patterns := []*regexp.Regexp{regexp.MustCompile(
		"^" + regexp.QuoteMeta(outside) + "(?:" + separator + "|$)",
	)}
	editResult := NewEditFileTool(workspace, true, patterns).Execute(
		context.Background(),
		map[string]any{
			"path": editPath, "old_text": "before", "new_text": "after",
		},
	)
	if editResult == nil || editResult.IsError {
		t.Fatalf("allowed external edit result = %#v", editResult)
	}
	appendResult := NewAppendFileTool(workspace, true, patterns).Execute(
		context.Background(),
		map[string]any{"path": appendPath, "content": "-after"},
	)
	if appendResult == nil || appendResult.IsError {
		t.Fatalf("allowed external append result = %#v", appendResult)
	}
	for path, want := range map[string]string{editPath: "after", appendPath: "before-after"} {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != want {
			t.Fatalf("allowed external file %q = %q, %v", filepath.Base(path), content, err)
		}
	}
}

func TestEditToolReportsWriteFailureAfterSuccessfulRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file modes do not provide this write-denial contract")
	}
	requireEnforcedDirectoryWriteMode(t)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "edit.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workspace, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(workspace, 0o700) })
	result := NewEditFileTool(workspace, true).Execute(
		context.Background(),
		map[string]any{"path": "edit.txt", "old_text": "before", "new_text": "after"},
	)
	if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "open temp file") {
		t.Fatalf("edit write-failure result = %#v", result)
	}
}

func requireEnforcedDirectoryWriteMode(t *testing.T) {
	t.Helper()
	probeRoot := t.TempDir()
	if err := os.Chmod(probeRoot, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(probeRoot, 0o700) })
	probe := filepath.Join(probeRoot, "write-mode-probe")
	err := os.WriteFile(probe, []byte("probe"), 0o600)
	if err == nil {
		_ = os.Remove(probe)
		t.Skip("process privileges bypass Unix directory write mode bits")
	}
	if !os.IsPermission(err) {
		t.Fatalf("write-mode capability probe failed unexpectedly: %v", err)
	}
}

func TestMediaFileToolsReportPathAndRegistrationFailures(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(workspace, "image.png")
	if err := os.WriteFile(image, []byte("\x89PNG\r\n\x1a\nimage"), 0o600); err != nil {
		t.Fatal(err)
	}

	load := NewLoadImageTool(workspace, true, 1024, rejectingPublicMediaStore{})
	load.SetContext("test-channel", "test-chat")
	for name, test := range map[string]struct {
		path string
		want string
	}{
		"outside workspace": {path: outside, want: "invalid path"},
		"missing":           {path: filepath.Join(workspace, "missing.png"), want: "file not found"},
		"directory":         {path: workspace, want: "path is a directory"},
		"registration":      {path: image, want: "failed to register image"},
	} {
		t.Run("load-"+name, func(t *testing.T) {
			result := load.Execute(context.Background(), map[string]any{"path": test.path})
			if result == nil || !result.IsError || !strings.Contains(result.ForLLM, test.want) {
				t.Fatalf("load image %s result = %#v", name, result)
			}
		})
	}

	send := NewSendFileTool(workspace, true, 1024, rejectingPublicMediaStore{})
	send.SetContext("test-channel", "test-chat")
	for name, test := range map[string]struct {
		path string
		want string
	}{
		"outside workspace": {path: outside, want: "invalid path"},
		"missing":           {path: filepath.Join(workspace, "missing.txt"), want: "file not found"},
		"registration":      {path: image, want: "failed to register media"},
	} {
		t.Run("send-"+name, func(t *testing.T) {
			result := send.Execute(context.Background(), map[string]any{"path": test.path})
			if result == nil || !result.IsError || !strings.Contains(result.ForLLM, test.want) {
				t.Fatalf("send file %s result = %#v", name, result)
			}
		})
	}
}

func filesystemBehaviorRegularInfo(t *testing.T) os.FileInfo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
