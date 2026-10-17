package decide

import (
	"fmt"
	"slices"
	"strings"
)

// Groups are the folders projects are sorted into inside the workspace. The
// names are plain ASCII so they are easy to cd into.
const (
	GroupBlockchain = "blockchain"
	GroupMobile     = "mobil"
	GroupWeb        = "web"
	GroupBackend    = "backend"
	GroupTools      = "araclar"
	GroupOther      = "diger"
)

// Groups lists every group folder.
var Groups = []string{GroupBlockchain, GroupMobile, GroupWeb, GroupBackend, GroupTools, GroupOther}

// ProjectGroup picks a project's group from its markers, stack hints and file
// types. Order matters: a dapp with a Next.js front end is still blockchain,
// and a React Native app is mobile before it is web.
func ProjectGroup(f *FolderInfo) (group, reason string) {
	signal := func(match func(string) bool) string {
		for _, s := range f.Signals {
			if match(s) {
				return s
			}
		}
		return ""
	}
	named := func(names ...string) func(string) bool {
		return func(s string) bool {
			for _, n := range names {
				if s == n || strings.HasSuffix(n, ".") && strings.HasPrefix(s, n) || strings.HasPrefix(n, "*") && strings.HasSuffix(s, n[1:]) {
					return true
				}
			}
			return false
		}
	}
	count := func(exts ...string) int {
		n := 0
		for _, e := range exts {
			n += f.Exts[e]
		}
		return n
	}
	code := 0
	for ext, n := range f.Exts {
		if isCodeExt(ext) {
			code += n
		}
	}

	if s := signal(named("foundry.toml", "hardhat.config.", "truffle-config.")); s != "" {
		return GroupBlockchain, s
	}
	if sol := count(".sol"); sol >= 5 || sol > 0 && sol*5 >= code {
		return GroupBlockchain, fmt.Sprintf("%d .sol dosyası", sol)
	}
	if s := signal(named("pubspec.yaml", "*.xcodeproj", "android", "ios")); s != "" {
		return GroupMobile, s
	}
	if s := signal(named("build.gradle", "build.gradle.kts")); s != "" &&
		count(".kt", ".java") > count(".ts", ".tsx", ".js", ".jsx", ".py", ".go") {
		return GroupMobile, s + ", Kotlin/Java"
	}
	if s := signal(named("next.config.", "vite.config.", "nuxt.config.", "svelte.config.", "astro.config.", "angular.json")); s != "" {
		return GroupWeb, s
	}
	if ui := count(".tsx", ".jsx", ".vue", ".svelte"); ui >= 5 {
		return GroupWeb, fmt.Sprintf("%d arayüz dosyası", ui)
	}
	if s := signal(named("go.mod", "Cargo.toml", "mix.exs", "pyproject.toml", "requirements.txt", "setup.py",
		"Dockerfile", "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml", "Gemfile", "composer.json", "pom.xml")); s != "" {
		return GroupBackend, s
	}
	if n := count(".go", ".rs", ".ex", ".exs", ".py", ".java", ".kt", ".rb", ".php", ".ts", ".js"); n >= 5 {
		return GroupBackend, fmt.Sprintf("%d kaynak dosyası", n)
	}
	if code > 0 && code <= 20 {
		return GroupTools, "küçük betik klasörü"
	}
	return GroupOther, "türü belirlenemedi"
}

func isCodeExt(ext string) bool {
	for _, r := range rules {
		if r.category == Code && slices.Contains(r.exts, ext) {
			return true
		}
	}
	return false
}
