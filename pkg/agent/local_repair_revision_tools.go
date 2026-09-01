package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	// Revision-aware edits cannot target a file larger than the editable-file
	// cap, so hashing a larger read-only file would add cost without granting a
	// usable compare fence.
	maxLocalRepairRevisionFile        = maxLocalRepairEditableFile
	maxLocalRepairRevisionBytesPerRun = 64 << 20
)

var errLocalRepairStaleRevision = errors.New("local repair edit revision is stale")

type localRepairRevisionReadTool struct {
	delegate        tools.Tool
	guard           *localRepairPathGuard
	constructionErr error
	revisionBytes   atomic.Int64
}

func newLocalRepairRevisionReadTool(guard *localRepairPathGuard) tools.Tool {
	delegate, err := tools.NewReadFileLinesToolWithPolicy(
		guard.root,
		true,
		tools.MaxReadFileSize,
		guard.fileMutationPolicy(),
	)
	return &localRepairRevisionReadTool{
		delegate:        delegate,
		guard:           guard,
		constructionErr: err,
	}
}

func (tool *localRepairRevisionReadTool) Name() string { return "read_file" }

func (tool *localRepairRevisionReadTool) Description() string {
	return tool.delegate.Description() +
		" Every successful read also returns revision_sha256 for the whole file; use it as expected_revision for a line-range edit."
}

func (tool *localRepairRevisionReadTool) Parameters() map[string]any {
	return tool.delegate.Parameters()
}

func (tool *localRepairRevisionReadTool) Execute(
	ctx context.Context,
	args map[string]any,
) *tools.ToolResult {
	if tool == nil || tool.delegate == nil || tool.guard == nil || tool.constructionErr != nil {
		return tools.ErrorResult("local repair read tool is unavailable")
	}
	path, ok := args["path"].(string)
	if !ok {
		return tools.ErrorResult("path is required")
	}
	before, err := localRepairFileRevision(tool.guard, path, &tool.revisionBytes)
	if err != nil {
		return tools.ErrorResult("failed to calculate whole-file revision")
	}
	result := tool.delegate.Execute(ctx, args)
	if result == nil || result.IsError || result.Err != nil {
		return result
	}
	after, err := localRepairFileRevision(tool.guard, path, &tool.revisionBytes)
	if err != nil || before != after {
		return tools.ErrorResult("file changed during read; reread the affected range")
	}
	result.ForLLM = "[revision_sha256: " + before + "]\n" + result.ForLLM
	return result
}

type localRepairRevisionEditTool struct {
	guard *localRepairPathGuard
}

func newLocalRepairRevisionEditTool(guard *localRepairPathGuard) tools.Tool {
	return &localRepairRevisionEditTool{guard: guard}
}

func (tool *localRepairRevisionEditTool) Name() string { return "edit_file" }

func (*localRepairRevisionEditTool) Description() string {
	return "Edit one existing file using exactly one mode: replace one unique exact old_text, or replace an inclusive start_line/end_line range after supplying the whole-file expected_revision returned by read_file. new_text is always literal and must include any desired line endings."
}

func (*localRepairRevisionEditTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Repository-relative path of the existing file to edit.",
			},
			"old_text": map[string]any{
				"type":        "string",
				"description": "Exact unique text for exact-replacement mode.",
			},
			"start_line": map[string]any{
				"type":        "integer",
				"description": "First line to replace (1-indexed, inclusive) in line-range mode.",
			},
			"end_line": map[string]any{
				"type":        "integer",
				"description": "Last line to replace (1-indexed, inclusive) in line-range mode.",
			},
			"expected_revision": map[string]any{
				"type":        "string",
				"description": "Whole-file revision_sha256 returned by read_file for line-range mode.",
			},
			"new_text": map[string]any{
				"type":        "string",
				"description": "Literal replacement bytes after JSON decoding, including any desired line endings.",
			},
		},
		"required":             []string{"path", "new_text"},
		"additionalProperties": false,
	}
}

