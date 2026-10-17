package scan

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/batikankutluer/mac-cleaner/internal/store"
)

func write(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWalkFolder(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module x")
	write(t, filepath.Join(root, "main.go"), "package main")
	write(t, filepath.Join(root, "docs/a.pdf"), "pdf")
	write(t, filepath.Join(root, ".git/HEAD"), "ref")              // not descended into
	write(t, filepath.Join(root, "node_modules/x/index.js"), "js") // not descended into
	write(t, filepath.Join(root, "sub/inner/package.json"), "{}")

	f, size, hash, err := walkFolder(root, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if f.Files != 4 || size != int64(len("module x")+len("package main")+len("pdf")+len("{}")) {
		t.Errorf("files=%d size=%d", f.Files, size)
	}
	if f.Exts[".go"] != 1 || f.Exts[".pdf"] != 1 || f.Exts[".js"] != 0 {
		t.Errorf("exts = %v", f.Exts)
	}
	if len(f.Markers) != 2 { // .git and go.mod
		t.Errorf("markers = %v", f.Markers)
	}
	if f.Projects != 1 { // sub/inner
		t.Errorf("projects = %d, want 1", f.Projects)
	}

	write(t, filepath.Join(root, "docs/a.pdf"), "changed")
	if _, _, again, _ := walkFolder(root, time.Time{}); again == hash {
		t.Error("hash did not change when a nested file changed")
	}
}

func TestListSkipsDestinations(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "loose.txt"), "x")
	write(t, filepath.Join(root, "proje/go.mod"), "module p")
	write(t, filepath.Join(root, "Belgeler/eski.pdf"), "x")     // a destination
	write(t, filepath.Join(root, "Kutu/Kurulumlar/a.dmg"), "x") // contains a destination

	res := &Result{}
	items := list([]string{root}, []string{filepath.Join(root, "Belgeler"), filepath.Join(root, "Kutu/Kurulumlar")}, res)
	got := map[string]bool{}
	for _, it := range items {
		got[filepath.Base(it.Path)] = it.IsDir
	}
	if len(got) != 2 || got["loose.txt"] || !got["proje"] {
		t.Errorf("items = %v", got)
	}
	if len(res.Skipped) != 2 {
		t.Errorf("skipped = %v", res.Skipped)
	}
}

func TestListSkipsAppFolders(t *testing.T) {
	real := installedApps
	installedApps = func() []string { return []string{"acme.io", "Image Capture"} }
	t.Cleanup(func() { installedApps = real })
	root := t.TempDir()
	write(t, filepath.Join(root, "Acme/recording.mp4"), "x")
	write(t, filepath.Join(root, "Adobe/Premiere/p.prproj"), "x")
	write(t, filepath.Join(root, "Notlar/.obsidian/app.json"), "{}")
	write(t, filepath.Join(root, "Image/a.png"), "x") // must not match "Image Capture"

	res := &Result{}
	items := list([]string{root}, nil, res)
	if len(items) != 1 || filepath.Base(items[0].Path) != "Image" {
		t.Errorf("items = %v", items)
	}
	skipped := map[string]string{}
	for _, s := range res.Skipped {
		skipped[filepath.Base(s.Path)] = s.Reason
	}
	for _, name := range []string{"Acme", "Adobe", "Notlar"} {
		if skipped[name] == "" {
			t.Errorf("%s not skipped (skipped: %v)", name, skipped)
		}
	}
}

