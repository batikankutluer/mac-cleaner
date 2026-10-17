package decide

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/batikankutluer/mac-cleaner/internal/format"
)

// SystemOne asks a local decision-model server that speaks TypeSafe's System One
// API (Ollaya, Kev, THX-01 and others), one multiple-choice question per file. Servers
// batch concurrent requests, so callers should run it through All.
type SystemOne struct {
	URL    string // e.g. http://127.0.0.1:11435 for Ollaya
	Model  string
	APIKey string // sent as a bearer token when set
	HTTP   *http.Client
}

func NewSystemOne(url, model string) *SystemOne {
	return &SystemOne{
		URL:   strings.TrimRight(url, "/"),
		Model: model,
		HTTP:  &http.Client{Timeout: 6 * time.Minute}, // the first request may wait for a model load
	}
}

// ErrUnauthorized means the server rejected the API key.
var ErrUnauthorized = errors.New("API anahtarı reddedildi")

const instructions = "This is the metadata of a loose file or folder in a macOS user's Downloads, Desktop or Documents folder. " +
	"Which folder should it be filed under?"

type s1Question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type s1Request struct {
	Model     string                `json:"model"`
	State     map[string]string     `json:"state"`
	Questions map[string]s1Question `json:"questions"`
}

type s1Response struct {
	Answers map[string]struct {
		Type       string  `json:"type"`
		Choice     string  `json:"choice"`
		Confidence float64 `json:"confidence"`
	} `json:"answers"`
}

func (k *SystemOne) Decide(ctx context.Context, in Input) (Decision, error) {
	criteria := make(map[string]string, len(Categories))
	for _, c := range Categories {
		// Emptiness is decided by rule, and only folders can be projects.
		if c.Name == Empty || c.Name == Projects && !in.IsFolder() {
			continue
		}
		criteria[c.Name] = c.Description
	}
	body, err := json.Marshal(s1Request{
		Model: k.Model,
		State: s1State(in),
		Questions: map[string]s1Question{
			"category": {Type: "choice", Instructions: instructions, Criteria: criteria},
		},
	})
	if err != nil {
		return Decision{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.URL+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return Decision{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.do(req)
	if err != nil {
		return Decision{}, err
	}
	defer resp.Body.Close()

	var out s1Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Decision{}, fmt.Errorf("model: yanıt okunamadı: %w", err)
	}
	ans, ok := out.Answers["category"]
	if _, offered := criteria[ans.Choice]; !ok || !offered {
		return Decision{}, fmt.Errorf("model: geçersiz cevap %q", ans.Choice)
	}
	return Decision{
		Category:   ans.Choice,
		Source:     k.Model,
		Reason:     fmt.Sprintf("model seçti, güven %.2f", ans.Confidence),
		Confidence: ans.Confidence,
	}, nil
}

func (k *SystemOne) do(req *http.Request) (*http.Response, error) {
	if k.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+k.APIKey)
	}
	resp, err := k.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("%w (%s)", ErrUnauthorized, resp.Status)
		}
		return nil, fmt.Errorf("model: %s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	return resp, nil
}

// s1State is the metadata the model sees. Only the download host is sent,
// never the full URL, since those often carry tokens.
func s1State(in Input) map[string]string {
	if in.IsFolder() {
		s := map[string]string{
			"folder name": in.Name,
			"kind":        "folder",
			"file count":  strconv.Itoa(in.Folder.Files),
			"total size":  format.Size(in.Size),
		}
		if exts := format.TopExts(in.Folder.Exts, 8); len(exts) > 0 {
			s["most common file types"] = strings.Join(exts, ", ")
		}
		if len(in.Folder.Markers) > 0 {
			s["project markers"] = strings.Join(in.Folder.Markers, ", ")
		}
		if in.Folder.Projects > 0 {
			s["project subfolders"] = strconv.Itoa(in.Folder.Projects)
		}
		return s
	}
	ext := strings.ToLower(filepath.Ext(in.Name))
	if ext == "" {
		ext = "(none)"
	}
	s := map[string]string{
		"file name": in.Name,
		"extension": ext,
		"size":      format.Size(in.Size),
	}
	if in.ContentType != "" {
		s["content type (UTI)"] = in.ContentType
	}
	if len(in.TypeTree) > 0 {
		s["conforms to"] = strings.Join(in.TypeTree, ", ")
	}
	if in.SourceHost != "" {
		s["downloaded from"] = in.SourceHost
	}
	if in.ScreenCapture {
		s["screen capture"] = "yes"
	}
	return s
}