func (tool *localRepairRevisionEditTool) Execute(
	ctx context.Context,
	args map[string]any,
) *tools.ToolResult {
	if tool == nil || tool.guard == nil {
		return tools.ErrorResult("local repair edit tool is unavailable")
	}
	path, pathOK := args["path"].(string)
	newText, newTextOK := args["new_text"].(string)
	if !pathOK {
		return tools.ErrorResult("path is required")
	}
	if !newTextOK {
		return tools.ErrorResult("new_text is required")
	}
	if len(newText) > maxLocalRepairEditArgument || !utf8.ValidString(newText) {
		return tools.ErrorResult("edit content is invalid or too large")
	}
	_, oldMode := args["old_text"]
	_, hasStart := args["start_line"]
	_, hasEnd := args["end_line"]
	_, hasRevision := args["expected_revision"]
	rangeMode := hasStart || hasEnd || hasRevision
	if oldMode == rangeMode {
		return tools.ErrorResult("exact and line-range edit modes are exclusive")
	}

	resolved, err := tool.guard.validate(path, true)
	if err != nil {
		return tools.ErrorResult("path is denied")
	}
	before, mode, err := readLocalRepairEditableFile(tool.guard, path, resolved)
	if err != nil {
		return tools.ErrorResult("failed to read editable file")
	}

	var after []byte
	if oldMode {
		if !localRepairOnlyArguments(args, "path", "old_text", "new_text") {
			return tools.ErrorResult("exact edit arguments are invalid")
		}
		oldText, ok := args["old_text"].(string)
		if !ok || oldText == "" || len(oldText) > maxLocalRepairEditArgument ||
			!utf8.ValidString(oldText) {
			return tools.ErrorResult("edit content is invalid or too large")
		}
		after, err = replaceLocalRepairExact(before, []byte(oldText), []byte(newText))
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
	} else {
		if !localRepairOnlyArguments(
			args,
			"path",
			"start_line",
			"end_line",
			"expected_revision",
			"new_text",
		) {
			return tools.ErrorResult("line-range edit arguments are invalid")
		}
		startLine, startErr := localRepairIntegerArgument(args, "start_line")
		endLine, endErr := localRepairIntegerArgument(args, "end_line")
		expected, expectedOK := args["expected_revision"].(string)
		if startErr != nil || endErr != nil || startLine < 1 || endLine < startLine ||
			!expectedOK || !validLocalRepairOpaqueDigest(expected) {
			return tools.ErrorResult("line-range edit arguments are invalid")
		}
		actual := sha256.Sum256(before)
		if expected != hex.EncodeToString(actual[:]) {
			return tools.ErrorResult("stale revision; reread the affected range before editing")
		}
		after, err = replaceLocalRepairLineRange(
			before,
			startLine,
			endLine,
			[]byte(newText),
		)
		if err != nil {
			return tools.ErrorResult(err.Error())
		}
	}
	if err := ctx.Err(); err != nil {
		return tools.ErrorResult("local repair was canceled")
	}
	beforeDigest := sha256.Sum256(before)
	if err := writeLocalRepairEditableFile(
		tool.guard,
		path,
		after,
		mode,
		hex.EncodeToString(beforeDigest[:]),
	); errors.Is(err, errLocalRepairStaleRevision) {
		return tools.ErrorResult("stale revision; reread the affected range before editing")
	} else if err != nil {
		return tools.ErrorResult("failed to write editable file")
	}
	return tools.DiffResult(path, before, after)
}

