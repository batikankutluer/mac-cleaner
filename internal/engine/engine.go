// Package engine downloads, verifies and runs a private copy of Ollaya
// (github.com/ollaya-dev/ollaya), a local server for decision models, so the
// tool works without a separate install. Everything lives under one directory:
// the binaries, the model store and the server log.
package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Version is pinned, and so are the archive checksums: a re-published release
// with different bytes is refused instead of trusted.
const Version = "0.13.0"

var assets = []struct{ name, sha256 string }{
	{"ollaya-darwin-arm64.tgz", "a047745e8f254d773227158cbbd5b2e8d595e8cfe366846d166bd7924abccdf1"},
	{"ollaya-darwin-arm64-mlx.tgz", "9302bb4ad1786adc517a0a5f2ab34491d35e38b39ed16834f9410a98727e6033"},
}

const (
	StepDownload = "engine-download"
	StepStart    = "engine-start"
	StepPull     = "model-pull"
)

type Progress struct {
	Step        string
	Done, Total int64 // bytes, for download and pull
}

type Engine struct {
	dir string
	URL string // set once the server answers

	mu      sync.Mutex
	cmd     *exec.Cmd
	exited  chan struct{}
	stopped bool
}

func New(dir string) *Engine { return &Engine{dir: dir} }

// Start installs Ollaya if needed, starts a private server on a free loopback
// port and pulls the model.
func (e *Engine) Start(ctx context.Context, model string, progress func(Progress)) error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("dahili motor sadece Apple Silicon Mac'lerde çalışır; -model-url ile harici bir sunucu ver")
	}
	prefix := filepath.Join(e.dir, Version)
	if err := install(ctx, prefix, progress); err != nil {
		return fmt.Errorf("Ollaya kurulamadı: %w", err)
	}
	if err := e.serve(ctx, filepath.Join(prefix, "bin", "ollaya"), progress); err != nil {
		return fmt.Errorf("Ollaya başlatılamadı: %w", err)
	}
	if err := e.pull(ctx, model, progress); err != nil {
		return fmt.Errorf("%s indirilemedi: %w", model, err)
	}
	return nil
}

// Stop terminates the server and its runner processes.
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopped = true
	if e.cmd == nil {
		return
	}
	pgid := e.cmd.Process.Pid
	syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-e.exited:
	case <-time.After(5 * time.Second):
		syscall.Kill(-pgid, syscall.SIGKILL)
		<-e.exited
	}
	os.Remove(e.pidFile())
	e.cmd = nil
}

func install(ctx context.Context, prefix string, progress func(Progress)) error {
	if _, err := os.Stat(filepath.Join(prefix, "bin", "ollaya")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(prefix), 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(prefix), ".install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, a := range assets {
		if err := fetch(ctx, a.name, a.sha256, stage, progress); err != nil {
			return err
		}
	}
	return os.Rename(stage, prefix)
}

func fetch(ctx context.Context, name, want, stage string, progress func(Progress)) error {
	url := fmt.Sprintf("https://github.com/ollaya-dev/ollaya/releases/download/v%s/%s", Version, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", name, resp.Status)
	}

	f, err := os.CreateTemp(stage, ".download-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()

	h := sha256.New()
	body := &countingReader{r: resp.Body, fn: func(n int64) {
		progress(Progress{Step: StepDownload, Done: n, Total: resp.ContentLength})
	}}
	if _, err := io.Copy(io.MultiWriter(f, h), body); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s: sha256 tutmadı (beklenen %s, gelen %s)", name, want, got)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return untar(f, stage)
}

// untar accepts only directories and regular files with local paths.
func untar(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("arşivde güvensiz yol: %q", hdr.Name)
		}
		p := filepath.Join(dst, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(hdr.Mode).Perm()&0o755)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("arşivde beklenmeyen giriş: %q", hdr.Name)
		}
	}
}

