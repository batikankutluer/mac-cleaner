// Package plan turns scanned files and decisions into proposed moves. Nothing
// here touches the file system except checking whether a destination exists.
package plan

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/batikankutluer/mac-cleaner/internal/decide"
	"github.com/batikankutluer/mac-cleaner/internal/engine"
	"github.com/batikankutluer/mac-cleaner/internal/scan"
	"github.com/batikankutluer/mac-cleaner/internal/store"
	"github.com/batikankutluer/mac-cleaner/internal/workspace"
)

const (
	PhaseModelLoad  = "model-load"
	PhaseModelCheck = "model-check"
	PhaseDecide     = "decide"
	PhaseApply      = "apply"
)

var phaseLabels = map[string]string{
	engine.StepDownload: "Ollaya indiriliyor (sadece ilk seferde)",
	engine.StepStart:    "Ollaya başlatılıyor",
	engine.StepPull:     "model hazırlanıyor (sadece ilk seferde indirilir)",
	PhaseModelLoad:      "model belleğe yükleniyor",
	PhaseModelCheck:     "modele bağlanılıyor",
	scan.PhaseList:      "dosyalar listeleniyor",
	scan.PhaseHash:      "sha256 hesaplanıyor (paralel)",
	scan.PhaseMeta:      "Spotlight metadata okunuyor",
	scan.PhaseSave:      "SQLite'a yazılıyor",
	PhaseDecide:         "karar veriliyor (paralel)",
	PhaseApply:          "taşınıyor",
}

// PhaseLabel is the human-readable name of a progress phase.
func PhaseLabel(phase string) string {
	if l, ok := phaseLabels[phase]; ok {
		return l
	}
	return "başlıyor"
}

type Item struct {
	scan.Item
	decide.Decision
	Dst      string
	Selected bool
	Dup      bool // another scanned file has the same content
	// Clean: delete the folder's stale build artifacts before moving it. Only
	// projects get it, and the user can turn it off per item.
	Clean bool
	// Remove: an empty folder, deleted instead of moved. Dst is empty.
	Remove bool
}

// CleanBytes is what deleting the item's artifacts frees, if cleaning is on.
func (it Item) CleanBytes() int64 {
	if !it.Clean || it.Folder == nil {
		return 0
	}
	var n int64
	for _, a := range it.Folder.Artifacts {
		n += a.Bytes
	}
	return n
}

// activeWithin: workspace projects changed this recently are not selected.
const activeWithin = 7 * 24 * time.Hour

// Layout maps each category to the folder its files go to.
type Layout map[string]string

// DefaultLayout files media into macOS's own folders, projects into the
// workspace and the rest under Documents. Installers stay in Downloads since
// they are usually deleted after use, and fonts deliberately avoid
// ~/Library/Fonts, which would install them.
func DefaultLayout(home, workspace string) Layout {
	docs := filepath.Join(home, "Documents")
	if workspace == "" {
		workspace = filepath.Join(docs, decide.Projects)
	}
	return Layout{
		decide.Projects:    workspace,
		decide.Screenshots: filepath.Join(home, "Pictures", decide.Screenshots),
		decide.Images:      filepath.Join(home, "Pictures"),
		decide.Videos:      filepath.Join(home, "Movies"),
		decide.Audio:       filepath.Join(home, "Music"),
		decide.Documents:   filepath.Join(docs, decide.Documents),
		decide.Archives:    filepath.Join(docs, decide.Archives),
		decide.Code:        filepath.Join(docs, decide.Code),
		decide.Fonts:       filepath.Join(docs, decide.Fonts),
		decide.Other:       filepath.Join(docs, decide.Other),
		decide.Installers:  filepath.Join(home, "Downloads", decide.Installers),
	}
}

type Options struct {
	Roots   []string
	Layout  Layout
	Protect []string // folders never moved, besides the layout's destinations
	Decider decide.Decider
	Workers int
	// StaleDays: build artifacts of projects untouched this long are deleted
	// before the move. 0 turns cleaning off.
	StaleDays int
	// Setup, when set, runs first and supplies the decider, e.g. after starting
	// a model server.
	Setup func(ctx context.Context, progress chan<- scan.Progress) (decide.Decider, error)
}