func localRepairFileRevision(
	guard *localRepairPathGuard,
	path string,
	revisionBytes *atomic.Int64,
) (string, error) {
	if guard == nil {
		return "", errors.New("path guard is unavailable")
	}
	resolved, err := guard.validate(path, false)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(guard.root)
	if err != nil {
		return "", err
	}
	defer root.Close()
	relative := filepath.ToSlash(path)
	info, err := root.Stat(relative)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 ||
		info.Size() > maxLocalRepairRevisionFile {
		return "", errors.New("revision source is invalid or too large")
	}
	if !reserveLocalRepairRevisionBytes(revisionBytes, info.Size()) {
		return "", errors.New("revision byte budget is exhausted")
	}
	if guard.beforeFileOpen != nil {
		guard.beforeFileOpen()
	}
	file, err := root.Open(relative)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if validateErr := guard.validateOpenedRegular(
		filepath.Join(guard.root, path),
		resolved,
		info,
		file,
	); validateErr != nil {
		return "", validateErr
	}
	digest := sha256.New()
	written, err := io.Copy(digest, io.LimitReader(file, maxLocalRepairRevisionFile+1))
	if err != nil || written != info.Size() || written > maxLocalRepairRevisionFile {
		return "", errors.New("revision source changed during hashing")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func reserveLocalRepairRevisionBytes(counter *atomic.Int64, size int64) bool {
	if counter == nil || size < 0 || size > maxLocalRepairRevisionBytesPerRun {
		return false
	}
	for {
		current := counter.Load()
		if current < 0 || current > maxLocalRepairRevisionBytesPerRun-size {
			return false
		}
		if counter.CompareAndSwap(current, current+size) {
			return true
		}
	}
}

func readLocalRepairEditableFile(
	guard *localRepairPathGuard,
	path string,
	resolved string,
) ([]byte, os.FileMode, error) {
	if guard == nil || resolved == "" {
		return nil, 0, errors.New("editable path is unavailable")
	}
	root, err := os.OpenRoot(guard.root)
	if err != nil {
		return nil, 0, err
	}
	defer root.Close()
	relative := filepath.ToSlash(path)
	info, err := root.Lstat(relative)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 0 || info.Size() > maxLocalRepairEditableFile {
		return nil, 0, errors.New("editable file is invalid or too large")
	}
	if guard.beforeFileOpen != nil {
		guard.beforeFileOpen()
	}
	file, err := root.Open(relative)
	if err != nil {
		return nil, 0, errors.New("editable file changed during read")
	}
	defer file.Close()
	if err = guard.validateOpenedRegular(
		filepath.Join(guard.root, path),
		resolved,
		info,
		file,
	); err != nil {
		return nil, 0, errors.New("editable file changed during read")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxLocalRepairEditableFile+1))
	afterInfo, statErr := file.Stat()
	if err != nil || int64(len(content)) != info.Size() ||
		statErr != nil || afterInfo.Mode() != info.Mode() || !os.SameFile(info, afterInfo) {
		return nil, 0, errors.New("editable file changed during read")
	}
	return content, info.Mode().Perm(), nil
}

var localRepairTemporarySequence atomic.Uint64

func writeLocalRepairEditableFile(
	guard *localRepairPathGuard,
	path string,
	content []byte,
	mode os.FileMode,
	expectedRevision string,
) error {
	if guard == nil || len(content) > maxLocalRepairEditableFile || mode&os.ModeType != 0 ||
		!validLocalRepairOpaqueDigest(expectedRevision) {
		return errors.New("editable write is invalid")
	}
	root, err := os.OpenRoot(guard.root)
	if err != nil {
		return err
	}
	defer root.Close()
	relative := filepath.ToSlash(path)
	directory := filepath.ToSlash(filepath.Dir(path))
	if directory == "" {
		directory = "."
	}
	pathDigest := sha256.Sum256([]byte(relative))
	for range 8 {
		sequence := localRepairTemporarySequence.Add(1)
		temporaryBase := fmt.Sprintf(
			".picoclaw-repair-%x-%d-%d",
			pathDigest[:8],
			os.Getpid(),
			sequence,
		)
		temporary := temporaryBase
		if directory != "." {
			temporary = directory + "/" + temporaryBase
		}
		file, openErr := root.OpenFile(
			temporary,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			mode,
		)
		if errors.Is(openErr, os.ErrExist) {
			continue
		}
		if openErr != nil {
			return openErr
		}
		cleanup := true
		defer func() {
			if cleanup {
				_ = root.Remove(temporary)
			}
		}()
		if _, err = file.Write(content); err == nil {
			err = file.Chmod(mode)
		}
		if err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		currentRevision, revisionErr := localRepairRootFileRevision(
			guard,
			root,
			relative,
			maxLocalRepairEditableFile,
		)
		if revisionErr != nil || currentRevision != expectedRevision {
			return errLocalRepairStaleRevision
		}
		if err = root.Rename(temporary, relative); err != nil {
			return err
		}
		cleanup = false
		// The manager-owned pinned operation and shared repair reservation lock
		// exclude other authorized checkout mutations. The immediate rehash above
		// is a defense-in-depth compare fence for uncoordinated writers; no
		// portable filesystem primitive can combine content comparison and rename.
		if runtime.GOOS == "windows" {
			return nil
		}
		directoryFile, openDirectoryErr := root.Open(directory)
		if openDirectoryErr != nil {
			return openDirectoryErr
		}
		syncErr := directoryFile.Sync()
		closeErr := directoryFile.Close()
		return errors.Join(syncErr, closeErr)
	}
	return errors.New("unable to reserve an edit temporary file")
}

func localRepairRootFileRevision(
	guard *localRepairPathGuard,
	root *os.Root,
	path string,
	maximum int64,
) (string, error) {
	if guard == nil || root == nil || maximum < 1 {
		return "", errors.New("revision source is unavailable")
	}
	info, err := root.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 0 || info.Size() > maximum {
		return "", errors.New("revision source is invalid or too large")
	}
	if guard.beforeFileOpen != nil {
		guard.beforeFileOpen()
	}
	file, err := root.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	candidate := filepath.Join(guard.root, filepath.FromSlash(path))
	resolved, err := resolveLocalRepairPath(candidate)
	if err != nil || guard.validateOpenedRegular(candidate, resolved, info, file) != nil {
		return "", errors.New("revision source changed during hashing")
	}
	digest := sha256.New()
	written, err := io.Copy(digest, io.LimitReader(file, maximum+1))
	if err != nil || written != info.Size() || written > maximum {
		return "", errors.New("revision source changed during hashing")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func replaceLocalRepairExact(content, oldText, newText []byte) ([]byte, error) {
	count := bytes.Count(content, oldText)
	if count == 0 {
		return nil, errors.New("old_text not found in file; make sure it matches exactly")
	}
	if count != 1 {
		return nil, fmt.Errorf(
			"old_text appears %d times; provide more context or use a revision-aware line range",
			count,
		)
	}
	return bytes.Replace(content, oldText, newText, 1), nil
}

func replaceLocalRepairLineRange(
	content []byte,
	startLine int64,
	endLine int64,
	newText []byte,
) ([]byte, error) {
	if startLine < 1 || endLine < startLine {
		return nil, errors.New("line range is invalid")
	}
	starts := []int{0}
	for index, value := range content {
		if value == '\n' && index+1 < len(content) {
			starts = append(starts, index+1)
		}
	}
	if len(content) == 0 || startLine > int64(len(starts)) || endLine > int64(len(starts)) {
		return nil, errors.New("line range is outside the file")
	}
	startOffset := starts[startLine-1]
	endOffset := len(content)
	if endLine < int64(len(starts)) {
		endOffset = starts[endLine]
	}
	after := make([]byte, 0, len(content)-(endOffset-startOffset)+len(newText))
	after = append(after, content[:startOffset]...)
	after = append(after, newText...)
	after = append(after, content[endOffset:]...)
	return after, nil
}

func localRepairOnlyArguments(args map[string]any, allowed ...string) bool {
	if len(args) != len(allowed) {
		return false
	}
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range args {
		if _, ok := set[key]; !ok {
			return false
		}
	}
	return true
}

func localRepairIntegerArgument(args map[string]any, name string) (int64, error) {
	raw, ok := args[name]
	if !ok {
		return 0, errors.New("integer argument is missing")
	}
	switch value := raw.(type) {
	case json.Number:
		return strconv.ParseInt(string(value), 10, 64)
	case int:
		return int64(value), nil
	case int64:
		return value, nil
	case float64:
		if value != math.Trunc(value) || value < math.MinInt64 || value > math.MaxInt64 {
			return 0, errors.New("integer argument is invalid")
		}
		return int64(value), nil
	default:
		return 0, errors.New("integer argument is invalid")
	}
}

func cloneLocalRepairToolArguments(args map[string]any) map[string]any {
	cloned := make(map[string]any, len(args)+1)
	for key, value := range args {
		cloned[key] = value
	}
	return cloned
}

func localRepairOptionalListParameters(parameters map[string]any) map[string]any {
	cloned := make(map[string]any, len(parameters))
	for key, value := range parameters {
		cloned[key] = value
	}
	delete(cloned, "required")
	return cloned
}

func localRepairRevisionFromResult(value string) string {
	const prefix = "[revision_sha256: "
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	revision, _, found := strings.Cut(strings.TrimPrefix(value, prefix), "]")
	if !found || !validLocalRepairOpaqueDigest(revision) {
		return ""
	}
	return revision
}
