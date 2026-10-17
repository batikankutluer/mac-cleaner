package decide

import "testing"

func TestProjectGroup(t *testing.T) {
	cases := []struct {
		name string
		f    FolderInfo
		want string
	}{
		{"next app", FolderInfo{Signals: []string{".git", "next.config.ts", "package.json"}, Exts: map[string]int{".ts": 125, ".tsx": 39}}, GroupWeb},
		{"vite app", FolderInfo{Signals: []string{"package.json", "vite.config.js"}, Exts: map[string]int{".jsx": 2}}, GroupWeb},
		{"dapp with next", FolderInfo{Signals: []string{"foundry.toml", "next.config.js"}, Exts: map[string]int{".sol": 3, ".tsx": 40}}, GroupBlockchain},
		{"solidity, no markers", FolderInfo{Exts: map[string]int{".sol": 255, ".py": 12}}, GroupBlockchain},
		{"android", FolderInfo{Signals: []string{"build.gradle.kts", "settings.gradle.kts"}, Exts: map[string]int{".kt": 280}}, GroupMobile},
		{"go server", FolderInfo{Signals: []string{".git"}, Exts: map[string]int{".go": 177, ".kt": 99}}, GroupBackend},
		{"rust", FolderInfo{Signals: []string{"Cargo.toml"}, Exts: map[string]int{".rs": 20}}, GroupBackend},
		{"ts api with docker", FolderInfo{Signals: []string{"docker-compose.yml", "package.json"}, Exts: map[string]int{".ts": 38, ".tsx": 3}}, GroupBackend},
		{"scripts", FolderInfo{Exts: map[string]int{".py": 1, ".sh": 1}}, GroupTools},
		{"notes", FolderInfo{Signals: []string{".git"}, Exts: map[string]int{".md": 3}}, GroupOther},
	}
	for _, c := range cases {
		if got, why := ProjectGroup(&c.f); got != c.want {
			t.Errorf("%s: got %s (%s), want %s", c.name, got, why, c.want)
		}
	}
}
