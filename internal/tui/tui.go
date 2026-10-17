// Package tui is the interactive front end: it scans, shows the proposed plan
// grouped by category, lets the user pick items, and applies the plan after
// a confirmation.
package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/batikankutluer/mac-cleaner/internal/apply"
	"github.com/batikankutluer/mac-cleaner/internal/decide"
	"github.com/batikankutluer/mac-cleaner/internal/plan"
	"github.com/batikankutluer/mac-cleaner/internal/scan"
	"github.com/batikankutluer/mac-cleaner/internal/store"
)

type phase int

const (
	phaseKey phase = iota
	phaseScanning
	phaseReview
	phaseHistory
	phaseConfirm
	phaseApplying
	phaseDone
	phaseError
)

const allLabel = "Tümü"

type Config struct {
	plan.Options
	Store       *store.Store
	DeciderName string
	Key         *KeyPrompt // nil when the decider needs no API key
	Workspace   string     // where projects go; "" if none was found
}

// KeyPrompt asks for the API key in the TUI when none is stored yet, or when
// the server rejects the stored one.
type KeyPrompt struct {
	Needed bool
	Save   func(key string) error
}

type (
	progressMsg scan.Progress
	scanDoneMsg struct {
		res   *scan.Result
		items []plan.Item
		runID int64
		err   error
		last  *lastApply // set when nothing is left to do
	}
	historyMsg struct {
		events []store.Event
		err    error
	}
	savedMsg struct {
		runID    int64
		selected int
		err      error
	}
	keySavedMsg struct{ err error }
	appliedMsg  struct {
		res apply.Result
		err error
	}
)

// lastApply summarizes the newest apply run that can still be undone.
type lastApply struct {
	runID    int64
	moved    int
	freed    int64
	finished time.Time
}

type model struct {
	cfg    Config
	ctx    context.Context
	phase  phase
	spin   spinner.Model
	prog   scan.Progress
	progCh chan scan.Progress

	res     *scan.Result
	runID   int64
	items   []plan.Item
	cats    []string
	counts  map[string]int
	catIdx  int
	visible []int // indices into items for the current category
	cursor  int
	offset  int

	history []store.Event
	status  string
	err     error

	input  textinput.Model
	keyErr string

	applied  apply.Result
	applyErr error
	last     *lastApply

	width, height int
}

func Run(cfg Config) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := tea.NewProgram(newModel(ctx, cfg), tea.WithAltScreen()).Run()
	return err
}

func newModel(ctx context.Context, cfg Config) model {
	in := textinput.New()
	in.Placeholder = "ts_..."
	in.EchoMode = textinput.EchoPassword
	in.EchoCharacter = '•'
	in.CharLimit = 512
	in.Prompt = "› "
	in.Focus()
	m := model{
		cfg:    cfg,
		ctx:    ctx,
		phase:  phaseScanning,
		spin:   spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(accentStyle)),
		progCh: make(chan scan.Progress, 1),
		input:  in,
		width:  100,
		height: 30,
	}
	if cfg.Key != nil && cfg.Key.Needed {
		m.phase = phaseKey
	}
	return m
}

func (m model) Init() tea.Cmd {
	if m.phase == phaseKey {
		return textinput.Blink
	}
	return m.scanCmds()
}

func (m model) scanCmds() tea.Cmd {
	return tea.Batch(m.spin.Tick, m.scanCmd(), waitProgress(m.progCh))
}

// rescan starts the pipeline again with a fresh progress channel; the previous
// run closed its own.
func (m model) rescan() (tea.Model, tea.Cmd) {
	m.phase, m.prog = phaseScanning, scan.Progress{}
	m.progCh = make(chan scan.Progress, 1)
	return m, m.scanCmds()
}

func (m model) startApply() (tea.Model, tea.Cmd) {
	m.phase, m.prog = phaseApplying, scan.Progress{}
	m.progCh = make(chan scan.Progress, 1)
	ctx, st, items, ch := m.ctx, m.cfg.Store, m.items, m.progCh
	run := func() tea.Msg {
		defer close(ch)
		res, err := apply.Apply(ctx, st, items, func(done, total int, current string) {
			scan.Send(ch, scan.Progress{Phase: plan.PhaseApply, Done: done, Total: total, Current: current})
		})
		return appliedMsg{res, err}
	}
	return m, tea.Batch(m.spin.Tick, run, waitProgress(ch))
}

func (m model) saveKeyCmd(key string) tea.Cmd {
	save := m.cfg.Key.Save
	return func() tea.Msg { return keySavedMsg{save(key)} }
}

func (m model) scanCmd() tea.Cmd {
	cfg, ctx, ch := m.cfg, m.ctx, m.progCh
	return func() tea.Msg {
		defer close(ch)
		res, items, runID, err := plan.Run(ctx, cfg.Store, cfg.Options, ch)
		msg := scanDoneMsg{res: res, items: items, runID: runID, err: err}
		if err == nil && len(items) == 0 {
			if id, _ := cfg.Store.LastUndoableApply(); id != 0 {
				l := &lastApply{runID: id}
				if l.moved, l.freed, l.finished, err = cfg.Store.ApplySummary(id); err == nil {
					msg.last = l
				}
			}
		}
		return msg
	}
}

func waitProgress(ch <-chan scan.Progress) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return nil
		}
		return progressMsg(p)
	}
}

func (m model) historyCmd(fileID int64) tea.Cmd {
	st := m.cfg.Store
	return func() tea.Msg {
		events, err := st.History(fileID)
		return historyMsg{events, err}
	}
}

