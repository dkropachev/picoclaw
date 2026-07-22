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
	if _, statErr := os.Stat(root); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, nil
		}
		return nil, statErr
	}

	var defs []Definition
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, entryErr error) error {
		if entryErr != nil {
			return entryErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yml" && ext != ".yaml" {
			return nil
		}
		rel, relErr := filepath.Rel(workspace, path)
		if relErr != nil {
			return relErr
		}
		ref := filepath.ToSlash(rel)
		def := Definition{Ref: ref, Path: path}
		if resolved, resolveErr := (Resolver{WorkspaceDir: workspace}).ResolveLocal(ref); resolveErr == nil {
			def.Ref = resolved.Canonical
			def.Path = resolved.Path
		} else {
			def.Error = resolveErr.Error()
		}
		if def.Error == "" {
			workflow, loadErr := LoadLocal(ctx, workspace, def.Ref)
			if loadErr != nil {
				def.Error = loadErr.Error()
			} else {
				def.Name = workflow.Name
			}
		}
		defs = append(defs, def)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
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
