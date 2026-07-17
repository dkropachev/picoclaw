package workflows

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Definition struct {
	Ref   string `json:"ref"`
	Path  string `json:"path"`
	Name  string `json:"name,omitempty"`
	Error string `json:"error,omitempty"`
}

func ListLocal(ctx context.Context, workspace string) ([]Definition, error) {
	root := filepath.Join(workspace, "workflows")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	_ = entries

	var defs []Definition
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yml" && ext != ".yaml" {
			return nil
		}
		rel, err := filepath.Rel(workspace, path)
		if err != nil {
			return err
		}
		ref := filepath.ToSlash(rel)
		def := Definition{Ref: ref, Path: path}
		if resolved, err := (Resolver{WorkspaceDir: workspace}).ResolveLocal(ref); err == nil {
			def.Ref = resolved.Canonical
			def.Path = resolved.Path
		} else {
			def.Error = err.Error()
		}
		if def.Error == "" {
			workflow, err := LoadLocal(ctx, workspace, def.Ref)
			if err != nil {
				def.Error = err.Error()
			} else {
				def.Name = workflow.Name
			}
		}
		defs = append(defs, def)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Ref < defs[j].Ref
	})
	return defs, nil
}

func LoadLocal(ctx context.Context, workspace, ref string) (*Workflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, err := (Resolver{WorkspaceDir: workspace}).ResolveLocal(ref)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(resolved.Path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}
