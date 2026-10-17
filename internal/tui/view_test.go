package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/batikankutluer/mac-cleaner/internal/apply"
	"github.com/batikankutluer/mac-cleaner/internal/decide"
	"github.com/batikankutluer/mac-cleaner/internal/plan"
	"github.com/batikankutluer/mac-cleaner/internal/scan"
)

func TestReviewFitsTerminal(t *testing.T) {
	names := []string{"rapor.pdf", "Ekran Resmi 2026-10-01 12.00.00.png", "kurulum.dmg",
		"çok uzun bir dosya adı ki kesinlikle ekrana sığmayacak kadar uzun olsun diye yazıldı.mp4", "satır\nkıran.txt"}
	var items []scan.Item
	for i, n := range names {
		items = append(items, scan.Item{FileID: int64(i + 1), Path: "/Users/x/Downloads/" + n, Size: int64(1 << (10 * (i % 4))),
			BirthTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Hash: strings.Repeat("ab", 32)})
	}
	decisions := decide.All(context.Background(), decide.Rules{}, plan.Inputs(items), 2, nil)

	for _, size := range [][2]int{{120, 30}, {80, 20}, {60, 14}} {
		layout := plan.DefaultLayout("/Users/x", "/Users/x/code")
		m := newModel(context.Background(), Config{Options: plan.Options{Layout: layout}, DeciderName: "kurallar"})
		next, _ := m.Update(scanDoneMsg{res: &scan.Result{}, items: plan.Build(items, decisions, layout), runID: 1})
		next, _ = next.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := next.View()

		lines := strings.Split(view, "\n")
		if len(lines) != size[1] {
			t.Errorf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w > size[0] {
				t.Errorf("%dx%d: line %d is %d wide: %q", size[0], size[1], i, w, l)
			}
		}
		if size[0] == 120 {
			t.Log("\n" + view)
		}
	}
}

func TestKeyPrompt(t *testing.T) {
	var saved string
	cfg := Config{DeciderName: "jev-1.14.0", Key: &KeyPrompt{Needed: true, Save: func(k string) error { saved = k; return nil }}}
	var m tea.Model = newModel(context.Background(), cfg)
	if !strings.Contains(m.View(), "TypeSafe API anahtarı") {
		t.Fatal("key prompt not shown")
	}
	for _, r := range "ts_secret-key" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if strings.Contains(m.View(), "ts_secret") {
		t.Error("key is echoed in clear text")
	}
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter did not save")
	}
	m, _ = m.Update(cmd())
	if saved != "ts_secret-key" {
		t.Errorf("saved %q", saved)
	}
	if m.(model).phase != phaseScanning {
		t.Errorf("phase = %v after save, want scanning", m.(model).phase)
	}
}

func TestConfirmAndDoneFit(t *testing.T) {
	items := []scan.Item{
		{FileID: 1, Path: "/Users/x/Desktop/eski-proje", IsDir: true, Size: 5 << 20, Hash: strings.Repeat("ab", 32),
			Folder: &scan.Folder{Files: 120, Exts: map[string]int{".tsx": 100}, Signals: []string{"next.config.js", "package.json"},
				LastModified: time.Now().AddDate(-1, 0, 0),
				Artifacts:    []scan.Artifact{{Path: "/Users/x/Desktop/eski-proje/node_modules", Bytes: 900 << 20}}}},
		{FileID: 2, Path: "/Users/x/Downloads/klip.mp4", Size: 30 << 20, Hash: strings.Repeat("cd", 32)},
	}
	decisions := []decide.Decision{{Category: decide.Projects, Source: "kural"}, {Category: decide.Videos, Source: "kural"}}
	layout := plan.DefaultLayout("/Users/x", "/Users/x/code")
	var m tea.Model = newModel(context.Background(), Config{Options: plan.Options{Layout: layout, StaleDays: 90}, DeciderName: "kurallar"})
	m, _ = m.Update(scanDoneMsg{res: &scan.Result{}, items: plan.Build(items, decisions, layout), runID: 1})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	if !strings.Contains(m.View(), "900.0 MB temizlenecek") {
		t.Errorf("freeable space missing from header:\n%s", m.View())
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	confirm := m.View()
	for _, want := range []string{"2 öğe taşınacak", "121 dosya", "1 build klasörü silinecek", "900.0 MB alan açılacak", "/Users/x/code/web"} {
		if !strings.Contains(confirm, want) {
			t.Errorf("confirm screen lacks %q:\n%s", want, confirm)
		}
	}
	m, _ = m.Update(appliedMsg{res: apply.Result{RunID: 7, Moved: 2, Files: 121, Bytes: 35 << 20, Cleaned: 1, Freed: 900 << 20, Elapsed: 1500 * time.Millisecond}})
	done := m.View()
	for _, want := range []string{"Bitti", "1.5s", "2 öğe taşındı", "900.0 MB disk alanı açıldı", "cleaner -undo"} {
		if !strings.Contains(done, want) {
			t.Errorf("summary lacks %q:\n%s", want, done)
		}
	}
	for _, v := range []string{confirm, done} {
		for _, l := range strings.Split(v, "\n") {
			if lipgloss.Width(l) > 90 {
				t.Errorf("line too wide: %q", l)
			}
		}
	}
}
