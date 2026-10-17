package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/batikankutluer/mac-cleaner/internal/workspace"
)

// Folder summarizes a directory that is moved as a unit.
type Folder struct {
	Files       int
	Exts        map[string]int   // lower-case extension ("" for none) → count
	ExtBytes    map[string]int64 // same keys → total size
	Markers     []string         // project markers at the top level
	Projects    int              // projects nested inside, outermost only
	ProjectDirs []string         // their paths
	Signals     []string         // marker and stack-hint names seen anywhere inside
	Truncated   bool             // stopped at maxFolderFiles

	LastModified time.Time  // newest file, build artifacts and .git aside
	Artifacts    []Artifact // regenerable folders of projects untouched since staleBefore
}

const (
	maxFolderFiles  = 50000
	maxProjectDepth = 5 // how deep below the folder a nested project is looked for
)

// Bundles that Finder shows as a single item. They count as one file of their
// own type instead of hundreds of internal ones.
var packageSuffixes = []string{".app", ".framework", ".bundle", ".plugin", ".kext", ".xcassets",
	".photoslibrary", ".musiclibrary", ".rtfd", ".pages", ".numbers", ".key", ".logicx", ".band"}

// walkFolder counts a folder's visible files without reading any of them. Its
// hash covers every file's relative path, size and mtime, so it changes when
// anything inside changes. Hidden folders such as .git, build artifacts and
// iCloud folders whose listing is not on this Mac are not descended into.
// Artifacts of projects with nothing newer than staleBefore are collected for
// deletion; a zero staleBefore collects none.
func walkFolder(root string, staleBefore time.Time) (Folder, int64, string, error) {
	top, err := os.ReadDir(root)
	if err != nil {
		return Folder{}, 0, "", err
	}
	f := Folder{Exts: map[string]int{}, ExtBytes: map[string]int64{}, Markers: workspace.Markers(top)}
	projects, signals := map[string]bool{}, map[string]bool{}
	newest := map[string]time.Time{} // per directory, own files only until propagated below
	var candidates []string
	var size int64
	h := sha256.New()
	add := func(p, rel, ext string, n int64, mtime time.Time) {
		f.Files++
		size += n
		f.Exts[ext]++
		f.ExtBytes[ext] += n
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", rel, n, mtime.UnixNano())
		if dir := filepath.Dir(p); mtime.After(newest[dir]) {
			newest[dir] = mtime
		}
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil // unreadable parts are left out, not fatal
		}
		rel, _ := filepath.Rel(root, p)
		parent := filepath.Dir(p)
		depth := strings.Count(rel, "/")
		name := d.Name()
		if depth <= maxProjectDepth && workspace.IsSignal(name) {
			signals[name] = true
			if workspace.IsMarker(name) && parent != root {
				projects[parent] = true
			}
		}
		if f.Files >= maxFolderFiles {
			f.Truncated = true
			return fs.SkipAll
		}
		if d.IsDir() {
			if IsArtifact(p) {
				candidates = append(candidates, p)
				return fs.SkipDir
			}
			if strings.HasPrefix(name, ".") || name == "node_modules" || dataless(d) {
				return fs.SkipDir
			}
			for _, s := range packageSuffixes {
				if strings.HasSuffix(strings.ToLower(name), s) {
					info, _ := d.Info()
					add(p, rel, s, treeSize(p), info.ModTime())
					return fs.SkipDir
				}
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		add(p, rel, strings.ToLower(filepath.Ext(name)), info.Size(), info.ModTime())
		return nil
	})
	// Each directory's newest time covers everything below it.
	own := make(map[string]time.Time, len(newest))
	for d, t := range newest {
		own[d] = t
	}
	for d, t := range own {
		for p := d; p != root && strings.HasPrefix(p, root); p = filepath.Dir(p) {
			if parent := filepath.Dir(p); t.After(newest[parent]) {
				newest[parent] = t
			}
		}
	}
	f.LastModified = newest[root]
	if !staleBefore.IsZero() {
		f.Artifacts = staleArtifacts(candidates, newest, staleBefore)
	}
	f.ProjectDirs = outermost(projects)
	sort.Strings(f.ProjectDirs)
	f.Projects = len(f.ProjectDirs)
	for s := range signals {
		f.Signals = append(f.Signals, s)
	}
	sort.Strings(f.Signals)
	return f, size, "tree:" + hex.EncodeToString(h.Sum(nil)), err
}

// outermost drops projects that sit inside another one, such as the packages
// of a monorepo.
func outermost(dirs map[string]bool) []string {
	var out []string
	for d := range dirs {
		inner := false
		for p := filepath.Dir(d); p != "/" && p != "."; p = filepath.Dir(p) {
			if dirs[p] {
				inner = true
				break
			}
		}
		if !inner {
			out = append(out, d)
		}
	}
	return out
}

func treeSize(root string) int64 {
	var n int64
	filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func dataless(d fs.DirEntry) bool {
	info, err := d.Info()
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Flags&sfDataless != 0
}
