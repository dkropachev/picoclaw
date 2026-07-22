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

type LocalOption func(*localOptions)

type localOptions struct {
	DefinitionsDir string
}

func WithDefinitionsDir(dir string) LocalOption {
	return func(opts *localOptions) {
		opts.DefinitionsDir = dir
	}
}

func collectLocalOptions(opts ...LocalOption) localOptions {
	out := localOptions{DefinitionsDir: DefaultDefinitionsDir}
	for _, opt := range opts {
		if opt != nil {
			opt(&out)
		}
	}
	return out
}

func (opts localOptions) resolver(workspace string) Resolver {
	return Resolver{WorkspaceDir: workspace, DefinitionsDir: opts.DefinitionsDir}
}

func (opts localOptions) definitionsRoot(workspace string) (string, error) {
	dir, err := cleanDefinitionsDir(opts.DefinitionsDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(workspace, dir), nil
}

func ListLocal(ctx context.Context, workspace string, opts ...LocalOption) ([]Definition, error) {
	local := collectLocalOptions(opts...)
	root, err := local.definitionsRoot(workspace)
	if err != nil {
		return nil, err
	}
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
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		ref := DefaultDefinitionsDir + "/" + filepath.ToSlash(rel)
		def := Definition{Ref: ref, Path: path}
		if resolved, resolveErr := local.resolver(workspace).ResolveLocal(ref); resolveErr == nil {
			def.Ref = resolved.Canonical
			def.Path = resolved.Path
		} else {
			def.Error = resolveErr.Error()
		}
		if def.Error == "" {
			workflow, loadErr := LoadLocal(ctx, workspace, def.Ref, opts...)
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

func LoadLocal(ctx context.Context, workspace, ref string, opts ...LocalOption) (*Workflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	local := collectLocalOptions(opts...)
	resolved, err := local.resolver(workspace).ResolveLocal(ref)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(resolved.Path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}
