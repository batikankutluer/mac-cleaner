// cleaner scans the loose files in your Mac's Downloads, Desktop and Documents
// folders, decides where each belongs from its metadata and shows the plan in
// a TUI. It does not move anything yet.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	neturl "net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/batikankutluer/mac-cleaner/internal/apply"
	"github.com/batikankutluer/mac-cleaner/internal/decide"
	"github.com/batikankutluer/mac-cleaner/internal/engine"
	"github.com/batikankutluer/mac-cleaner/internal/format"
	"github.com/batikankutluer/mac-cleaner/internal/keychain"
	"github.com/batikankutluer/mac-cleaner/internal/plan"
	"github.com/batikankutluer/mac-cleaner/internal/scan"
	"github.com/batikankutluer/mac-cleaner/internal/store"
	"github.com/batikankutluer/mac-cleaner/internal/tui"
	"github.com/batikankutluer/mac-cleaner/internal/workspace"
)

// version is set at build time from the git tag (see Makefile). A plain
// `go install ...@vX.Y.Z` leaves it unset and the module version is used.
var version = ""

func currentVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hata:", err)
		os.Exit(1)
	}
}

func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	defaultDB, err := store.DefaultPath()
	if err != nil {
		return err
	}
	appDir := filepath.Dir(defaultDB)
	var (
		dbPath    = flag.String("db", defaultDB, "SQLite veritabanı")
		model     = flag.String("model", "", "decision model (varsayılan jev-1.14.0; -local ile laya:multilingual)")
		modelURL  = flag.String("model-url", "", "TypeSafe yerine başka bir System One sunucusu")
		local     = flag.Bool("local", false, "bulut yerine dahili Ollaya motoru ve yerel model kullan")
		rulesOnly = flag.Bool("rules-only", false, "model kullanma, sadece kurallarla karar ver")
		resetKey  = flag.Bool("reset-key", false, "kayıtlı TypeSafe anahtarını Anahtar Zinciri'nden sil")
		staleDays = flag.Int("stale-days", 90, "bu kadar gündür dokunulmamış projelerin node_modules gibi build klasörleri taşımadan önce silinir (0: kapalı)")
		undo      = flag.Bool("undo", false, "son uygulamayı geri al")
		showVer   = flag.Bool("version", false, "sürümü yazdır")
		wsFlag    = flag.String("workspace", "", "projelerin gideceği çalışma alanı (bir kez verilince hatırlanır; boşsa otomatik bulunur)")
		minConf   = flag.Float64("min-confidence", 0.5, "modelin bu güvenin altındaki kararlarında kurallara dön")
		workers   = flag.Int("workers", 8, "paralel karar sayısı")
		print     = flag.Bool("print", false, "TUI açmadan planı yazdır")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "cleaner %s\n\nkullanım: cleaner [bayraklar] [klasör...]\n\n"+
			"Klasör verilmezse ~/Downloads, ~/Desktop, ~/Documents ve ev dizininin kökü taranır.\n"+
			"En üst seviyedeki dosya ve klasörlere bakılır; klasörler bütün olarak ele alınır.\n"+
			"Plan TUI'de incelenir, u ile onaylanınca uygulanır; cleaner -undo geri alır.\n"+
			"Proje klasörleri (go.mod, .git, ...) çalışma alanında web/, blockchain/ gibi\n"+
			"gruplara önerilir; çalışma alanı otomatik bulunur ve hatırlanır. Uzun süredir\n"+
			"dokunulmamış projelerin node_modules gibi build klasörleri taşımadan önce silinir.\n"+
			"Videolar ~/Movies, görseller ~/Pictures, sesler ~/Music, kurulumlar\n"+
			"~/Downloads/Kurulumlar, geri kalanı ~/Documents/<Kategori> altına önerilir.\n\n"+
			"Kararları TypeSafe Jev verir; API anahtarı ilk açılışta TUI'de sorulur ve\n"+
			"Anahtar Zinciri'ne kaydedilir. Jev'e sadece Spotlight'ın tanımadığı dosyaların\n"+
			"adı, uzantısı, boyutu, türü ve indirildiği site gider; içerik hiç gönderilmez.\n\n"+
			"-local ile ilk çalıştırmada Ollaya %s ve yerel model indirilir:\n  %s\n\n",
			currentVersion(), engine.Version, filepath.Join(appDir, "ollaya"))
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVer {
		fmt.Println("cleaner", currentVersion())
		return nil
	}
	if *resetKey {
		if err := keychain.Delete(); err != nil {
			return err
		}
		fmt.Println("TypeSafe anahtarı silindi; bir sonraki açılışta tekrar sorulur.")
		return nil
	}

	roots := flag.Args()
	if len(roots) == 0 {
		roots = []string{filepath.Join(home, "Downloads"), filepath.Join(home, "Desktop"), filepath.Join(home, "Documents"), home}
	}
	for i, r := range roots {
		if roots[i], err = filepath.Abs(r); err != nil {
			return err
		}
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ws, err := resolveWorkspace(st, home, *wsFlag)
	if err != nil {
		return err
	}
	if *undo {
		return undoLast(st)
	}
	opts := plan.Options{Roots: roots, Layout: plan.DefaultLayout(home, ws), Protect: protected(home),
		Decider: decide.Rules{}, Workers: *workers, StaleDays: *staleDays}
	deciderName := "kurallar"
	hybrid := func(s1 *decide.SystemOne) decide.Decider {
		return decide.Hybrid{Model: s1, Rules: decide.Rules{}, MinConfidence: *minConf}
	}
	var keyPrompt *tui.KeyPrompt
	switch {
	case *rulesOnly:
	case *local:
		name := cmp.Or(*model, "laya:multilingual")
		eng := engine.New(filepath.Join(appDir, "ollaya"))
		defer eng.Stop()
		opts.Setup = func(ctx context.Context, progress chan<- scan.Progress) (decide.Decider, error) {
			err := eng.Start(ctx, name, func(p engine.Progress) {
				scan.Send(progress, scan.Progress{Phase: p.Step, Done: int(p.Done), Total: int(p.Total), Bytes: true})
			})
			if err != nil {
				return nil, err
			}
			// The first request loads the model into memory. Do it once here so the
			// parallel decisions that follow don't all queue behind the load.
			scan.Send(progress, scan.Progress{Phase: plan.PhaseModelLoad})
			s1 := decide.NewSystemOne(eng.URL, name)
			if _, err := s1.Decide(ctx, decide.Input{Name: "warmup.txt"}); err != nil {
				return nil, fmt.Errorf("model yüklenemedi: %w", err)
			}
			return hybrid(s1), nil
		}
		deciderName = fmt.Sprintf("%s (dahili Ollaya) + kurallar, %d paralel", name, *workers)
	default:
		url := cmp.Or(*modelURL, os.Getenv("TYPESAFE_BASE_URL"), "https://api.typesafe.ai")
		name := cmp.Or(*model, "jev-1.14.0")
		// TYPESAFE_API_KEY goes wherever the SDK would send it; the keychain key
		// only ever goes to TypeSafe itself.
		key := os.Getenv("TYPESAFE_API_KEY")
		toTypeSafe := isTypeSafe(url)
		if key == "" && toTypeSafe {
			if key, err = keychain.Get(); err != nil {
				return err
			}
			keyPrompt = &tui.KeyPrompt{Needed: key == "", Save: func(k string) error {
				if err := keychain.Set(k); err != nil {
					return err
				}
				key = strings.TrimSpace(k)
				return nil
			}}
		}
		if *print && key == "" && toTypeSafe {
			return errors.New("TypeSafe anahtarı yok: bir kez `cleaner` ile TUI'yi açıp gir ya da TYPESAFE_API_KEY ayarla")
		}
		s1 := decide.NewSystemOne(url, name)
		// One real request before scanning, so a bad URL or key fails up front
		// instead of quietly becoming a rules fallback on every file.
		opts.Setup = func(ctx context.Context, progress chan<- scan.Progress) (decide.Decider, error) {
			s1.APIKey = key
			scan.Send(progress, scan.Progress{Phase: plan.PhaseModelCheck})
			if _, err := s1.Decide(ctx, decide.Input{Name: "warmup.txt"}); err != nil {
				return nil, fmt.Errorf("%s modeline ulaşılamadı: %w", name, err)
			}
			return hybrid(s1), nil
		}
		deciderName = fmt.Sprintf("%s + kurallar, %d paralel", name, *workers)
	}
	if *print {
		return printPlan(st, opts)
	}
	return tui.Run(tui.Config{Options: opts, Store: st, DeciderName: deciderName, Key: keyPrompt, Workspace: ws})
}

