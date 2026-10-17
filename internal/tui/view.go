package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/batikankutluer/mac-cleaner/internal/format"
	"github.com/batikankutluer/mac-cleaner/internal/plan"
)

const (
	detailHeight = 7 // 5 lines + border
	chromeHeight = 4 // header, blank line, status, help
)

var (
	accent      = lipgloss.AdaptiveColor{Light: "#5B3FD0", Dark: "#B7A6FF"}
	accentStyle = lipgloss.NewStyle().Foreground(accent)
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(accent)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#7A7A85", Dark: "#7D7D88"})
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#5A5A66", Dark: "#9A9AA6"})
	cursorStyle = lipgloss.NewStyle().Bold(true).Background(lipgloss.AdaptiveColor{Light: "#E9E4FF", Dark: "#352C5C"})
	dupStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#A35200", Dark: "#FFB45E"})
	cleanStyle  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#1F7A4D", Dark: "#6EE7A8"})
	okStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#1F7A4D", Dark: "#6EE7A8"})
	errStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#B3261E", Dark: "#FF8A80"})
	boxStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.AdaptiveColor{Light: "#C9C2EA", Dark: "#4A4170"}).Padding(0, 1)
)

func (m model) View() string {
	switch m.phase {
	case phaseKey:
		return m.viewKey()
	case phaseScanning:
		return m.viewScanning()
	case phaseError:
		return "\n  " + errStyle.Render("Hata: "+m.err.Error()) + "\n\n  " + dimStyle.Render("q çık") + "\n"
	case phaseHistory:
		return m.viewHistory()
	case phaseApplying:
		return m.viewScanning()
	case phaseConfirm:
		return m.viewConfirm()
	case phaseDone:
		return m.viewDone()
	}
	return m.viewReview()
}

func (m model) viewKey() string {
	msg := ""
	if m.keyErr != "" {
		msg = "\n  " + errStyle.Render(m.keyErr)
	}
	return "\n  " + titleStyle.Render("mac-cleaner") + dimStyle.Render("  ·  karar: "+m.cfg.DeciderName) +
		"\n\n  " + lipgloss.NewStyle().Bold(true).Render("TypeSafe API anahtarı") +
		"\n  " + dimStyle.Render("Jev için gerekli. macOS Anahtar Zinciri'ne kaydedilir, bir daha sorulmaz.") +
		"\n  " + dimStyle.Render("Jev'e sadece Spotlight'ın tanımadığı dosyaların adı, uzantısı, boyutu ve kaynağı gider; içerik gitmez.") +
		"\n\n  " + m.input.View() + "\n" + msg +
		"\n\n  " + dimStyle.Render("enter kaydet · esc anahtarsız devam (sadece kurallar) · ctrl+c çık") + "\n"
}

func (m model) viewScanning() string {
	line := m.spin.View() + " " + plan.PhaseLabel(m.prog.Phase)
	switch p := m.prog; {
	case p.Bytes && p.Total > 0:
		line += dimStyle.Render(fmt.Sprintf("  %s / %s  %%%d", format.Size(int64(p.Done)), format.Size(int64(p.Total)), p.Done*100/p.Total))
	case p.Bytes && p.Done > 0:
		line += dimStyle.Render("  " + format.Size(int64(p.Done)))
	case p.Total > 0:
		line += dimStyle.Render(fmt.Sprintf("  %d/%d", p.Done, p.Total))
	}
	cur := ansi.Truncate(format.Tilde(format.Clean(m.prog.Current)), max(m.width-4, 10), "…")
	return "\n  " + titleStyle.Render("mac-cleaner") + dimStyle.Render("  ·  karar: "+m.cfg.DeciderName) +
		"\n\n  " + line + "\n  " + dimStyle.Render(cur) + "\n"
}

// catWidth shrinks the category column on narrow terminals.
func (m model) catWidth() int {
	return min(26, max(m.width/4, 12))
}

func (m model) listHeight() int {
	return max(m.height-chromeHeight-detailHeight, 3)
}

// viewNothing replaces the empty list when a scan finds nothing to move.
func (m model) viewNothing() string {
	out := []string{"", "  " + titleStyle.Render("mac-cleaner") + dimStyle.Render("  ·  tarama #"+fmt.Sprint(m.runID)), "",
		"  " + okStyle.Render("✓") + " Düzenlenecek bir şey kalmadı."}
	if n := len(m.res.Skipped); n > 0 {
		out = append(out, "  "+dimStyle.Render(fmt.Sprintf("%d öğeye bilerek dokunulmadı (uygulama, hedef ya da korunan klasörler).", n)))
	}
	if l := m.last; l != nil {
		out = append(out, "",
			fmt.Sprintf("  Son uygulama #%d  ·  %s", l.runID, l.finished.Local().Format("2 Jan 15:04")),
			fmt.Sprintf("    %d öğe taşındı  ·  %s disk alanı açıldı", l.moved, format.Size(l.freed)),
			dimStyle.Render("    Geri almak için: cleaner -undo"))
	}
	out = append(out, "", dimStyle.Render("  Başka bir klasörü taramak için: cleaner ~/klasör"), "", dimStyle.Render("  q çık"))
	for i := range out {
		out[i] = ansi.Truncate(out[i], m.width, "…")
	}
	return strings.Join(out, "\n")
}

