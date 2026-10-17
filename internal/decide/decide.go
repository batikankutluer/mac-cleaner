// Package decide picks a category for each file. A Decider looks only at
// metadata, never file contents, and All runs one over many files in parallel.
package decide

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
)

type Input struct {
	Name          string
	ContentType   string
	TypeTree      []string
	ScreenCapture bool
	SourceHost    string
	Size          int64
	Folder        *FolderInfo // set for directories
	Empty         bool        // an empty folder, to be removed
	InWorkspace   bool        // a folder already in the workspace: a project by definition
}

type FolderInfo struct {
	Files    int
	Exts     map[string]int   // lower-case extension ("" for none) → count
	ExtBytes map[string]int64 // same keys → total size, to break ties
	Markers  []string         // project markers such as go.mod or .git
	Projects int              // immediate subfolders that are projects
	Signals  []string         // marker and stack-hint names seen anywhere inside
}

// IsFolder reports whether the input is a plain folder. Packages such as .app
// bundles or .pages documents are directories too, but are judged like files.
func (in Input) IsFolder() bool {
	return in.Folder != nil && !slices.Contains(in.TypeTree, "com.apple.package")
}

type Decision struct {
	Category   string
	Source     string // "kural" or the model name
	Reason     string
	Confidence float64
}

type Decider interface {
	Decide(ctx context.Context, in Input) (Decision, error)
}

const (
	Projects    = "Projeler"
	Screenshots = "Ekran Görüntüleri"
	Documents   = "Belgeler"
	Images      = "Görseller"
	Videos      = "Videolar"
	Audio       = "Ses"
	Archives    = "Arşivler"
	Installers  = "Kurulumlar"
	Code        = "Kod"
	Fonts       = "Fontlar"
	Other       = "Diğer"
	Empty       = "Boş Klasörler"
)

// Categories is the fixed option set, in display order. The descriptions are
// what a decision model sees as each option's meaning.
var Categories = []struct{ Name, Description string }{
	{Projects, "A software project folder: a git repository or source tree someone works on"},
	{Screenshots, "Only screenshots and screen recordings captured on this Mac, not other images"},
	{Documents, "PDFs, office documents, spreadsheets, presentations, e-books, plain text and notes"},
	{Images, "Photos, pictures, frames, banners and other graphics"},
	{Videos, "Video files and movies"},
	{Audio, "Music, podcasts and other audio recordings"},
	{Archives, "Compressed archives such as zip, rar, 7z and tarballs"},
	{Installers, "Only app installers and disk images: .dmg, .pkg, .mpkg and .iso files"},
	{Code, "Developer files: source code, scripts, configs, build files, Docker and CI artifacts, JSON or YAML"},
	{Fonts, "Font files"},
	{Other, "Anything that fits none of the other categories"},
	{Empty, "An empty folder"},
}

func IsCategory(name string) bool {
	for _, c := range Categories {
		if c.Name == name {
			return true
		}
	}
	return false
}

// All runs d over inputs with up to workers concurrent calls. A failed call
// becomes Other with the error as its reason, so one bad file never stops a run.
func All(ctx context.Context, d Decider, inputs []Input, workers int, onDone func(done int)) []Decision {
	out := make([]Decision, len(inputs))
	jobs := make(chan int)
	var (
		wg   sync.WaitGroup
		done atomic.Int64
	)
	for range max(workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				dec, err := d.Decide(ctx, inputs[i])
				if err != nil {
					dec = Decision{Category: Other, Source: "hata", Reason: err.Error()}
				}
				out[i] = dec
				if onDone != nil {
					onDone(int(done.Add(1)))
				}
			}
		}()
	}
feed:
	for i := range inputs {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	return out
}

// Hybrid trusts Rules when they are certain (Spotlight metadata) and asks
// Model only otherwise. The model wins only when it is more confident than the
// rules and at least MinConfidence. Measured on real files, the model
// misfiled things metadata got right (a PNG as an installer at confidence 1.0),
// so it is never allowed to overrule metadata.
type Hybrid struct {
	Model         Decider
	Rules         Decider
	MinConfidence float64
}

func (h Hybrid) Decide(ctx context.Context, in Input) (Decision, error) {
	rule, err := h.Rules.Decide(ctx, in)
	if err != nil || rule.Confidence >= certain {
		return rule, err
	}
	dec, err := h.Model.Decide(ctx, in)
	switch {
	case err != nil:
		rule.Reason += fmt.Sprintf(" (model hatası: %v)", err)
		return rule, nil
	case dec.Confidence < max(h.MinConfidence, rule.Confidence):
		rule.Reason += fmt.Sprintf(" (model %s dedi, güven %.2f yetmedi)", dec.Category, dec.Confidence)
		return rule, nil
	}
	if dec.Category != rule.Category {
		dec.Reason += fmt.Sprintf(" · kurallar %s derdi", rule.Category)
	}
	return dec, nil
}