func TestEmptyFolders(t *testing.T) {
	empty := &Item{Path: t.TempDir(), IsDir: true}
	if err := hashItem(nil, empty, time.Time{}); err != nil || !empty.Empty {
		t.Errorf("empty folder: empty=%v err=%v", empty.Empty, err)
	}
	finder := &Item{Path: t.TempDir(), IsDir: true}
	write(t, filepath.Join(finder.Path, ".DS_Store"), "x")
	if err := hashItem(nil, finder, time.Time{}); err != nil || !finder.Empty {
		t.Errorf(".DS_Store only: empty=%v err=%v", finder.Empty, err)
	}
	nested := &Item{Path: t.TempDir(), IsDir: true}
	if err := os.MkdirAll(filepath.Join(nested.Path, "files/deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(nested.Path, "files/.DS_Store"), "x")
	if err := hashItem(nil, nested, time.Time{}); err != nil || !nested.Empty {
		t.Errorf("only empty subfolders: empty=%v err=%v", nested.Empty, err)
	}
	hidden := &Item{Path: t.TempDir(), IsDir: true}
	write(t, filepath.Join(hidden.Path, ".venv/bin/python"), "x")
	if err := hashItem(nil, hidden, time.Time{}); err == nil || hidden.Empty {
		t.Errorf("hidden content must not count as empty: empty=%v err=%v", hidden.Empty, err)
	}
}

func TestSameApp(t *testing.T) {
	cases := map[[2]string]bool{
		{"Acme", "acme.io"}:                      true,
		{"Acme", "Acme"}:                         true,
		{"Some Game Studio", "Some Game Studio"}: true,
		{"Image", "Image Capture"}:               false,
		{"Acme", "Acme Helper Tool"}:             false,
		{"Acm", "Acme"}:                          false,
		{"Ab", "Ab"}:                             false, // too short to trust
	}
	for c, want := range cases {
		if got := sameApp(c[0], c[1]); got != want {
			t.Errorf("sameApp(%q, %q) = %v", c[0], c[1], got)
		}
	}
}

func TestWalkFolderNestedProjectsAndPackages(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "group-a/repo-a/.git/HEAD"), "x")
	write(t, filepath.Join(root, "group-a/repo-a/packages/ui/package.json"), "{}") // inside repo-a: not counted again
	write(t, filepath.Join(root, "group-b/repo-b/mix.exs"), "x")
	write(t, filepath.Join(root, "group-b/repo-b/lib/b.ex"), "x")
	for i := range 50 {
		write(t, filepath.Join(root, "Tool.app/Contents/Resources", fmt.Sprintf("r%d.qml", i)), "x")
	}

	f, _, _, err := walkFolder(root, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if f.Projects != 2 {
		t.Errorf("projects = %d, want 2", f.Projects)
	}
	if f.Exts[".qml"] != 0 || f.Exts[".app"] != 1 || f.ExtBytes[".app"] != 50 {
		t.Errorf("package not counted as one item: %v %v", f.Exts, f.ExtBytes)
	}
}

func TestStaleArtifacts(t *testing.T) {
	root := t.TempDir()
	old := time.Now().AddDate(-1, 0, 0)
	touchOld := func(p, data string) {
		write(t, p, data)
		os.Chtimes(p, old, old)
	}
	// An old Next.js app, an old Rust tool, and a fresh one.
	touchOld(filepath.Join(root, "eski-web/package.json"), "{}")
	touchOld(filepath.Join(root, "eski-web/src/page.tsx"), "x")
	write(t, filepath.Join(root, "eski-web/node_modules/react/index.js"), "0123456789")
	write(t, filepath.Join(root, "eski-web/.next/cache/x"), "01234")
	touchOld(filepath.Join(root, "eski-rust/Cargo.toml"), "x")
	write(t, filepath.Join(root, "eski-rust/target/debug/app"), "0123")
	write(t, filepath.Join(root, "yeni/package.json"), "{}")
	write(t, filepath.Join(root, "yeni/node_modules/x.js"), "x")
	// "target" without Cargo.toml is not an artifact.
	touchOld(filepath.Join(root, "eski-web/docs/target/keep.md"), "x")

	f, _, _, err := walkFolder(root, time.Now().AddDate(0, -3, 0))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, a := range f.Artifacts {
		rel, _ := filepath.Rel(root, a.Path)
		got[rel] = a.Bytes
	}
	want := map[string]int64{"eski-web/node_modules": 10, "eski-web/.next": 5, "eski-rust/target": 4}
	if len(got) != len(want) {
		t.Fatalf("artifacts = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d", k, got[k], v)
		}
	}
	if f.Exts[".js"] != 0 || f.Exts[".md"] != 1 {
		t.Errorf("artifacts leaked into the summary: %v", f.Exts)
	}
}

