package decide

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRules(t *testing.T) {
	cases := []struct {
		in   Input
		want string
	}{
		{Input{Name: "a.pdf", ContentType: "com.adobe.pdf", TypeTree: []string{"com.adobe.pdf", "public.data"}}, Documents},
		{Input{Name: "song.mp3", TypeTree: []string{"public.mp3", "public.audio", "public.audiovisual-content"}}, Audio},
		{Input{Name: "clip.mp4", TypeTree: []string{"public.mpeg-4", "public.movie", "public.audiovisual-content"}}, Videos},
		{Input{Name: "main.go", TypeTree: []string{"public.source-code", "public.plain-text", "public.text"}}, Code},
		{Input{Name: "book.epub", TypeTree: []string{"org.idpf.epub-container", "public.zip-archive"}}, Documents},
		{Input{Name: "x.png", ScreenCapture: true, TypeTree: []string{"public.image"}}, Screenshots},
		// NFD-encoded name, as some macOS apps write it.
		{Input{Name: "Ekran Görüntüsü 2026-10-01.png"}, Screenshots},
		{Input{Name: "Setup.DMG"}, Installers},
		{Input{Name: "mystery"}, Other},
	}
	for _, c := range cases {
		got, _ := Rules{}.Decide(context.Background(), c.in)
		if got.Category != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.in.Name, got.Category, got.Reason, c.want)
		}
	}
}

func TestRulesFolders(t *testing.T) {
	folder := func(exts map[string]int, markers []string, projects int) Input {
		n := 0
		for _, c := range exts {
			n += c
		}
		return Input{Name: "x", TypeTree: []string{"public.folder"}, Folder: &FolderInfo{Files: n, Exts: exts, Markers: markers, Projects: projects}}
	}
	cases := []struct {
		name string
		in   Input
		want string
		conf float64
	}{
		{"go project", folder(map[string]int{".go": 3}, []string{"go.mod"}, 0), Projects, certain},
		{"git repo of images", folder(map[string]int{".png": 50}, []string{".git"}, 0), Projects, certain},
		{"group of projects", folder(map[string]int{".ts": 9}, nil, 2), Projects, certain},
		{"course", folder(map[string]int{".pdf": 5, ".mp4": 4, ".jpg": 1}, nil, 0), Documents, extensionOnly},
		{"downloads", folder(map[string]int{".mp4": 4, ".mp3": 3}, nil, 0), Videos, extensionOnly},
		{"tie goes to bytes", Input{Name: "x", Folder: &FolderInfo{Files: 2, Exts: map[string]int{".jpg": 1, ".mp4": 1},
			ExtBytes: map[string]int64{".jpg": 200 << 10, ".mp4": 23 << 20}}}, Videos, extensionOnly},
		{"scripts", folder(map[string]int{".py": 1, ".sh": 1}, nil, 0), Projects, certain},
		{"saved web page", folder(map[string]int{".js": 13, ".png": 12}, nil, 0), Code, extensionOnly},
		{"unknown", folder(map[string]int{".f0": 9}, nil, 0), Other, noMatch},
		{"empty", folder(nil, nil, 0), Other, noMatch},
	}
	for _, c := range cases {
		got, _ := Rules{}.Decide(context.Background(), c.in)
		if got.Category != c.want || got.Confidence != c.conf {
			t.Errorf("%s: got %s %.1f (%s), want %s %.1f", c.name, got.Category, got.Confidence, got.Reason, c.want, c.conf)
		}
	}

	// A package is a directory but is judged like a file.
	app := Input{Name: "Example.app", TypeTree: []string{"com.apple.application-bundle", "com.apple.package", "public.directory"},
		Folder: &FolderInfo{Files: 10, Exts: map[string]int{".dylib": 10}}}
	if got, _ := (Rules{}).Decide(context.Background(), app); got.Category != Installers {
		t.Errorf("app bundle: got %s (%s)", got.Category, got.Reason)
	}
}

func TestSystemOne(t *testing.T) {
	var calls, inFlight, peak atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(20 * time.Millisecond)

		var req s1Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		q := req.Questions["category"]
		_, offersProjects := q.Criteria[Projects]
		_, offersEmpty := q.Criteria[Empty]
		if q.Type != "choice" || offersProjects || offersEmpty || len(q.Criteria) != len(Categories)-2 || req.State["file name"] == "" {
			http.Error(w, "bad request", http.StatusUnprocessableEntity)
			return
		}
		conf := map[string]float64{"unsure": 0.1, "clip.mov": 0.6, "loud.mov": 0.9}[req.State["file name"]]
		if conf == 0 {
			conf = 0.9
		}
		json.NewEncoder(w).Encode(map[string]any{
			"model": req.Model,
			"answers": map[string]any{
				"category": map[string]any{"type": "choice", "choice": Code, "confidence": conf},
			},
		})
	}))
	defer srv.Close()

	h := Hybrid{Model: NewSystemOne(srv.URL, "winnow:e4b"), Rules: Rules{}, MinConfidence: 0.5}
	inputs := []Input{
		{Name: "photo.png", ContentType: "public.png", TypeTree: []string{"public.png", "public.image"}},
		{Name: "unsure"},
		{Name: "clip.mov"},
		{Name: "loud.mov"},
	}
	for range 6 {
		inputs = append(inputs, Input{Name: "x.dockerbuild"})
	}
	got := All(context.Background(), h, inputs, 4, nil)

	want := []struct{ category, source string }{
		{Images, "kural"},    // metadata is final, the model is never asked
		{Other, "kural"},     // no rule, but the model is unsure
		{Videos, "kural"},    // extension only, model not more confident than 0.7
		{Code, "winnow:e4b"}, // extension only, model confident enough
		{Code, "winnow:e4b"}, // no rule at all
	}
	for i, w := range want {
		if got[i].Category != w.category || got[i].Source != w.source {
			t.Errorf("%s: got %s from %s (%s), want %s from %s",
				inputs[i].Name, got[i].Category, got[i].Source, got[i].Reason, w.category, w.source)
		}
	}
	if calls.Load() != int64(len(inputs)-1) {
		t.Errorf("model asked %d times, want %d (never for metadata)", calls.Load(), len(inputs)-1)
	}
	if peak.Load() < 2 {
		t.Errorf("requests were not parallel (peak %d)", peak.Load())
	}
}

type failing struct{}

func (failing) Decide(context.Context, Input) (Decision, error) {
	return Decision{}, errors.New("down")
}

func TestHybridModelError(t *testing.T) {
	got, err := Hybrid{Model: failing{}, Rules: Rules{}, MinConfidence: 0.5}.Decide(context.Background(), Input{Name: "a.zip"})
	if err != nil || got.Category != Archives || got.Source != "kural" || !strings.Contains(got.Reason, "model hatası") {
		t.Errorf("got %+v, %v", got, err)
	}
}
