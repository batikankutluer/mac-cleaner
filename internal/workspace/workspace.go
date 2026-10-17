// Package workspace finds where the user keeps code projects: the directory
// under home with the most immediate children that look like projects.
package workspace

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// markerNames at the top of a folder mark it as a code project.
var markerNames = map[string]bool{
	".git": true, "go.mod": true, "package.json": true, "Cargo.toml": true, "pyproject.toml": true,
	"requirements.txt": true, "setup.py": true, "Makefile": true, "CMakeLists.txt": true, "pom.xml": true,
	"build.gradle": true, "build.gradle.kts": true, "Package.swift": true, "Gemfile": true, "composer.json": true,
	"Dockerfile": true, "docker-compose.yml": true, "docker-compose.yaml": true, "compose.yml": true, "compose.yaml": true, "deno.json": true, "pubspec.yaml": true,
	"mix.exs": true, "foundry.toml": true, "hardhat.config.js": true, "hardhat.config.ts": true,
}

var markerSuffixes = []string{".xcodeproj", ".xcworkspace", ".sln"}

// IsMarker reports whether an entry with this name marks its folder as a project.
func IsMarker(name string) bool {
	if markerNames[name] {
		return true
	}
	for _, s := range markerSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// Stack hints say what kind of project a folder is, beyond being one.
var (
	hintNames    = map[string]bool{"angular.json": true, "android": true, "ios": true, "pubspec.yaml": true}
	hintPrefixes = []string{"next.config.", "vite.config.", "nuxt.config.", "svelte.config.", "astro.config.", "truffle-config."}
)

// IsSignal reports whether a name is a project marker or a stack hint.
func IsSignal(name string) bool {
	if IsMarker(name) || hintNames[name] {
		return true
	}
	for _, p := range hintPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// dumpName matches folders that are piles of projects rather than one: the
// kind that should be unpacked so each project lands in its own group.
var dumpName = regexp.MustCompile(`^(?:(?:projects?|projeler|projelerim|repos?|repositories|github|gitlab|code|dev|workspace|sandbox)(?: \(\d+\))?$|d[uü]zenlenecek|to[-_ ]?(?:organi[sz]e|sort)|(?:eski|old)[-_ ]?(?:projeler|projects?))`)

// IsDump reports whether a folder name says it is a pile of projects.
func IsDump(name string) bool {
	return dumpName.MatchString(strings.ToLower(norm.NFC.String(name)))
}

// Markers returns the project markers among a folder's entries.
func Markers(entries []os.DirEntry) []string {
	var out []string
	for _, e := range entries {
		if IsMarker(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// Folders under home that never hold projects, or that macOS guards with
// permission prompts for no gain here.
var skipTop = map[string]bool{
	"Library": true, "Applications": true, "Movies": true, "Music": true, "Pictures": true,
	"Public": true, "Downloads": true, "Desktop": true,
}

const (
	maxParents  = 400
	maxChildren = 300
)

type Candidate struct {
	Path     string
	Projects int
}

// Detect looks at home's subfolders two levels deep and returns those with at
// least two project children, most projects first.
func Detect(home string) []Candidate {
	var parents []string
	top, _ := os.ReadDir(home)
	for _, e := range top {
		if !visibleDir(e) || skipTop[e.Name()] {
			continue
		}
		p := filepath.Join(home, e.Name())
		parents = append(parents, p)
		sub, _ := os.ReadDir(p)
		for _, s := range sub {
			if visibleDir(s) && s.Name() != "node_modules" && len(parents) < maxParents {
				parents = append(parents, filepath.Join(p, s.Name()))
			}
		}
	}

	var out []Candidate
	for _, p := range parents {
		children, _ := os.ReadDir(p)
		n, seen := 0, 0
		for _, c := range children {
			if !visibleDir(c) {
				continue
			}
			if seen++; seen > maxChildren {
				break
			}
			if entries, err := os.ReadDir(filepath.Join(p, c.Name())); err == nil && len(Markers(entries)) > 0 {
				n++
			}
		}
		if n >= 2 {
			out = append(out, Candidate{p, n})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Projects != out[j].Projects {
			return out[i].Projects > out[j].Projects
		}
		return len(out[i].Path) < len(out[j].Path)
	})
	return out
}

func visibleDir(e os.DirEntry) bool {
	return e.IsDir() && !strings.HasPrefix(e.Name(), ".")
}
