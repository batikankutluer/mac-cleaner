// Package scan lists the loose files and folders at the top level of each
// root, hashes them in parallel, reads their Spotlight metadata and records
// them in the store. Folders are summarized and moved as a unit.
package scan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/batikankutluer/mac-cleaner/internal/store"
	"github.com/batikankutluer/mac-cleaner/internal/workspace"
)

type Meta struct {
	ContentType   string   `json:"content_type,omitempty"`
	TypeTree      []string `json:"type_tree,omitempty"`
	ScreenCapture bool     `json:"screen_capture,omitempty"`
	WhereFroms    []string `json:"where_froms,omitempty"`
}

type Item struct {
	FileID      int64
	Path        string
	From        string // the pile of projects this one was unpacked from, if any
	IsDir       bool
	Empty       bool    // a folder holding nothing, or only Finder's .DS_Store
	InWorkspace bool    // already in the workspace, waiting to be sorted into a group
	Folder      *Folder // set for directories
	Dev, Inode  uint64
	Size        int64
	ModTime     time.Time
	BirthTime   time.Time
	Hash        string
	Meta        Meta
}

type Skip struct{ Path, Reason string }

type Result struct {
	Items   []Item
	Skipped []Skip
}

type Progress struct {
	Phase       string
	Done, Total int
	Bytes       bool // Done and Total are byte counts
	Current     string
}

const (
	PhaseList = "list"
	PhaseHash = "hash"
	PhaseMeta = "meta"
	PhaseSave = "save"
)

// SF_DATALESS: the content lives in iCloud and reading it would trigger a download.
const sfDataless = 0x40000000

var skipExts = map[string]string{
	".download":   "yarım indirme",
	".crdownload": "yarım indirme",
	".part":       "yarım indirme",
	".partial":    "yarım indirme",
	".icloud":     "iCloud yer tutucu",
}

// Options says what to scan.
type Options struct {
	Roots []string
	// Exclude: folders that are, or contain, one of these are skipped: the
	// destinations, which must never be moved into themselves, the roots, and
	// folders other tools depend on.
	Exclude []string
	// StaleBefore: projects with nothing newer have their build artifacts
	// collected for deletion. Zero collects none.
	StaleBefore time.Time
	// Containers: piles of projects outside the roots (inside the workspace,
	// left there by an earlier run) to unpack as well.
	Containers []string
	// InWorkspace: projects lying loose in the workspace, to sort into groups.
	InWorkspace []string
}

func Run(ctx context.Context, opts Options, st *store.Store, runID int64, progress chan<- Progress) (*Result, error) {
	res := &Result{}
	items := list(opts.Roots, opts.Exclude, res)
	for _, p := range opts.InWorkspace {
		if it, reason := newItem(p, true); reason == "" {
			it.InWorkspace = true
			items = append(items, it)
		}
	}
	forced := map[string]bool{}
	for _, c := range opts.Containers {
		if it, reason := newItem(c, true); reason == "" {
			items = append(items, it)
			forced[c] = true
		}
	}
	Send(progress, Progress{Phase: PhaseList, Done: len(items), Total: len(items)})

	items = hashAll(ctx, items, st, opts.StaleBefore, res, progress)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	items = unpack(ctx, items, forced, st, opts.StaleBefore, res, progress)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	paths := make([]string, len(items))
	for i, it := range items {
		paths[i] = it.Path
	}
	metas := readMeta(ctx, paths, func(done int) {
		Send(progress, Progress{Phase: PhaseMeta, Done: done, Total: len(paths)})
	})

	for i := range items {
		it := &items[i]
		it.Meta = metas[i]
		Send(progress, Progress{Phase: PhaseSave, Done: i + 1, Total: len(items), Current: it.Path})
		meta, err := json.Marshal(it.Meta)
		if err != nil {
			return nil, err
		}
		id, err := st.Observe(runID, store.Observation{
			Dev: it.Dev, Inode: it.Inode, Meta: string(meta),
			State: store.State{Path: it.Path, Hash: it.Hash, Size: it.Size, MTime: it.ModTime.UnixNano()},
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", it.Path, err)
		}
		it.FileID = id
	}
	res.Items = items
	return res, nil
}

// isEmptyDir: nothing inside but Finder's .DS_Store files and folders that
// are empty the same way. Any other file, hidden ones such as a .venv
// included, or a symlink makes a folder not empty.
func isEmptyDir(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		switch {
		case e.Name() == ".DS_Store" && e.Type().IsRegular():
		case e.IsDir():
			if empty, err := isEmptyDir(filepath.Join(dir, e.Name())); err != nil || !empty {
				return false, err
			}
		default:
			return false, nil
		}
	}
	return true, nil
}

