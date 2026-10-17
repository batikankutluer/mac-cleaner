package apply

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/batikankutluer/mac-cleaner/internal/plan"
	"github.com/batikankutluer/mac-cleaner/internal/scan"
	"github.com/batikankutluer/mac-cleaner/internal/store"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// planned records the file as a scan would and returns a selected plan item.
func planned(t *testing.T, st *store.Store, src, dst string) plan.Item {
	t.Helper()
	var s syscall.Stat_t
	if err := syscall.Lstat(src, &s); err != nil {
		t.Fatal(err)
	}
	it := scan.Item{Path: src, IsDir: s.Mode&syscall.S_IFMT == syscall.S_IFDIR, Dev: uint64(uint32(s.Dev)), Inode: s.Ino,
		Size: s.Size, ModTime: time.Unix(s.Mtimespec.Unix()), Hash: "h-" + filepath.Base(src)}
	run, _ := st.BeginRun("scan", nil)
	id, err := st.Observe(run, store.Observation{Dev: it.Dev, Inode: it.Inode,
		State: store.State{Path: src, Hash: it.Hash, Size: it.Size, MTime: it.ModTime.UnixNano()}})
	if err != nil {
		t.Fatal(err)
	}
	it.FileID = id
	return plan.Item{Item: it, Dst: dst, Selected: true}
}

func TestApplyAndUndo(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	write(t, filepath.Join(root, "Downloads/rapor.pdf"), "pdf")
	write(t, filepath.Join(root, "Downloads/proje/go.mod"), "module p")
	write(t, filepath.Join(root, "Downloads/klip.mp4"), "mp4")
	write(t, filepath.Join(root, "Movies/klip.mp4"), "already there")
	write(t, filepath.Join(root, "Downloads/degisen.txt"), "v1")
	if err := os.MkdirAll(filepath.Join(root, "Documents"), 0o755); err != nil {
		t.Fatal(err)
	}

	items := []plan.Item{
		planned(t, st, filepath.Join(root, "Downloads/rapor.pdf"), filepath.Join(root, "Documents/Belgeler/rapor.pdf")),
		planned(t, st, filepath.Join(root, "Downloads/proje"), filepath.Join(root, "code/backend/proje")),
		planned(t, st, filepath.Join(root, "Downloads/klip.mp4"), filepath.Join(root, "Movies/klip.mp4")),
		planned(t, st, filepath.Join(root, "Downloads/degisen.txt"), filepath.Join(root, "Documents/Diğer/degisen.txt")),
	}
	notSelected := planned(t, st, filepath.Join(root, "Downloads/proje/go.mod"), filepath.Join(root, "x/go.mod"))
	notSelected.Selected = false
	items = append(items, notSelected)
	write(t, filepath.Join(root, "Downloads/degisen.txt"), "v2, edited after the scan")

	res, err := Apply(context.Background(), st, items, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != 2 || len(res.Failed) != 2 {
		t.Fatalf("moved %d, failed %v", res.Moved, res.Failed)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "Movies/klip.mp4")); string(b) != "already there" {
		t.Fatal("existing target was overwritten")
	}
	if _, err := os.Stat(filepath.Join(root, "code/backend/proje/go.mod")); err != nil {
		t.Error("folder not moved as a unit")
	}
	hist, _ := st.History(items[0].FileID)
	if len(hist) != 2 || hist[0].Op != "moved" || hist[0].Old.Path != items[0].Path || hist[0].New.Path != items[0].Dst {
		t.Errorf("history = %+v", hist)
	}

	undo, err := Undo(st, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if undo.Moved != 2 || len(undo.Failed) != 0 {
		t.Fatalf("undo moved %d, failed %v", undo.Moved, undo.Failed)
	}
	for _, p := range []string{"Downloads/rapor.pdf", "Downloads/proje/go.mod", "Downloads/klip.mp4"} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("%s not restored", p)
		}
	}
	for _, p := range []string{"Documents/Belgeler", "work"} {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			t.Errorf("%s was created by apply and should be gone", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "Documents")); err != nil {
		t.Error("pre-existing Documents was removed")
	}
	if hist, _ := st.History(items[0].FileID); len(hist) != 3 || hist[0].Op != "undone" {
		t.Errorf("history after undo = %+v", hist)
	}
	if _, err := Undo(st, res.RunID); err == nil {
		t.Error("second undo of the same run was allowed")
	}
}