// Run scans the roots, records what it saw and decides where each file goes.
func Run(ctx context.Context, st *store.Store, opts Options, progress chan<- scan.Progress) (*scan.Result, []Item, int64, error) {
	if opts.Setup != nil {
		d, err := opts.Setup(ctx, progress)
		if err != nil {
			return nil, nil, 0, err
		}
		opts.Decider = d
	}
	runID, err := st.BeginRun("scan", opts.Roots)
	if err != nil {
		return nil, nil, 0, err
	}
	exclude := append(append([]string{}, opts.Protect...), opts.Roots...)
	for _, dir := range opts.Layout {
		exclude = append(exclude, dir)
	}
	var staleBefore time.Time
	if opts.StaleDays > 0 {
		staleBefore = time.Now().AddDate(0, 0, -opts.StaleDays)
	}
	ws := opts.Layout[decide.Projects]
	res, err := scan.Run(ctx, scan.Options{Roots: opts.Roots, Exclude: exclude, StaleBefore: staleBefore,
		Containers: workspacePiles(ws), InWorkspace: looseInWorkspace(ws)}, st, runID, progress)
	if ferr := st.FinishRun(runID, err); err == nil {
		err = ferr
	}
	if err != nil {
		return nil, nil, runID, err
	}
	inputs := Inputs(res.Items)
	decisions := decide.All(ctx, opts.Decider, inputs, opts.Workers, func(done int) {
		scan.Send(progress, scan.Progress{Phase: PhaseDecide, Done: done, Total: len(inputs)})
	})
	if err := ctx.Err(); err != nil {
		return nil, nil, runID, err
	}
	return res, Build(res.Items, decisions, opts.Layout), runID, nil
}

// workspacePiles finds piles of projects an earlier run moved into the
// workspace whole, directly in it or in one of its group folders.
func workspacePiles(ws string) []string {
	if ws == "" {
		return nil
	}
	var out []string
	dirs := []string{ws}
	for _, g := range decide.Groups {
		dirs = append(dirs, filepath.Join(ws, g))
	}
	for _, d := range dirs {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if e.IsDir() && workspace.IsDump(e.Name()) {
				out = append(out, filepath.Join(d, e.Name()))
			}
		}
	}
	return out
}

// looseInWorkspace lists the workspace's own folders that are neither group
// folders nor piles: projects that predate the groups.
func looseInWorkspace(ws string) []string {
	if ws == "" {
		return nil
	}
	entries, _ := os.ReadDir(ws)
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() && !strings.HasPrefix(name, ".") && !slices.Contains(decide.Groups, name) && !workspace.IsDump(name) {
			out = append(out, filepath.Join(ws, name))
		}
	}
	return out
}

func Inputs(items []scan.Item) []decide.Input {
	out := make([]decide.Input, len(items))
	for i, it := range items {
		out[i] = decide.Input{
			Name:          filepath.Base(it.Path),
			ContentType:   it.Meta.ContentType,
			TypeTree:      it.Meta.TypeTree,
			ScreenCapture: it.Meta.ScreenCapture,
			SourceHost:    SourceHost(it.Meta.WhereFroms),
			Size:          it.Size,
			Empty:         it.Empty,
			InWorkspace:   it.InWorkspace,
		}
		if f := it.Folder; f != nil && !it.Empty {
			out[i].Folder = folderInfo(f)
		}
	}
	return out
}

func folderInfo(f *scan.Folder) *decide.FolderInfo {
	return &decide.FolderInfo{Files: f.Files, Exts: f.Exts, ExtBytes: f.ExtBytes, Markers: f.Markers, Projects: f.Projects, Signals: f.Signals}
}

