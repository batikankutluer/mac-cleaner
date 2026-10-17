package decide

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Rules classifies by Spotlight metadata first and file extension second. Its
// confidence says how strong the evidence was: certain for metadata and
// macOS's own screenshot names, weaker for a bare extension, none otherwise.
type Rules struct{}

const (
	certain       = 1.0
	extensionOnly = 0.7
	noMatch       = 0.0
)

type rule struct {
	category string
	utis     []string // a trailing "." matches any UTI with that prefix
	exts     []string
}

// First match wins, so order matters: documents that are secretly zip files
// (epub, iWork, OOXML) come before archives, installers before archives, code
// before documents (source code conforms to public.text), and audio before
// video (public.audio conforms to public.audiovisual-content).
var rules = []rule{
	{Installers, []string{"com.apple.disk-image", "com.apple.installer-package-archive", "com.apple.installer-meta-package", "com.apple.application-bundle"},
		[]string{".dmg", ".pkg", ".mpkg", ".iso"}},
	{Documents, []string{"org.idpf.epub-container", "com.apple.iwork.", "org.openxmlformats.", "org.oasis-open.opendocument."},
		[]string{".epub", ".pages", ".numbers", ".key", ".docx", ".xlsx", ".pptx", ".odt", ".ods", ".odp"}},
	{Images, []string{"public.image"},
		[]string{".jpg", ".jpeg", ".png", ".gif", ".heic", ".heif", ".webp", ".tif", ".tiff", ".bmp", ".svg", ".raw", ".cr2", ".nef", ".arw", ".dng"}},
	{Audio, []string{"public.audio"},
		[]string{".mp3", ".m4a", ".wav", ".aac", ".flac", ".ogg", ".aif", ".aiff", ".opus"}},
	{Videos, []string{"public.movie", "public.video", "public.audiovisual-content"},
		[]string{".mp4", ".mov", ".mkv", ".avi", ".webm", ".m4v"}},
	{Archives, []string{"public.archive", "public.zip-archive", "com.pkware.zip-archive", "org.gnu.gnu-zip-archive", "com.rarlab.rar-archive", "org.7-zip.7-zip-archive"},
		[]string{".zip", ".rar", ".7z", ".tar", ".gz", ".tgz", ".bz2", ".xz"}},
	{Fonts, []string{"public.font"},
		[]string{".ttf", ".otf", ".woff", ".woff2"}},
	{Code, []string{"public.source-code", "public.script", "public.json", "public.xml", "public.yaml"},
		[]string{".go", ".py", ".pyc", ".ipynb", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx", ".vue", ".svelte", ".json",
			".sh", ".zsh", ".sql", ".yaml", ".yml", ".toml", ".ini", ".lock", ".xml", ".swift", ".m", ".mm", ".c", ".h",
			".cc", ".cpp", ".hpp", ".o", ".rs", ".java", ".kt", ".kts", ".gradle", ".scala", ".rb", ".php", ".cs",
			".ex", ".exs", ".erl", ".sol", ".lua", ".dart", ".zig", ".hs", ".clj", ".jl", ".r", ".qml"}},
	{Documents, []string{"com.adobe.pdf", "public.composite-content", "public.presentation", "public.spreadsheet", "public.text"},
		[]string{".pdf", ".doc", ".xls", ".ppt", ".txt", ".md", ".rtf", ".csv"}},
}

var screenshotName = regexp.MustCompile(`^(Screenshot|Screen Shot|Screen Recording|Ekran Resmi|Ekran Görüntüsü|Ekran Kaydı)[ _]`)

func (Rules) Decide(_ context.Context, in Input) (Decision, error) {
	decision := func(category, reason string, confidence float64) (Decision, error) {
		return Decision{Category: category, Source: "kural", Reason: reason, Confidence: confidence}, nil
	}
	if in.Empty {
		return decision(Empty, "boş klasör", certain)
	}
	if in.InWorkspace {
		return decision(Projects, "çalışma alanında", certain)
	}
	if in.IsFolder() {
		return decision(decideFolder(in.Folder))
	}
	if in.ScreenCapture {
		return decision(Screenshots, "meta: ekran görüntüsü", certain)
	}
	name := norm.NFC.String(in.Name)
	if screenshotName.MatchString(name) {
		return decision(Screenshots, "isim: ekran görüntüsü", certain)
	}
	types := in.TypeTree
	if in.ContentType != "" && !slices.Contains(types, in.ContentType) {
		types = append([]string{in.ContentType}, types...)
	}
	for _, r := range rules {
		for _, u := range r.utis {
			for _, t := range types {
				if t == u || (strings.HasSuffix(u, ".") && strings.HasPrefix(t, u)) {
					return decision(r.category, "uti: "+t, certain)
				}
			}
		}
	}
	ext := strings.ToLower(filepath.Ext(name))
	for _, r := range rules {
		if slices.Contains(r.exts, ext) {
			return decision(r.category, "uzantı: "+ext, extensionOnly)
		}
	}
	return decision(Other, "eşleşme yok", noMatch)
}

// decideFolder files a project by its markers, nested projects or mostly
// code, and anything else by what most of its files are. The latter is only
// as sure as an extension match, so a model may overrule it. The 60% code bar
// keeps a saved web page's "_files" folder (scripts plus assets) out of
// projects.
func decideFolder(f *FolderInfo) (string, string, float64) {
	if len(f.Markers) > 0 {
		return Projects, "proje işareti: " + strings.Join(f.Markers, ", "), certain
	}
	if f.Projects > 0 {
		return Projects, fmt.Sprintf("içinde %d proje var", f.Projects), certain
	}
	if f.Files == 0 {
		return Other, "boş klasör", noMatch
	}
	counts, bytes := map[string]int{}, map[string]int64{}
	for ext, n := range f.Exts {
		category := Other
		for _, r := range rules {
			if slices.Contains(r.exts, ext) {
				category = r.category
				break
			}
		}
		counts[category] += n
		bytes[category] += f.ExtBytes[ext]
	}
	// Most files wins; on a tie the bigger share of bytes does, so one video
	// with its thumbnail is a video folder.
	best := Other
	for _, c := range Categories {
		n, b := counts[c.Name], bytes[c.Name]
		if n > counts[best] || n == counts[best] && b > bytes[best] {
			best = c.Name
		}
	}
	share := counts[best] * 100 / f.Files
	if best == Code && share >= 60 {
		return Projects, fmt.Sprintf("kod klasörü: %%%d kod", share), certain
	}
	reason := fmt.Sprintf("içerik: %%%d %s", share, best)
	if best == Other {
		return Other, reason, noMatch
	}
	return best, reason, extensionOnly
}