func (m model) viewReview() string {
	if len(m.items) == 0 {
		return m.viewNothing()
	}
	var size, freeable int64
	selected, dups := 0, 0
	for _, it := range m.items {
		size += it.Size
		if it.Selected {
			selected++
			freeable += it.CleanBytes()
		}
		if it.Dup {
			dups++
		}
	}
	ws := "çalışma alanı bulunamadı"
	if m.cfg.Workspace != "" {
		ws = "çalışma alanı " + format.Tilde(m.cfg.Workspace)
	}
	header := titleStyle.Render("mac-cleaner") + dimStyle.Render(fmt.Sprintf("  ·  %d öğe · %s · %d seçili", len(m.items), format.Size(size), selected))
	if freeable > 0 {
		header += dimStyle.Render(" · ") + cleanStyle.Render(format.Size(freeable)+" temizlenecek")
	}
	header += dimStyle.Render(fmt.Sprintf(" · %d kopya · %d atlandı  ·  %s  ·  karar: %s  ·  tarama #%d",
		dups, len(m.res.Skipped), ws, m.cfg.DeciderName, m.runID))
	header = ansi.Truncate(header, m.width, "…")

	body := lipgloss.JoinHorizontal(lipgloss.Top, m.viewCategories(), m.viewList(m.width-m.catWidth()))
	help := dimStyle.Render(ansi.Truncate("↑↓ gez · ←→ kategori · space seç · a hepsi · c temizlik · enter geçmiş · u uygula · s kaydet · q çık", m.width, "…"))
	status := ansi.Truncate(m.status, m.width, "…")
	return strings.Join([]string{header, "", body, m.viewDetail(m.width), status, help}, "\n")
}

func (m model) viewCategories() string {
	lines := make([]string, 0, len(m.cats))
	for i, c := range m.cats {
		count := " " + fmt.Sprint(m.counts[c])
		name := ansi.Truncate(c, m.catWidth()-3-len(count), "…")
		if i == m.catIdx {
			lines = append(lines, titleStyle.Render("▸ "+name)+dimStyle.Render(count))
		} else {
			lines = append(lines, "  "+name+dimStyle.Render(count))
		}
	}
	return lipgloss.NewStyle().Width(m.catWidth()).MaxWidth(m.catWidth()).Height(m.listHeight()).MaxHeight(m.listHeight()).Render(strings.Join(lines, "\n"))
}

func (m model) viewList(width int) string {
	h := m.listHeight()
	end := min(m.offset+h, len(m.visible))

	destW := 0
	for _, i := range m.visible[m.offset:end] {
		destW = max(destW, lipgloss.Width(m.relDst(m.items[i])))
	}
	destW = min(destW+2, width/3)
	const sizeW, markW = 9, 3
	nameW := max(width-4-2-destW-1-sizeW-markW, 4)

	lines := make([]string, 0, h)
	if len(m.visible) == 0 {
		lines = append(lines, dimStyle.Render("Bu kategoride dosya yok."))
	}
	for row := m.offset; row < end; row++ {
		it := m.items[m.visible[row]]
		check := "[ ]"
		if it.Selected {
			check = "[x]"
		}
		name := pad(ansi.Truncate(displayName(it), nameW, "…"), nameW)
		dest := pad(ansi.Truncate("→ "+m.relDst(it), destW, "…"), destW)
		size := fmt.Sprintf("%*s", sizeW, format.Size(it.Size))
		dup, clean := " ", " "
		if it.Dup {
			dup = "⧉"
		}
		if it.CleanBytes() > 0 {
			clean = "✂"
		}
		switch {
		case row == m.cursor:
			lines = append(lines, cursorStyle.Render(pad(check+" "+name+"  "+dest+" "+size+" "+dup+clean, width)))
		case !it.Selected:
			lines = append(lines, dimStyle.Render(check+" "+name+"  "+dest+" "+size+" "+dup+clean))
		default:
			lines = append(lines, check+" "+name+"  "+dimStyle.Render(dest)+" "+size+" "+dupStyle.Render(dup)+cleanStyle.Render(clean))
		}
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m model) viewDetail(width int) string {
	inner := max(width-4, 10)
	lines := []string{"", "", "", "", ""}
	if it := m.current(); it != nil {
		decision := it.Category + dimStyle.Render("  ·  "+it.Source+"  ·  ") + format.Clean(it.Reason)
		typ := it.Meta.ContentType
		if typ == "" {
			typ = "bilinmiyor (Spotlight'ta yok)"
		}
		if f := it.Folder; f != nil {
			typ = fmt.Sprintf("klasör, %d dosya: %s", f.Files, strings.Join(format.TopExts(f.Exts, 4), ", "))
			if f.Truncated {
				typ += " (ilk 50.000 dosya)"
			}
		}
		meta := typ
		if host := plan.SourceHost(it.Meta.WhereFroms); host != "" {
			meta += dimStyle.Render("  ·  kaynak ") + format.Clean(host)
		}
		meta += dimStyle.Render("  ·  oluşturma ") + it.BirthTime.Format("2006-01-02") +
			dimStyle.Render("  ·  sha256 ") + it.Hash[:12]
		if it.Dup {
			meta += dupStyle.Render("  ·  kopyası var")
		}
		lines = []string{
			labelStyle.Render("Şu an  ") + format.Tilde(format.Clean(it.Path)),
			labelStyle.Render("Hedef  ") + format.Tilde(format.Clean(it.Dst)),
			labelStyle.Render("Karar  ") + decision,
			labelStyle.Render("Meta   ") + meta,
			labelStyle.Render("Temiz  ") + cleanLine(*it, m.cfg.StaleDays),
		}
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], inner, "…")
	}
	return boxStyle.Width(width - 2).Render(strings.Join(lines, "\n"))
}

