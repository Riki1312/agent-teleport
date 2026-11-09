package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

// canonicalWorkspace resolves existing symlinks without creating a workspace.
// Resolve the existing ancestor first when the workspace has not been created yet.
func canonicalWorkspace(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	current := absolute
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			info, err := os.Stat(resolved)
			if err != nil {
				return "", fmt.Errorf("stat workspace: %w", err)
			}
			if !info.IsDir() {
				return "", fmt.Errorf("workspace ancestor %q is not a directory", current)
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve workspace %q: %w", path, err)
		}
		if info, statErr := os.Lstat(current); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("workspace %q contains a broken symlink", path)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve workspace %q: %w", path, err)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

// currentWorkspace returns the canonical current directory.
func currentWorkspace() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve current workspace: %w", err)
	}
	return canonicalWorkspace(cwd)
}

// targetWorkspace returns the canonical import workspace: path when given,
// otherwise the current directory.
func targetWorkspace(path string) (string, error) {
	if path == "" {
		return currentWorkspace()
	}
	return canonicalWorkspace(path)
}