// Build puts each file directly into its category's folder, and each project
// into a group folder inside the workspace. Items that are already in place
// are left out of the plan.
func Build(items []scan.Item, decisions []decide.Decision, layout Layout) []Item {
	hashes := map[string]int{}
	for _, it := range items {
		hashes[it.Hash]++
	}
	out := make([]Item, 0, len(items))
	for i, it := range items {
		dec := decisions[i]
		if dec.Category == decide.Empty {
			out = append(out, Item{Item: it, Decision: dec, Selected: true, Remove: true})
			continue
		}
		dir := layout[dec.Category]
		if dec.Category == decide.Projects && it.Folder != nil && dir != "" {
			group, why := decide.ProjectGroup(folderInfo(it.Folder))
			dir = filepath.Join(dir, group)
			dec.Reason += fmt.Sprintf(" · grup %s (%s)", group, why)
		}
		if dir == "" || dir == filepath.Dir(it.Path) {
			continue
		}
		// A workspace project touched this week is probably open somewhere;
		// it is offered, but only moved if picked by hand.
		selected := true
		if it.InWorkspace && it.Folder != nil && time.Since(it.Folder.LastModified) < activeWithin {
			selected = false
			dec.Reason += " · son 7 günde değişmiş, elle seçilmedikçe taşınmaz"
		}
		out = append(out, Item{
			Item:     it,
			Decision: dec,
			Dst:      filepath.Join(dir, filepath.Base(it.Path)),
			Selected: selected,
			Dup:      hashes[it.Hash] > 1,
			Clean:    dec.Category == decide.Projects && it.Folder != nil && len(it.Folder.Artifacts) > 0,
		})
	}
	keepWebPagesTogether(out)
	order := map[string]int{}
	for i, c := range decide.Categories {
		order[c.Name] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := order[out[i].Category], order[out[j].Category]; a != b {
			return a < b
		}
		return strings.ToLower(filepath.Base(out[i].Path)) < strings.ToLower(filepath.Base(out[j].Path))
	})
	resolveConflicts(out)
	return out
}

// keepWebPagesTogether sends a saved web page's "<name>_files" folder wherever
// its "<name>.html" goes; apart, the page loses its images and styles.
func keepWebPagesTogether(items []Item) {
	byPath := make(map[string]int, len(items))
	for i, it := range items {
		byPath[it.Path] = i
	}
	for i := range items {
		page, ok := strings.CutSuffix(items[i].Path, "_files")
		if !ok || !items[i].IsDir {
			continue
		}
		for _, ext := range []string{".html", ".htm"} {
			if j, ok := byPath[page+ext]; ok {
				items[i].Category = items[j].Category
				items[i].Reason = "kaydedilmiş web sayfasının dosyaları, sayfayla birlikte"
				items[i].Dst = filepath.Join(filepath.Dir(items[j].Dst), filepath.Base(items[i].Path))
				break
			}
		}
	}
}

// resolveConflicts renames "x.pdf" to "x (2).pdf" when the destination already
// exists or another file in the plan claims it. APFS is case- and
// normalization-insensitive by default, so keys are folded the same way.
func resolveConflicts(items []Item) {
	taken := map[string]bool{}
	key := func(p string) string { return strings.ToLower(norm.NFC.String(p)) }
	for i := range items {
		if items[i].Remove {
			continue
		}
		dst := items[i].Dst
		for n := 2; taken[key(dst)] || exists(dst); n++ {
			ext := filepath.Ext(items[i].Dst)
			dst = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(items[i].Dst, ext), n, ext)
		}
		taken[key(dst)] = true
		items[i].Dst = dst
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// SourceHost returns the host a file was downloaded from. Browsers store the
// file URL first and the page URL last; the page is the more telling one.
func SourceHost(froms []string) string {
	for i := len(froms) - 1; i >= 0; i-- {
		if u, err := url.Parse(froms[i]); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return ""
}

// Rows are the plan as stored; an empty folder to remove has no destination.
func Rows(items []Item) []store.PlanRow {
	rows := make([]store.PlanRow, len(items))
	for i, it := range items {
		rows[i] = store.PlanRow{
			FileID: it.FileID, Src: it.Path, Dst: it.Dst,
			Category: it.Category, Source: it.Source, Reason: it.Reason, Confidence: it.Confidence,
			Selected: it.Selected,
		}
	}
	return rows
}