// protected lists folders in home that other software relies on staying put:
// macOS's own, the Go workspace, ~/bin, and anything on PATH.
func protected(home string) []string {
	out := []string{}
	for _, name := range []string{"Library", "Applications", "Public", "bin", "go"} {
		out = append(out, filepath.Join(home, name))
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		out = append(out, filepath.SplitList(gopath)...)
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.HasPrefix(dir, home+"/") {
			out = append(out, dir)
		}
	}
	return out
}

func undoLast(st *store.Store) error {
	runID, err := st.LastUndoableApply()
	if err != nil {
		return err
	}
	if runID == 0 {
		return errors.New("geri alınacak bir uygulama yok")
	}
	res, err := apply.Undo(st, runID)
	if err != nil {
		return err
	}
	fmt.Printf("uygulama #%d geri alındı · %s · %d öğe yerine döndü\n", runID, format.Duration(res.Elapsed), res.Moved)
	for _, f := range res.Failed {
		fmt.Printf("  ✗ %s  (%s)\n", format.Tilde(format.Clean(f.Path)), f.Reason)
	}
	fmt.Println("silinen build klasörleri geri gelmez; npm install vb. ile yeniden oluşur")
	return nil
}

// resolveWorkspace returns where projects go: the -workspace flag if given,
// else the remembered one, else the best guess from Detect, which is then
// remembered. "" means none was found.
func resolveWorkspace(st *store.Store, home, override string) (string, error) {
	if override != "" {
		dir, err := filepath.Abs(override)
		if err != nil {
			return "", err
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return "", fmt.Errorf("çalışma alanı bir klasör değil: %s", dir)
		}
		return dir, st.SetSetting("workspace", dir)
	}
	saved, err := st.Setting("workspace")
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(saved); saved != "" && err == nil && fi.IsDir() {
		return saved, nil
	}
	found := workspace.Detect(home)
	if len(found) == 0 {
		return "", nil
	}
	fmt.Fprintf(os.Stderr, "çalışma alanı bulundu: %s (%d proje); değiştirmek için -workspace\n",
		format.Tilde(found[0].Path), found[0].Projects)
	return found[0].Path, st.SetSetting("workspace", found[0].Path)
}