func isOrContainsAny(dir string, paths []string) bool {
	for _, p := range paths {
		if p == dir || strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}

// Send reports progress without ever blocking the scan; dropped updates are fine.
func Send(ch chan<- Progress, p Progress) {
	if ch == nil {
		return
	}
	select {
	case ch <- p:
	default:
	}
}

func list(roots, exclude []string, res *Result) []Item {
	var items []Item
	apps := installedApps()
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			res.Skipped = append(res.Skipped, Skip{root, err.Error()})
			continue
		}
		for _, e := range entries {
			name := e.Name()
			p := filepath.Join(root, name)
			switch {
			case strings.HasPrefix(name, "."):
				continue // .DS_Store, .localized and other hidden files
			case e.IsDir():
				if isOrContainsAny(p, exclude) {
					res.Skipped = append(res.Skipped, Skip{p, "hedef ya da korunan klasör"})
					continue
				}
				if app := appOwner(p, apps); app != "" {
					res.Skipped = append(res.Skipped, Skip{p, "uygulama klasörü (" + app + ")"})
					continue
				}
			case !e.Type().IsRegular():
				res.Skipped = append(res.Skipped, Skip{p, "normal dosya değil"})
				continue
			}
			if reason, ok := skipExts[strings.ToLower(filepath.Ext(name))]; ok {
				res.Skipped = append(res.Skipped, Skip{p, reason})
				continue
			}
			it, reason := newItem(p, e.IsDir())
			if reason != "" {
				res.Skipped = append(res.Skipped, Skip{p, reason})
				continue
			}
			items = append(items, it)
		}
	}
	return items
}

// newItem stats a path; a non-empty reason says why it can't be an item.
func newItem(p string, isDir bool) (Item, string) {
	var st syscall.Stat_t
	if err := syscall.Lstat(p, &st); err != nil {
		return Item{}, err.Error()
	}
	if st.Flags&sfDataless != 0 {
		return Item{}, "iCloud'da, Mac'e inmemiş"
	}
	return Item{
		Path:      p,
		IsDir:     isDir,
		Dev:       uint64(uint32(st.Dev)),
		Inode:     st.Ino,
		Size:      st.Size,
		ModTime:   time.Unix(st.Mtimespec.Unix()),
		BirthTime: time.Unix(st.Birthtimespec.Unix()),
	}, ""
}

// unpack replaces each pile (a folder named like one that holds projects, or
// a pile an earlier run left in the workspace) with everything directly
// inside it, so each piece is sorted on its own: projects into workspace
// groups, the rest by content, empty folders removed. A pile emptied this way
// is removed after the move. A pile left in the workspace that is already
// empty becomes an empty folder to remove.
func unpack(ctx context.Context, items []Item, forced map[string]bool, st *store.Store, staleBefore time.Time, res *Result, progress chan<- Progress) []Item {
	apps := installedApps()
	var out, children []Item
	for _, it := range items {
		f := it.Folder
		pile := it.IsDir && !it.Empty && f != nil && len(f.Markers) == 0 &&
			(forced[it.Path] || workspace.IsDump(filepath.Base(it.Path)) && len(f.ProjectDirs) > 0)
		if !pile {
			if !forced[it.Path] || it.Empty {
				out = append(out, it)
			}
			continue
		}
		children = append(children, pileContents(it.Path, it.Path, apps, res)...)
	}
	return append(out, hashAll(ctx, children, st, staleBefore, res, progress)...)
}

