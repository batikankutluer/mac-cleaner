package store

import (
	"path/filepath"
	"testing"
)

func TestObserveBuildsChain(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, err := st.BeginRun("scan", []string{"/x"})
	if err != nil {
		t.Fatal(err)
	}

	first := Observation{Dev: 1, Inode: 42, State: State{Path: "/x/a.pdf", Hash: "h1", Size: 10, MTime: 100}}
	id, err := st.Observe(run, first)
	if err != nil {
		t.Fatal(err)
	}

	// Unchanged: no new event.
	if again, err := st.Observe(run, first); err != nil || again != id {
		t.Fatalf("again = %d, %v; want %d", again, err, id)
	}

	// Renamed: same identity, new event that remembers the old path.
	renamed := first
	renamed.Path = "/x/b.pdf"
	if got, err := st.Observe(run, renamed); err != nil || got != id {
		t.Fatalf("renamed = %d, %v; want %d", got, err, id)
	}

	hist, err := st.History(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("history has %d events, want 2", len(hist))
	}
	if hist[0].Op != "changed" || hist[0].Old == nil || hist[0].Old.Path != "/x/a.pdf" || hist[0].New.Path != "/x/b.pdf" {
		t.Errorf("head event = %+v", hist[0])
	}
	if hist[0].PrevID != hist[1].ID || hist[1].Op != "seen" || hist[1].Old != nil || hist[1].PrevID != 0 {
		t.Errorf("chain broken: %+v -> %+v", hist[0], hist[1])
	}

	// Inode reused by an unrelated file: new identity, old history kept.
	reused := Observation{Dev: 1, Inode: 42, State: State{Path: "/x/other.zip", Hash: "h2", Size: 5, MTime: 200}}
	other, err := st.Observe(run, reused)
	if err != nil {
		t.Fatal(err)
	}
	if other == id {
		t.Fatal("reused inode should get a new file id")
	}
	if hist, _ := st.History(id); len(hist) != 2 {
		t.Errorf("old history has %d events after reuse, want 2", len(hist))
	}
}

func TestSettings(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if v, err := st.Setting("workspace"); err != nil || v != "" {
		t.Fatalf("empty: %q, %v", v, err)
	}
	for _, want := range []string{"/a", "/b"} {
		if err := st.SetSetting("workspace", want); err != nil {
			t.Fatal(err)
		}
		if v, _ := st.Setting("workspace"); v != want {
			t.Errorf("got %q, want %q", v, want)
		}
	}
}
