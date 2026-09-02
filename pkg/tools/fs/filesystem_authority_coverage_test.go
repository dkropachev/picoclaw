package fstools

import (
	"context"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

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