func (m model) saveCmd() tea.Cmd {
	st, roots, rows := m.cfg.Store, m.cfg.Roots, plan.Rows(m.items)
	return func() tea.Msg {
		runID, err := st.BeginRun("plan", roots)
		if err != nil {
			return savedMsg{err: err}
		}
		err = st.SavePlan(runID, rows)
		if ferr := st.FinishRun(runID, err); err == nil {
			err = ferr
		}
		selected := 0
		for _, r := range rows {
			if r.Selected {
				selected++
			}
		}
		return savedMsg{runID, selected, err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampScroll()
	case spinner.TickMsg:
		if m.phase != phaseScanning && m.phase != phaseApplying {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case progressMsg:
		m.prog = scan.Progress(msg)
		return m, waitProgress(m.progCh)
	case scanDoneMsg:
		if errors.Is(msg.err, decide.ErrUnauthorized) && m.cfg.Key != nil {
			m.phase, m.keyErr = phaseKey, "TypeSafe bu anahtarı kabul etmedi, tekrar dene."
			m.input.SetValue("")
			return m, textinput.Blink
		}
		if msg.err != nil {
			m.err, m.phase = msg.err, phaseError
			return m, nil
		}
		m.res, m.items, m.runID, m.last = msg.res, msg.items, msg.runID, msg.last
		m.buildCategories()
		m.phase = phaseReview
	case historyMsg:
		if msg.err != nil {
			m.status = "Geçmiş okunamadı: " + msg.err.Error()
			return m, nil
		}
		m.history, m.phase = msg.events, phaseHistory
	case savedMsg:
		if msg.err != nil {
			m.status = "Plan kaydedilemedi: " + msg.err.Error()
		} else {
			m.status = fmt.Sprintf("Plan #%d kaydedildi, %d öğe seçili. Uygulamak için u.", msg.runID, msg.selected)
		}
	case appliedMsg:
		m.applied, m.applyErr, m.phase = msg.res, msg.err, phaseDone
	case keySavedMsg:
		if msg.err != nil {
			m.keyErr = msg.err.Error()
			return m, nil
		}
		m.keyErr = ""
		return m.rescan()
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	if m.phase == phaseKey {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.phase == phaseApplying {
		return m, nil // quitting halfway through a move could leave it unrecorded
	}
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.phase {
	case phaseKey:
		switch key {
		case "enter":
			if m.input.Value() == "" {
				return m, nil
			}
			m.keyErr = "kaydediliyor…"
			return m, m.saveKeyCmd(m.input.Value())
		case "esc":
			// Go on without a key: rules only.
			m.cfg.Setup, m.cfg.Decider, m.cfg.DeciderName = nil, decide.Rules{}, "kurallar"
			return m.rescan()
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	case phaseScanning, phaseError:
		if key == "q" || key == "esc" {
			return m, tea.Quit
		}
	case phaseHistory:
		switch key {
		case "esc", "q", "enter", "backspace":
			m.phase, m.history = phaseReview, nil
		}
	case phaseReview:
		return m.reviewKey(key)
	case phaseConfirm:
		switch key {
		case "e", "y", "enter":
			return m.startApply()
		case "h", "n", "esc", "q":
			m.phase = phaseReview
		}
	case phaseDone:
		switch key {
		case "q", "esc":
			return m, tea.Quit
		case "r":
			return m.rescan()
		}
	}
	return m, nil
}

func (m model) reviewKey(key string) (tea.Model, tea.Cmd) {
	m.status = ""
	switch key {
	case "q", "esc":
		return m, tea.Quit
	case "up", "k":
		m.cursor--
	case "down", "j":
		m.cursor++
	case "pgup":
		m.cursor -= m.listHeight()
	case "pgdown":
		m.cursor += m.listHeight()
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = len(m.visible) - 1
	case "left", "h", "shift+tab":
		m.setCategory(m.catIdx - 1)
	case "right", "l", "tab":
		m.setCategory(m.catIdx + 1)
	case " ":
		if it := m.current(); it != nil {
			it.Selected = !it.Selected
		}
	case "a":
		all := true
		for _, i := range m.visible {
			all = all && m.items[i].Selected
		}
		for _, i := range m.visible {
			m.items[i].Selected = !all
		}
	case "enter":
		if it := m.current(); it != nil {
			return m, m.historyCmd(it.FileID)
		}
	case "s":
		return m, m.saveCmd()
	case "c":
		if it := m.current(); it != nil && it.Folder != nil && len(it.Folder.Artifacts) > 0 {
			it.Clean = !it.Clean
		}
	case "u":
		for _, it := range m.items {
			if it.Selected {
				m.phase = phaseConfirm
				return m, nil
			}
		}
		m.status = "Seçili öğe yok."
	}
	m.clampScroll()
	return m, nil
}

func (m *model) buildCategories() {
	m.counts = map[string]int{allLabel: len(m.items)}
	m.cats = []string{allLabel}
	for _, it := range m.items {
		if m.counts[it.Category] == 0 {
			m.cats = append(m.cats, it.Category) // items are already sorted by category
		}
		m.counts[it.Category]++
	}
	m.setCategory(0)
}

func (m *model) setCategory(i int) {
	if len(m.cats) == 0 {
		return
	}
	m.catIdx = (i + len(m.cats)) % len(m.cats)
	m.visible = m.visible[:0]
	for j, it := range m.items {
		if m.catIdx == 0 || it.Category == m.cats[m.catIdx] {
			m.visible = append(m.visible, j)
		}
	}
	m.cursor, m.offset = 0, 0
}

func (m *model) clampScroll() {
	m.cursor = max(0, min(m.cursor, len(m.visible)-1))
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
}

func (m model) current() *plan.Item {
	if len(m.visible) == 0 {
		return nil
	}
	return &m.items[m.visible[m.cursor]]
}
