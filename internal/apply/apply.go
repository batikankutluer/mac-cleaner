// Package apply carries out a plan and reverts it. A move is a single
// rename(2) on the same volume, so tags, xattrs and dates survive, and it
// never overwrites: RENAME_EXCL makes the kernel refuse an existing target.
// The only thing ever deleted is a stale project's regenerable build folders
// (node_modules and the like), and only when the plan item asks for it.
package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/batikankutluer/mac-cleaner/internal/plan"
	"github.com/batikankutluer/mac-cleaner/internal/scan"
	"github.com/batikankutluer/mac-cleaner/internal/store"
)

type Failure struct{ Path, Reason string }

type Result struct {
	RunID   int64
	Moved   int   // items moved
	Files   int   // files inside them
	Bytes   int64 // their size, build folders aside
	Cleaned int   // build folders deleted
	Freed   int64 // bytes those took
	Removed int   // empty folders deleted
	Elapsed time.Duration
	Failed  []Failure
}

// Apply moves the selected items and records each move in the file's history.
// One failed item never stops the rest.
func Apply(ctx context.Context, st *store.Store, items []plan.Item, progress func(done, total int, current string)) (Result, error) {
	start := time.Now()
	var selected []plan.Item
	for _, it := range items {
		if it.Selected {
			selected = append(selected, it)
		}
	}
	runID, err := st.BeginRun("apply", []string{fmt.Sprintf("%d öğe", len(selected))})
	if err != nil {
		return Result{}, err
	}
	res := Result{RunID: runID}
	err = func() error {
		if err := st.SavePlan(runID, plan.Rows(selected)); err != nil {
			return err
		}
		for i, it := range selected {
			if err := ctx.Err(); err != nil {
				return err
			}
			if progress != nil {
				progress(i, len(selected), it.Path)
			}
			if err := unchanged(it); err != nil {
				res.Failed = append(res.Failed, Failure{it.Path, err.Error()})
				continue
			}
			if it.Remove {
				if err := removeEmpty(it.Path); err != nil {
					res.Failed = append(res.Failed, Failure{it.Path, err.Error()})
					continue
				}
				if err := st.RecordMove(runID, it.FileID, "removed", ""); err != nil {
					return fmt.Errorf("%s silindi ama kaydedilemedi: %w", it.Path, err)
				}
				if it.From != "" {
					pruneEmpty(filepath.Dir(it.Path), it.From)
				}
				res.Removed++
				continue
			}
			if it.Clean {
				if err := clean(st, runID, it, &res); err != nil {
					return err
				}
			}
			created, err := move(it.Path, it.Dst)
			for _, d := range created {
				if err := st.AddCreatedDir(runID, d); err != nil {
					return err
				}
			}
			if err != nil {
				res.Failed = append(res.Failed, Failure{it.Path, err.Error()})
				continue
			}
			if err := st.RecordMove(runID, it.FileID, "moved", it.Dst); err != nil {
				return fmt.Errorf("%s taşındı ama kaydedilemedi: %w", it.Dst, err)
			}
			if it.From != "" {
				pruneEmpty(filepath.Dir(it.Path), it.From)
			}
			res.Moved++
			res.Bytes += it.Size
			if it.Folder != nil {
				res.Files += it.Folder.Files
			} else {
				res.Files++
			}
		}
		return nil
	}()
	if ferr := st.FinishRun(runID, err); err == nil {
		err = ferr
	}
	res.Elapsed = time.Since(start)
	return res, err
}

// clean deletes the item's stale build folders after checking each one again:
// still a real folder inside the item, still next to the manifest that
// regenerates it. A folder that fails a check is kept and reported.
func clean(st *store.Store, runID int64, it plan.Item, res *Result) error {
	var deleted []scan.Artifact
	for _, a := range it.Folder.Artifacts {
		fi, err := os.Lstat(a.Path)
		switch {
		case err != nil:
			continue // already gone
		case !strings.HasPrefix(a.Path, it.Path+"/") || !fi.IsDir() || !scan.IsArtifact(a.Path):
			res.Failed = append(res.Failed, Failure{a.Path, "silinmedi: artık build klasörüne benzemiyor"})
			continue
		}
		if err := os.RemoveAll(a.Path); err != nil {
			res.Failed = append(res.Failed, Failure{a.Path, "tam silinemedi: " + err.Error()})
			continue
		}
		deleted = append(deleted, a)
		res.Cleaned++
		res.Freed += a.Bytes
	}
	if len(deleted) == 0 {
		return nil
	}
	meta, _ := json.Marshal(deleted)
	return st.RecordClean(runID, it.FileID, string(meta))
}