func TestApplyCleansStaleArtifacts(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	write(t, filepath.Join(root, "Desktop/eski/package.json"), "{}")
	write(t, filepath.Join(root, "Desktop/eski/src/app.ts"), "x")
	write(t, filepath.Join(root, "Desktop/eski/node_modules/dep/index.js"), "0123456789")

	it := planned(t, st, filepath.Join(root, "Desktop/eski"), filepath.Join(root, "code/web/eski"))
	it.Folder = &scan.Folder{Files: 2, Artifacts: []scan.Artifact{{Path: filepath.Join(root, "Desktop/eski/node_modules"), Bytes: 10}}}
	it.Clean = true

	res, err := Apply(context.Background(), st, []plan.Item{it}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != 1 || res.Cleaned != 1 || res.Freed != 10 || res.Files != 2 || len(res.Failed) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "code/web/eski/node_modules")); err == nil {
		t.Error("node_modules was moved instead of deleted")
	}
	if _, err := os.Stat(filepath.Join(root, "code/web/eski/src/app.ts")); err != nil {
		t.Error("source not moved")
	}
	hist, _ := st.History(it.FileID)
	if len(hist) != 3 || hist[0].Op != "moved" || hist[1].Op != "cleaned" {
		t.Errorf("history = %+v", hist)
	}
}

func TestApplyUnpackedPile(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pile := filepath.Join(root, "code/blockchain/Projects")
	write(t, filepath.Join(pile, "a/go.mod"), "module a")
	write(t, filepath.Join(pile, "nested/b/package.json"), "{}")
	write(t, filepath.Join(pile, "nested/.DS_Store"), "finder")
	write(t, filepath.Join(root, "Desktop/old-projects/c/go.mod"), "module c")
	write(t, filepath.Join(root, "Desktop/old-projects/notlar.txt"), "kalsın")

	var items []plan.Item
	for _, m := range []struct{ src, from, dst string }{
		{filepath.Join(pile, "a"), pile, filepath.Join(root, "code/backend/a")},
		{filepath.Join(pile, "nested/b"), pile, filepath.Join(root, "code/web/b")},
		{filepath.Join(root, "Desktop/old-projects/c"), filepath.Join(root, "Desktop/old-projects"), filepath.Join(root, "code/backend/c")},
	} {
		it := planned(t, st, m.src, m.dst)
		it.From = m.from
		items = append(items, it)
	}
	res, err := Apply(context.Background(), st, items, nil)
	if err != nil || res.Moved != 3 {
		t.Fatalf("moved %d, failed %v, err %v", res.Moved, res.Failed, err)
	}
	if _, err := os.Stat(pile); err == nil {
		t.Error("emptied pile was left behind")
	}
	if _, err := os.Stat(filepath.Join(root, "code/blockchain")); err != nil {
		t.Error("pruning went past the pile")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "Desktop/old-projects/notlar.txt")); string(b) != "kalsın" {
		t.Error("non-project file in a pile was touched")
	}

	if _, err := Undo(st, res.RunID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a/go.mod", "nested/b/package.json"} {
		if _, err := os.Stat(filepath.Join(pile, p)); err != nil {
			t.Errorf("undo did not restore %s", p)
		}
	}
}

func TestApplyRemovesEmptyFolders(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	empty := filepath.Join(root, "Documents/EmptyApp")
	finder := filepath.Join(root, "old-folder")
	filled := filepath.Join(root, "Desktop/dolu")
	for _, d := range []string{empty, finder, filled, filepath.Join(finder, "files/deeper")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(finder, ".DS_Store"), "x")

	var items []plan.Item
	for _, d := range []string{empty, finder, filled} {
		it := planned(t, st, d, "")
		it.Remove = true
		items = append(items, it)
	}
	write(t, filepath.Join(filled, "sonradan.txt"), "scan sonrası eklendi")

	res, err := Apply(context.Background(), st, items, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 2 || len(res.Failed) != 1 {
		t.Fatalf("removed %d, failed %v", res.Removed, res.Failed)
	}
	if _, err := os.Stat(filepath.Join(filled, "sonradan.txt")); err != nil {
		t.Fatal("a folder that filled up after the scan was deleted")
	}
	for _, d := range []string{empty, finder} {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("%s still there", d)
		}
	}
	if _, err := Undo(st, res.RunID); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{empty, finder} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("undo did not recreate %s", d)
		}
	}
}
