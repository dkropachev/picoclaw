package localci

import (
	"fmt"
	"io"
	"path/filepath"
)

func readDiscoveryFile(root, relative string, limit int64) ([]byte, error) {
	if !validLocalDirectory(filepath.ToSlash(filepath.Dir(relative))) ||
		!filepath.IsLocal(filepath.FromSlash(relative)) {
		return nil, fmt.Errorf("%w: invalid discovery input path", ErrInvalid)
	}
	file, err := openDiscoveryFile(root, filepath.FromSlash(relative))
	if err != nil {
		return nil, fmt.Errorf("open local CI input %s: %w", relative, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, fmt.Errorf("%w: local CI input %s is not a bounded regular file", ErrInvalid, relative)
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read local CI input %s: %w", relative, err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%w: local CI input %s exceeds its byte limit", ErrInvalid, relative)
	}
	return raw, nil
}
