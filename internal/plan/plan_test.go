package plan

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/batikankutluer/mac-cleaner/internal/decide"
	"github.com/batikankutluer/mac-cleaner/internal/scan"
)

func TestBuildLayout(t *testing.T) {
	layout := DefaultLayout("/Users/x", "/Users/x/code")
	items := []scan.Item{
		{Path: "/Users/x/Downloads/klip.mp4", Hash: "a"},
		{Path: "/Users/x/Desktop/rapor.pdf", Hash: "b"},
		{Path: "/Users/x/Downloads/Kurulumlar/zaten.dmg", Hash: "c"}, // already in place
		{Path: "/Users/x/Downloads/Installer.dmg", Hash: "a"},
	}
	decisions := []decide.Decision{
		{Category: decide.Videos}, {Category: decide.Documents}, {Category: decide.Installers}, {Category: decide.Installers},
	}
	got := map[string]Item{}
	for _, it := range Build(items, decisions, layout) {
		got[it.Path] = it
	}
	want := map[string]string{
		"/Users/x/Downloads/klip.mp4":      "/Users/x/Movies/klip.mp4",
		"/Users/x/Desktop/rapor.pdf":       "/Users/x/Documents/Belgeler/rapor.pdf",
		"/Users/x/Downloads/Installer.dmg": "/Users/x/Downloads/Kurulumlar/Installer.dmg",
	}
	if len(got) != len(want) {
		t.Errorf("plan has %d items, want %d (files already in place are skipped)", len(got), len(want))
	}
	for src, dst := range want {
		if got[src].Dst != dst {
			t.Errorf("%s → %s, want %s", src, got[src].Dst, dst)
		}
	}
	if !got["/Users/x/Downloads/klip.mp4"].Dup || got["/Users/x/Desktop/rapor.pdf"].Dup {
		t.Error("duplicate flags wrong")
	}
}

func TestBuildGroupsProjects(t *testing.T) {
	layout := DefaultLayout("/Users/x", "/Users/x/code")
	items := []scan.Item{{Path: "/Users/x/Desktop/dapp", IsDir: true, Hash: "a",
		Folder: &scan.Folder{Files: 40, Exts: map[string]int{".tsx": 30, ".ts": 10}, Signals: []string{".git", "next.config.ts"}}}}
	got := Build(items, []decide.Decision{{Category: decide.Projects}}, layout)
	if len(got) != 1 || got[0].Dst != "/Users/x/code/web/dapp" {
		t.Errorf("got %+v", got)
	}
}

func TestBuildKeepsWebPagesTogether(t *testing.T) {
	layout := DefaultLayout("/Users/x", "/Users/x/code")
	items := []scan.Item{
		{Path: "/Users/x/Desktop/sayfa.html", Hash: "a"},
		{Path: "/Users/x/Desktop/sayfa_files", IsDir: true, Hash: "b", Folder: &scan.Folder{Files: 3, Exts: map[string]int{".js": 3}}},
	}
	got := Build(items, []decide.Decision{{Category: decide.Documents}, {Category: decide.Code}}, layout)
	dst := map[string]string{}
	for _, it := range got {
		dst[it.Path] = it.Dst
	}
	if dst["/Users/x/Desktop/sayfa_files"] != "/Users/x/Documents/Belgeler/sayfa_files" {
		t.Errorf("_files folder went to %s, not next to its page", dst["/Users/x/Desktop/sayfa_files"])
	}
}

func TestBuildWorkspaceProjects(t *testing.T) {
	layout := DefaultLayout("/Users/x", "/Users/x/code")
	old := &scan.Folder{Files: 30, Exts: map[string]int{".kt": 30}, Signals: []string{"build.gradle.kts"}, LastModified: time.Now().AddDate(0, -2, 0)}
	active := &scan.Folder{Files: 25, Exts: map[string]int{".go": 25}, Signals: []string{"go.mod"}, LastModified: time.Now()}
	items := []scan.Item{
		{Path: "/Users/x/code/android-app", IsDir: true, InWorkspace: true, Hash: "a", Folder: old},
		{Path: "/Users/x/code/cli-tool", IsDir: true, InWorkspace: true, Hash: "b", Folder: active},
	}
	dec := []decide.Decision{{Category: decide.Projects}, {Category: decide.Projects}}
	got := map[string]Item{}
	for _, it := range Build(items, dec, layout) {
		got[filepath.Base(it.Path)] = it
	}
	if a := got["android-app"]; a.Dst != "/Users/x/code/mobil/android-app" || !a.Selected {
		t.Errorf("android-app: %+v", a)
	}
	if m := got["cli-tool"]; m.Dst != "/Users/x/code/backend/cli-tool" || m.Selected {
		t.Errorf("an active project must be offered unselected: dst=%s selected=%v", m.Dst, m.Selected)
	}
}