func (m model) viewHistory() string {
	it := m.current()
	if it == nil {
		return ""
	}
	out := []string{
		titleStyle.Render("Geçmiş") + dimStyle.Render(fmt.Sprintf("  ·  %s  ·  kayıt #%d  ·  %d olay, en yeni üstte",
			displayName(*it), it.FileID, len(m.history))),
		"",
	}
	state := func(label string, p, hash string, size int64) string {
		return labelStyle.Render(label) + format.Tilde(format.Clean(p)) +
			dimStyle.Render(fmt.Sprintf("  ·  %s  ·  %s", format.Size(size), hash[:min(12, len(hash))]))
	}
	for i, e := range m.history {
		out = append(out, accentStyle.Render("●")+fmt.Sprintf(" #%d  %s", e.ID, e.Op)+
			dimStyle.Render(fmt.Sprintf("  ·  %s  ·  tarama #%d", e.CreatedAt.Local().Format("2006-01-02 15:04"), e.RunID)))
		if e.Old != nil {
			out = append(out, "│   "+state("eski  ", e.Old.Path, e.Old.Hash, e.Old.Size))
		}
		out = append(out, "│   "+state("yeni  ", e.New.Path, e.New.Hash, e.New.Size))
		if i < len(m.history)-1 {
			out = append(out, dimStyle.Render(fmt.Sprintf("│   ↓ prev_event_id = %d", e.PrevID)))
		}
	}
	for i := range out {
		out[i] = ansi.Truncate(out[i], m.width, "…")
	}
	if len(out) > m.height-2 {
		out = out[:max(m.height-2, 1)]
	}
	return strings.Join(out, "\n") + "\n\n" + dimStyle.Render("esc geri")
}

func cleanLine(it plan.Item, staleDays int) string {
	f := it.Folder
	switch {
	case f == nil:
		return dimStyle.Render("—")
	case len(f.Artifacts) == 0:
		if f.LastModified.IsZero() {
			return dimStyle.Render("silinecek build klasörü yok")
		}
		if staleDays > 0 && time.Since(f.LastModified) < time.Duration(staleDays)*24*time.Hour {
			return dimStyle.Render(fmt.Sprintf("yakın zamanda kullanılmış (son değişiklik %s), build klasörlerine dokunulmaz",
				f.LastModified.Format("2006-01-02")))
		}
		return dimStyle.Render("silinecek build klasörü yok · son değişiklik " + f.LastModified.Format("2006-01-02"))
	}
	var names []string
	var total int64
	for _, a := range f.Artifacts {
		rel, _ := filepath.Rel(it.Path, a.Path)
		names = append(names, rel)
		total += a.Bytes
	}
	state := cleanStyle.Render(format.Size(total) + " açılacak")
	if !it.Clean {
		state = dimStyle.Render("kapalı (" + format.Size(total) + ")")
	}
	return state + dimStyle.Render(fmt.Sprintf("  ·  son değişiklik %s  ·  c aç/kapa  ·  ", f.LastModified.Format("2006-01-02"))) +
		strings.Join(names, ", ")
}

