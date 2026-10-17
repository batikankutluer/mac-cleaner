// Package format holds small display helpers shared by the TUI and plain output.
package format

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"
)

func Size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

var home, _ = os.UserHomeDir()

// Tilde shortens paths under the home directory to ~/...
func Tilde(p string) string {
	if home != "" && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// Clean replaces control characters (file names may contain newlines) so a
// name can never break the terminal layout.
func Clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

// TopExts lists the n most common extensions as "mp4 ×4", most common first.
func TopExts(exts map[string]int, n int) []string {
	keys := make([]string, 0, len(exts))
	for k := range exts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if exts[keys[i]] != exts[keys[j]] {
			return exts[keys[i]] > exts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	out := make([]string, 0, n)
	for _, k := range keys[:min(n, len(keys))] {
		name := strings.TrimPrefix(k, ".")
		if name == "" {
			name = "uzantısız"
		}
		out = append(out, fmt.Sprintf("%s ×%d", name, exts[k]))
	}
	return out
}

// Duration shows short runs in milliseconds and longer ones to a tenth of a second.
func Duration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}
