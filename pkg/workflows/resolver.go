package workflows

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type Resolver struct {
	WorkspaceDir string
}

type ResolvedRef struct {
	Canonical string
	Path      string
}

func (r Resolver) ResolveLocal(ref string) (ResolvedRef, error) {
	canonical, err := CanonicalLocalRef(ref)
	if err != nil {
		return ResolvedRef{}, err
	}
	workspaceDir := strings.TrimSpace(r.WorkspaceDir)
	if workspaceDir == "" {
		workspaceDir = "."
	}
	root := filepath.Join(workspaceDir, "workflows")
	rel := strings.TrimPrefix(canonical, "workflows/")
	target := filepath.Join(root, filepath.FromSlash(rel))
	if err := ensureInsideWorkflowRoot(root, target); err != nil {
		return ResolvedRef{}, err
	}
	return ResolvedRef{Canonical: canonical, Path: target}, nil
}

func CanonicalLocalRef(ref string) (string, error) {
	ref = strings.TrimSpace(filepath.ToSlash(ref))
	if ref == "" {
		return "", fmt.Errorf("workflow ref is required")
	}
	if path.IsAbs(ref) || filepath.IsAbs(ref) {
		return "", fmt.Errorf("workflow ref %q must be relative", ref)
	}
	if strings.HasPrefix(ref, "./") {
		ref = strings.TrimPrefix(ref, "./")
	}
	for _, part := range strings.Split(ref, "/") {
		switch part {
		case "", ".", "..":
			return "", fmt.Errorf("workflow ref %q contains unsafe path component", ref)
		}
	}
	clean := path.Clean(ref)
	if !strings.HasPrefix(clean, "workflows/") {
		return "", fmt.Errorf("workflow ref %q must start with workflows/", ref)
	}
	ext := strings.ToLower(path.Ext(clean))
	if ext != ".yml" && ext != ".yaml" {
		return "", fmt.Errorf("workflow ref %q must end with .yml or .yaml", ref)
	}
	return clean, nil
}

func ensureInsideWorkflowRoot(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve workflow root: %w", err)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve workflow path: %w", err)
	}
	if info, statErr := os.Lstat(targetAbs); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		evalRoot, rootErr := filepath.EvalSymlinks(rootAbs)
		if rootErr != nil {
			return fmt.Errorf("resolve workflow root symlink: %w", rootErr)
		}
		evalTarget, targetErr := filepath.EvalSymlinks(targetAbs)
		if targetErr != nil {
			return fmt.Errorf("resolve workflow symlink: %w", targetErr)
		}
		rootAbs = evalRoot
		targetAbs = evalTarget
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return fmt.Errorf("compare workflow path: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return fmt.Errorf("workflow path escapes workflow root")
	}
	return nil
}

func IsLocalWorkflowRef(ref string) bool {
	_, err := CanonicalLocalRef(ref)
	return err == nil
}