func (m model) viewConfirm() string {
	type dest struct {
		n     int
		bytes int64
	}
	var (
		n, files, cleanDirs int
		bytes, freed        int64
		byDir               = map[string]*dest{}
		order               []string
	)
	for _, it := range m.items {
		if !it.Selected {
			continue
		}
		n++
		bytes += it.Size
		if it.Folder != nil {
			files += it.Folder.Files
		} else {
			files++
		}
		if b := it.CleanBytes(); b > 0 {
			freed += b
			cleanDirs += len(it.Folder.Artifacts)
		}
		dir := format.Tilde(filepath.Dir(it.Dst))
		if byDir[dir] == nil {
			byDir[dir] = &dest{}
			order = append(order, dir)
		}
		byDir[dir].n++
		byDir[dir].bytes += it.Size
	}
	out := []string{
		"",
		"  " + titleStyle.Render("Planı uygula"),
		"",
		fmt.Sprintf("  %d öğe taşınacak  ·  içlerinde %d dosya  ·  %s", n, files, format.Size(bytes)),
	}
	if freed > 0 {
		out = append(out, "  "+cleanStyle.Render(fmt.Sprintf("%d build klasörü silinecek  ·  %s alan açılacak", cleanDirs, format.Size(freed)))+
			dimStyle.Render("  (geri alınamaz; npm install vb. ile yeniden oluşur)"))
	}
	out = append(out, "")
	maxRows := max(m.height-len(out)-6, 3)
	for i, dir := range order {
		if i == maxRows {
			out = append(out, dimStyle.Render(fmt.Sprintf("    … ve %d klasör daha", len(order)-i)))
			break
		}
		d := byDir[dir]
		out = append(out, fmt.Sprintf("    → %s  %s", dir, dimStyle.Render(fmt.Sprintf("%d öğe · %s", d.n, format.Size(d.bytes)))))
	}
	out = append(out, "",
		dimStyle.Render("  Taşımalar geri alınabilir: cleaner -undo. Hiçbir şeyin üzerine yazılmaz."),
		"",
		"  "+lipgloss.NewStyle().Bold(true).Render("e")+" uygula  ·  "+lipgloss.NewStyle().Bold(true).Render("h")+" vazgeç")
	for i := range out {
		out[i] = ansi.Truncate(out[i], m.width, "…")
	}
	return strings.Join(out, "\n")
}

func (m model) viewDone() string {
	r := m.applied
	out := []string{"", "  " + titleStyle.Render("Bitti") + dimStyle.Render(fmt.Sprintf("  ·  %s  ·  uygulama #%d", format.Duration(r.Elapsed), r.RunID)), ""}
	if m.applyErr != nil {
		out = append(out, "  "+errStyle.Render("Hata: "+m.applyErr.Error()), "")
	}
	out = append(out, "  "+okStyle.Render("✓")+fmt.Sprintf(" %d öğe taşındı  ·  içlerinde %d dosya  ·  %s", r.Moved, r.Files, format.Size(r.Bytes)))
	if r.Cleaned > 0 {
		out = append(out, "  "+okStyle.Render("✓")+cleanStyle.Render(fmt.Sprintf(" %d build klasörü silindi  ·  %s disk alanı açıldı", r.Cleaned, format.Size(r.Freed))))
	} else {
		out = append(out, "  "+dimStyle.Render("· disk alanı açılmadı (taşımak yer kazandırmaz; silinecek build klasörü yoktu)"))
	}
	if len(r.Failed) > 0 {
		out = append(out, "", "  "+errStyle.Render(fmt.Sprintf("✗ %d öğe atlandı:", len(r.Failed))))
		maxRows := max(m.height-len(out)-6, 3)
		for i, f := range r.Failed {
			if i == maxRows {
				out = append(out, dimStyle.Render(fmt.Sprintf("    … ve %d tane daha", len(r.Failed)-i)))
				break
			}
			out = append(out, "    "+format.Tilde(format.Clean(f.Path))+dimStyle.Render("  "+f.Reason))
		}
	}
	out = append(out, "",
		dimStyle.Render("  Geri almak için: cleaner -undo  (silinen build klasörleri geri gelmez)"),
		"",
		dimStyle.Render("  r yeniden tara  ·  q çık"))
	for i := range out {
		out[i] = ansi.Truncate(out[i], m.width, "…")
	}
	return strings.Join(out, "\n")
}

func (m model) relDst(it plan.Item) string {
	return format.Tilde(filepath.Dir(it.Dst))
}

func displayName(it plan.Item) string {
	name := format.Clean(filepath.Base(it.Path))
	if it.IsDir {
		name += "/"
	}
	if it.From != "" {
		name = format.Clean(filepath.Base(it.From)) + " › " + name
	}
	return name
}

func pad(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}