// pileContents lists what is directly inside dir as items from pile. A
// subfolder that is named like a pile and is not a project is opened too;
// app folders and hidden entries are left where they are.
func pileContents(dir, pile string, apps []string, res *Result) []Item {
	entries, err := os.ReadDir(dir)
	if err != nil {
		res.Skipped = append(res.Skipped, Skip{dir, err.Error()})
		return nil
	}
	var out []Item
	for _, e := range entries {
		name := e.Name()
		p := filepath.Join(dir, name)
		switch {
		case strings.HasPrefix(name, "."):
			continue
		case e.IsDir():
			if app := appOwner(p, apps); app != "" {
				res.Skipped = append(res.Skipped, Skip{p, "uygulama klasörü (" + app + ")"})
				continue
			}
			if workspace.IsDump(name) {
				if sub, err := os.ReadDir(p); err == nil && len(workspace.Markers(sub)) == 0 {
					out = append(out, pileContents(p, pile, apps, res)...)
					continue
				}
			}
		case !e.Type().IsRegular():
			res.Skipped = append(res.Skipped, Skip{p, "normal dosya değil"})
			continue
		}
		if reason, ok := skipExts[strings.ToLower(filepath.Ext(name))]; ok {
			res.Skipped = append(res.Skipped, Skip{p, reason})
			continue
		}
		it, reason := newItem(p, e.IsDir())
		if reason != "" {
			res.Skipped = append(res.Skipped, Skip{p, reason})
			continue
		}
		it.From = pile
		out = append(out, it)
	}
	return out
}

func hashAll(ctx context.Context, items []Item, st *store.Store, staleBefore time.Time, res *Result, progress chan<- Progress) []Item {
	errs := make([]error, len(items))
	jobs := make(chan int)
	var (
		wg   sync.WaitGroup
		done atomic.Int64
	)
	for range min(runtime.NumCPU(), 8) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				errs[i] = hashItem(st, &items[i], staleBefore)
				n := done.Add(1)
				Send(progress, Progress{Phase: PhaseHash, Done: int(n), Total: len(items), Current: items[i].Path})
			}
		}()
	}
feed:
	for i := range items {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	kept := items[:0]
	for i, it := range items {
		if errs[i] != nil {
			res.Skipped = append(res.Skipped, Skip{it.Path, errs[i].Error()})
			continue
		}
		if it.Hash != "" {
			kept = append(kept, it)
		}
	}
	return kept
}

// hashItem reuses a file's stored hash when size and mtime are unchanged.
// Folders are always walked: their own mtime misses changes deeper inside.
func hashItem(st *store.Store, it *Item, staleBefore time.Time) error {
	if it.IsDir {
		if empty, err := isEmptyDir(it.Path); err != nil {
			return err
		} else if empty {
			it.Empty, it.Folder, it.Hash = true, &Folder{}, "empty:"+it.Path
			return nil
		}
		f, size, hash, err := walkFolder(it.Path, staleBefore)
		if err != nil {
			return err
		}
		if f.Files == 0 && len(f.Markers) == 0 && f.Projects == 0 {
			return errors.New("sadece gizli dosyalar var")
		}
		it.Folder, it.Size, it.Hash = &f, size, hash
		return nil
	}
	head, err := st.Lookup(it.Dev, it.Inode)
	if err != nil {
		return err
	}
	if head != nil && head.Size == it.Size && head.MTime == it.ModTime.UnixNano() {
		it.Hash = head.Hash
		return nil
	}
	f, err := os.Open(it.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	it.Hash = hex.EncodeToString(h.Sum(nil))
	return nil
}