func (e *Engine) serve(ctx context.Context, bin string, progress func(Progress)) error {
	progress(Progress{Step: StepStart})
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		return err
	}
	e.killStale(bin)

	port, err := freePort()
	if err != nil {
		return err
	}
	logPath := filepath.Join(e.dir, "server.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close() // the child keeps its own descriptor

	// Not CommandContext: Stop owns the server's lifetime.
	cmd := exec.Command(bin, "serve")
	cmd.Env = append(withoutOllayaVars(os.Environ()),
		"OLLAYA_HOST=127.0.0.1:"+port,
		"OLLAYA_MODELS="+filepath.Join(e.dir, "models"),
	)
	cmd.Stdout, cmd.Stderr = logf, logf
	// Its own process group: the terminal's signals don't reach it, and Stop can
	// take down the runner processes it spawns along with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return context.Canceled
	}
	if err := cmd.Start(); err != nil {
		e.mu.Unlock()
		return err
	}
	e.cmd, e.exited = cmd, make(chan struct{})
	exited := e.exited
	go func() { cmd.Wait(); close(exited) }()
	e.mu.Unlock()
	os.WriteFile(e.pidFile(), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)

	url := "http://127.0.0.1:" + port
	deadline := time.Now().Add(30 * time.Second)
	for {
		if ok, _ := ping(ctx, url); ok {
			e.URL = url
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exited:
			return fmt.Errorf("sunucu hemen kapandı, ayrıntılar: %s", logPath)
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sunucu 30 saniyede cevap vermedi, ayrıntılar: %s", logPath)
		}
	}
}

func (e *Engine) pull(ctx context.Context, model string, progress func(Progress)) error {
	body, _ := json.Marshal(map[string]string{"model": model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.URL+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	type line struct {
		Status    string `json:"status"`
		Digest    string `json:"digest"`
		Total     int64  `json:"total"`
		Completed int64  `json:"completed"`
		Error     string `json:"error"`
	}
	if resp.StatusCode != http.StatusOK {
		var l line
		json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&l)
		return fmt.Errorf("%s: %s", resp.Status, l.Error)
	}
	layers := map[string][2]int64{}
	dec := json.NewDecoder(resp.Body)
	for {
		var l line
		if err := dec.Decode(&l); err == io.EOF {
			return errors.New("indirme yarıda kesildi")
		} else if err != nil {
			return err
		}
		switch {
		case l.Error != "":
			return errors.New(l.Error)
		case l.Status == "success":
			return nil
		case l.Digest != "":
			layers[l.Digest] = [2]int64{l.Completed, l.Total}
			var done, total int64
			for _, v := range layers {
				done, total = done+v[0], total+v[1]
			}
			progress(Progress{Step: StepPull, Done: done, Total: total})
		}
	}
}

func (e *Engine) pidFile() string { return filepath.Join(e.dir, "server.pid") }

// killStale stops a server left behind by a run that crashed, but only if the
// recorded pid still belongs to our own binary.
func (e *Engine) killStale(bin string) {
	b, err := os.ReadFile(e.pidFile())
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err == nil && strings.TrimSpace(string(out)) == bin {
		syscall.Kill(-pid, syscall.SIGTERM)
		for i := 0; i < 50 && syscall.Kill(pid, 0) == nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
	os.Remove(e.pidFile())
}

func ping(ctx context.Context, url string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/", nil)
	if err != nil {
		return false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port), nil
}

// withoutOllayaVars drops the user's OLLAYA_* settings (host, API key, model
// store) so they can't redirect or lock out the private server.
func withoutOllayaVars(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "OLLAYA_") {
			out = append(out, kv)
		}
	}
	return out
}

type countingReader struct {
	r    io.Reader
	n    int64
	fn   func(int64)
	last time.Time
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if now := time.Now(); now.Sub(c.last) > 100*time.Millisecond || err != nil {
		c.last = now
		c.fn(c.n)
	}
	return n, err
}
