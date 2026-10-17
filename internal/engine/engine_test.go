package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archive(t *testing.T, entries ...*tar.Header) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(h.Name))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(h.Name))
		}
	}
	tw.Close()
	gz.Close()
	return &buf
}

func TestUntar(t *testing.T) {
	dst := t.TempDir()
	err := untar(archive(t,
		&tar.Header{Name: "bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		&tar.Header{Name: "bin/ollaya", Typeflag: tar.TypeReg, Mode: 0o4777},
	), dst)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dst, "bin/ollaya"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode() != 0o755 {
		t.Errorf("mode = %v, want setuid and world-write stripped", fi.Mode())
	}
}

func TestUntarRejects(t *testing.T) {
	cases := map[string]*tar.Header{
		"traversal": {Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644},
		"absolute":  {Name: "/etc/evil", Typeflag: tar.TypeReg, Mode: 0o644},
		"symlink":   {Name: "lib/x", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	}
	for name, h := range cases {
		if err := untar(archive(t, h), t.TempDir()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestWithoutOllayaVars(t *testing.T) {
	got := withoutOllayaVars([]string{"HOME=/x", "OLLAYA_HOST=0.0.0.0", "OLLAYA_API_KEY=k", "PATH=/bin"})
	if strings.Join(got, " ") != "HOME=/x PATH=/bin" {
		t.Errorf("got %q", got)
	}
}
