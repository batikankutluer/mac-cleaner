package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, parts ...string) {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetect(t *testing.T) {
	home := t.TempDir()
	// ~/src/mine holds three projects, ~/src/team one, ~/Downloads is skipped.
	touch(t, home, "src/mine/a/go.mod")
	touch(t, home, "src/mine/b/package.json")
	if err := os.MkdirAll(filepath.Join(home, "src/mine/c/.git"), 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, home, "src/mine/notes/todo.txt")
	touch(t, home, "src/team/x/Cargo.toml")
	touch(t, home, "Downloads/p1/go.mod")
	touch(t, home, "Downloads/p2/go.mod")
	touch(t, home, "Downloads/p3/go.mod")

	got := Detect(home)
	if len(got) != 1 {
		t.Fatalf("got %+v, want only ~/src/mine", got)
	}
	if got[0].Path != filepath.Join(home, "src/mine") || got[0].Projects != 3 {
		t.Errorf("got %+v", got[0])
	}
}

func TestIsDump(t *testing.T) {
	for name, want := range map[string]bool{
		"Projects": true, "projects (2)": true, "Projeler": true, "repos": true, "GitHub": true,
		"duzenlenecek": true, "Düzenlenecek (2)": true, "to-organize": true, "old-projects": true, "eski_projeler": true,
		"reports": false, "notes": false, "my-projects-site": false, "codec": false, "SomeApp": false,
	} {
		if got := IsDump(name); got != want {
			t.Errorf("IsDump(%q) = %v", name, got)
		}
	}
}
