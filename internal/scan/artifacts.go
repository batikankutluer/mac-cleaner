package scan

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Artifact is a regenerable build or dependency folder inside a project that
// has not been touched for a long time: safe to delete before the move.
type Artifact struct {
	Path  string
	Bytes int64
}

// artifactRules: a folder with this name is an artifact only when one of the
// manifests that regenerates it sits next to it (nil: anywhere). dist and
// build are deliberately absent; some repositories commit them.
var artifactRules = map[string][]string{
	"node_modules":  {"package.json"},
	".next":         {"package.json"},
	".nuxt":         {"package.json"},
	".svelte-kit":   {"package.json"},
	".turbo":        {"package.json"},
	".parcel-cache": {"package.json"},
	"target":        {"Cargo.toml", "pom.xml"},
	".venv":         {"pyproject.toml", "requirements.txt", "setup.py"},
	"venv":          {"pyproject.toml", "requirements.txt", "setup.py"},
	"__pycache__":   nil,
	".pytest_cache": nil,
	".mypy_cache":   nil,
	".ruff_cache":   nil,
	"out":           {"foundry.toml"},
	"cache":         {"foundry.toml", "hardhat.config.js", "hardhat.config.ts"},
	"artifacts":     {"hardhat.config.js", "hardhat.config.ts"},
	".gradle":       {"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts"},
	"Pods":          {"Podfile"},
	".dart_tool":    {"pubspec.yaml"},
	"_build":        {"mix.exs"},
	"deps":          {"mix.exs"},
}

// IsArtifact reports whether dir is a regenerable folder by name and by the
// manifest next to it.

func IsArtifact(dir string) bool {
	needs, ok := artifactRules[filepath.Base(dir)]
	if !ok {
		return false
	}
	if needs == nil {
		return true
	}
	for _, m := range needs {
		if _, err := os.Lstat(filepath.Join(filepath.Dir(dir), m)); err == nil {
			return true
		}
	}
	return false
}

// gitAllowsDelete is false only when the folder sits in a git repository that
// does not ignore it, which means its contents may be tracked.
func gitAllowsDelete(dir string) bool {
	err := exec.Command("git", "-C", filepath.Dir(dir), "check-ignore", "-q", filepath.Base(dir)).Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true // ignored
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false // in a repository, not ignored
	default:
		return true // not a repository (exit 128), or no git at all
	}
}

// staleArtifacts keeps the candidates whose project has not changed since
// before, and that git does not track, and measures them.
func staleArtifacts(candidates []string, newest map[string]time.Time, before time.Time) []Artifact {
	var out []Artifact
	for _, c := range candidates {
		if t := newest[filepath.Dir(c)]; t.IsZero() || !t.Before(before) || !gitAllowsDelete(c) {
			continue
		}
		out = append(out, Artifact{Path: c, Bytes: treeSize(c)})
	}
	return out
}