func TestTrackedArtifactKept(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	old := time.Now().AddDate(-1, 0, 0)
	write(t, filepath.Join(root, "p/package.json"), "{}")
	write(t, filepath.Join(root, "p/node_modules/vendored.js"), "x")
	if out, err := exec.Command("git", "-C", filepath.Join(root, "p"), "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	for _, p := range []string{"p/package.json", "p/node_modules/vendored.js"} {
		os.Chtimes(filepath.Join(root, p), old, old)
	}
	f, _, _, _ := walkFolder(root, time.Now())
	if len(f.Artifacts) != 0 {
		t.Errorf("node_modules in a repo that does not ignore it was offered for deletion: %v", f.Artifacts)
	}
}

func TestUnpackPiles(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "Projects/a/go.mod"), "module a")
	write(t, filepath.Join(root, "Projects/myapp/frontend/package.json"), "{}")
	write(t, filepath.Join(root, "Projects/myapp/backend/go.mod"), "module b")
	write(t, filepath.Join(root, "Projects/old-projects/d/Cargo.toml"), "x") // a pile inside a pile
	write(t, filepath.Join(root, "Projects/notes.txt"), "x")                 // not a project: stays in the pile
	write(t, filepath.Join(root, "review/r1/.git/HEAD"), "x")                // not a pile by name: moved whole
	write(t, filepath.Join(root, "ws/blockchain/old-projects/c/foundry.toml"), "x")

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	res := &Result{}
	items := list([]string{root}, []string{filepath.Join(root, "ws")}, res)
	forcedPath := filepath.Join(root, "ws/blockchain/old-projects")
	forced := map[string]bool{forcedPath: true}
	it, _ := newItem(forcedPath, true)
	items = hashAll(context.Background(), append(items, it), st, time.Time{}, res, nil)
	items = unpack(context.Background(), items, forced, st, time.Time{}, res, nil)

	got := map[string]string{}
	for _, it := range items {
		rel, _ := filepath.Rel(root, it.Path)
		from, _ := filepath.Rel(root, it.From)
		if it.From == "" {
			from = ""
		}
		got[rel] = from
	}
	want := map[string]string{
		"Projects/a":                   "Projects",
		"Projects/notes.txt":           "Projects",
		"Projects/myapp":               "Projects",
		"Projects/old-projects/d":      "Projects",
		"review":                       "",
		"ws/blockchain/old-projects/c": "ws/blockchain/old-projects",
	}
	if len(got) != len(want) {
		t.Fatalf("items = %v", got)
	}
	for k, v := range want {
		if from, ok := got[k]; !ok || from != v {
			t.Errorf("%s: from %q, want %q (items %v)", k, from, v, got)
		}
	}
}

func TestUnpackWorkspaceLeftovers(t *testing.T) {
	root := t.TempDir()
	pile := filepath.Join(root, "blockchain/old-projects")
	write(t, filepath.Join(pile, "node-data/genesis.json"), "{}")
	write(t, filepath.Join(pile, "service/compose.yml"), "x")
	if err := os.MkdirAll(filepath.Join(pile, "empty-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	emptied := filepath.Join(root, "blockchain/Projects")
	if err := os.MkdirAll(emptied, 0o755); err != nil {
		t.Fatal(err)
	}

	res := &Result{}
	var items []Item
	forced := map[string]bool{}
	for _, p := range []string{pile, emptied} {
		it, _ := newItem(p, true)
		items = append(items, it)
		forced[p] = true
	}
	items = hashAll(context.Background(), items, nil, time.Time{}, res, nil)
	items = unpack(context.Background(), items, forced, nil, time.Time{}, res, nil)

	got := map[string]string{}
	for _, it := range items {
		kind := "dosya/klasör"
		if it.Empty {
			kind = "boş"
		}
		if it.Folder != nil && len(it.Folder.Markers) > 0 {
			kind = "proje"
		}
		rel, _ := filepath.Rel(root, it.Path)
		got[rel] = kind
	}
	want := map[string]string{
		"blockchain/old-projects/node-data": "dosya/klasör",
		"blockchain/old-projects/service":   "proje",
		"blockchain/old-projects/empty-dir": "boş",
		"blockchain/Projects":               "boş",
	}
	if len(got) != len(want) {
		t.Fatalf("items = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}