// Undo moves everything an apply run moved back where it was, newest first,
// then removes the folders that run created if they are empty again.
func Undo(st *store.Store, applyRunID int64) (Result, error) {
	start := time.Now()
	if done, err := st.Undone(applyRunID); err != nil {
		return Result{}, err
	} else if done {
		return Result{}, fmt.Errorf("uygulama #%d zaten geri alınmış", applyRunID)
	}
	moves, err := st.Moves(applyRunID)
	if err != nil {
		return Result{}, err
	}
	runID, err := st.BeginRun("undo", []string{"apply #" + strconv.FormatInt(applyRunID, 10)})
	if err != nil {
		return Result{}, err
	}
	res := Result{RunID: runID}
	err = func() error {
		for i := len(moves) - 1; i >= 0; i-- {
			m := moves[i]
			if m.Removed {
				if err := os.Mkdir(m.From, 0o755); err != nil && !os.IsExist(err) {
					res.Failed = append(res.Failed, Failure{m.From, err.Error()})
					continue
				}
				if err := st.RecordMove(runID, m.FileID, "restored", ""); err != nil {
					return err
				}
				res.Moved++
				continue
			}
			var s syscall.Stat_t
			if err := syscall.Lstat(m.To, &s); err != nil {
				res.Failed = append(res.Failed, Failure{m.To, "artık orada değil"})
				continue
			}
			if uint64(uint32(s.Dev)) != m.Dev || s.Ino != m.Inode {
				res.Failed = append(res.Failed, Failure{m.To, "yerinde başka bir şey var"})
				continue
			}
			if _, err := move(m.To, m.From); err != nil {
				res.Failed = append(res.Failed, Failure{m.To, err.Error()})
				continue
			}
			if err := st.RecordMove(runID, m.FileID, "undone", m.From); err != nil {
				return fmt.Errorf("%s geri taşındı ama kaydedilemedi: %w", m.From, err)
			}
			res.Moved++
		}
		dirs, err := st.CreatedDirs(applyRunID)
		if err != nil {
			return err
		}
		for _, d := range dirs {
			os.Remove(d) // only succeeds when empty, which is the point
		}
		return nil
	}()
	if ferr := st.FinishRun(runID, err); err == nil {
		err = ferr
	}
	res.Elapsed = time.Since(start)
	return res, err
}

// removeEmpty deletes a folder that holds nothing but .DS_Store files and
// empty folders, bottom up. The only files it ever deletes are .DS_Store;
// rmdir(2) refuses any folder with something else in it.
func removeEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errors.New("artık yerinde değil")
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		switch {
		case e.Name() == ".DS_Store" && e.Type().IsRegular():
			os.Remove(p)
		case e.IsDir():
			if err := removeEmpty(p); err != nil {
				return err
			}
		default:
			return errors.New("artık boş değil")
		}
	}
	if err := os.Remove(dir); err != nil {
		return errors.New("artık boş değil")
	}
	return nil
}

// pruneEmpty removes the folders a project leaves empty behind it, from dir
// up to and including the pile it was unpacked from. A folder holding nothing
// but Finder's .DS_Store counts as empty. Undo recreates folders as needed.
func pruneEmpty(dir, pile string) {
	for dir == pile || strings.HasPrefix(dir, pile+"/") {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		if len(entries) == 1 && entries[0].Name() == ".DS_Store" {
			os.Remove(filepath.Join(dir, ".DS_Store"))
		} else if len(entries) > 0 {
			return
		}
		if os.Remove(dir) != nil || dir == pile {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// unchanged checks that the item is still the one that was planned: same
// identity, and for files the same size and mtime.
func unchanged(it plan.Item) error {
	var s syscall.Stat_t
	if err := syscall.Lstat(it.Path, &s); err != nil {
		return errors.New("artık yerinde değil")
	}
	if uint64(uint32(s.Dev)) != it.Dev || s.Ino != it.Inode {
		return errors.New("tarandıktan sonra değiştirilmiş")
	}
	if !it.IsDir && (s.Size != it.Size || s.Mtimespec.Nano() != it.ModTime.UnixNano()) {
		return errors.New("tarandıktan sonra değiştirilmiş")
	}
	return nil
}

// move renames src to dst without ever replacing dst, creating dst's missing
// parent folders first. It returns the folders it created, outermost first.
func move(src, dst string) ([]string, error) {
	var created []string
	for d := filepath.Dir(dst); ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil || d == "/" {
			break
		}
		created = append([]string{d}, created...)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	err := unix.RenamexNp(src, dst, unix.RENAME_EXCL)
	switch {
	case errors.Is(err, unix.EEXIST):
		return created, errors.New("hedefte aynı adda bir şey var")
	case errors.Is(err, unix.EXDEV):
		return created, errors.New("hedef başka bir diskte; taşınmadı")
	}
	return created, err
}
