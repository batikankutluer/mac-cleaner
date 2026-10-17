package scan

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Apps keep their own folders in Documents (meeting recordings, game saves,
// a notes vault). Moving one can break the app, so such folders are left
// alone. They are spotted three ways: a name matching an installed app, a
// vendor that names its folder differently from its app, or a marker inside.
var vendorFolders = []string{
	"Image-Line", "Native Instruments", "Adobe", "Microsoft User Data", "My Games",
	"Rockstar Games", "Electronic Arts", "Ubisoft", "Ableton", "Arturia", "Avid",
	"Blackmagic Design", "Epic Games", "Steam",
}

var appMarkers = map[string]string{".obsidian": "Obsidian", ".logseq": "Logseq", "Local State": "Chrome profili"}

// installedApps lists the names of installed apps. A variable so tests can
// pretend any set of apps is installed.
var installedApps = func() []string {
	home, _ := os.UserHomeDir()
	var apps []string
	for _, dir := range []string{"/Applications", "/System/Applications", filepath.Join(home, "Applications")} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if name, ok := strings.CutSuffix(e.Name(), ".app"); ok {
				apps = append(apps, name)
			}
		}
	}
	return apps
}

// appOwner returns the app a folder belongs to, or "".
func appOwner(dir string, apps []string) string {
	name := filepath.Base(dir)
	for _, v := range vendorFolders {
		if strings.EqualFold(name, v) {
			return v
		}
	}
	for _, app := range apps {
		if sameApp(name, app) {
			return app
		}
	}
	for marker, app := range appMarkers {
		if _, err := os.Lstat(filepath.Join(dir, marker)); err == nil {
			return app
		}
	}
	return ""
}

// sameApp matches "Acme" to "acme.io" and "Acme" to "Acme", but not
// "Image" to "Image Capture" or "Acm" to "Acme": names must be equal, or
// the shorter one must continue in the longer with punctuation, not a space.
func sameApp(folder, app string) bool {
	a, b := strings.ToLower(folder), strings.ToLower(app)
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(a) < 4 || !strings.HasPrefix(b, a) {
		return false
	}
	if len(b) == len(a) {
		return true
	}
	next := rune(b[len(a)])
	return !unicode.IsLetter(next) && !unicode.IsDigit(next) && !unicode.IsSpace(next)
}