func isTypeSafe(raw string) bool {
	u, err := neturl.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() == "api.typesafe.ai"
}

func printPlan(st *store.Store, opts plan.Options) error {
	// Cancel on Ctrl+C so deferred cleanup (stopping the model server) still runs.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	progress := make(chan scan.Progress, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		last := ""
		for p := range progress {
			if p.Phase != last {
				last = p.Phase
				fmt.Fprintln(os.Stderr, ">>>", plan.PhaseLabel(p.Phase))
			}
		}
	}()
	res, items, runID, err := plan.Run(ctx, st, opts, progress)
	close(progress)
	<-done
	if err != nil {
		return err
	}

	fmt.Printf("tarama #%d: %d dosya, %d atlandı\n\n", runID, len(items), len(res.Skipped))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KATEGORİ\tDOSYA\tHEDEF\tKARAR\tBOYUT\tKOPYA")
	for _, it := range items {
		dup := ""
		if it.Dup {
			dup = "⧉"
		}
		src, dst := format.Tilde(format.Clean(it.Path)), format.Tilde(format.Clean(it.Dst))
		if it.IsDir {
			src, dst = src+"/", dst+"/"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s: %s\t%s\t%s\n", it.Category, src, dst,
			it.Source, format.Clean(it.Reason), format.Size(it.Size), dup)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	var projects, dirs int
	var freeable int64
	for _, it := range items {
		if b := it.CleanBytes(); b > 0 {
			projects++
			dirs += len(it.Folder.Artifacts)
			freeable += b
		}
	}
	if freeable > 0 {
		fmt.Printf("\n%d projede %d build klasörü silinecek: %s açılacak\n", projects, dirs, format.Size(freeable))
	}
	if len(res.Skipped) > 0 {
		fmt.Println("\natlananlar:")
		for _, s := range res.Skipped {
			fmt.Printf("  %s  (%s)\n", format.Tilde(format.Clean(s.Path)), s.Reason)
		}
	}
	return nil
}
