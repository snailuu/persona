package node

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

// herdrHost is the herdr host: every method is one herdr client call (Close: the tab's pane count, then a close).
// This file and engine.go are the only places in the node that know the herdr client.
type herdrHost struct{ c *herdr.Client }

// herdrHosts is a registry holding only the herdr host on c.
func herdrHosts(c *herdr.Client) *host.Registry { return host.NewRegistry(&herdrHost{c: c}) }

var (
	_ host.Host         = (*herdrHost)(nil)
	_ host.Watcher      = (*herdrHost)(nil)
	_ host.Launcher     = (*herdrHost)(nil)
	_ host.Organizer    = (*herdrHost)(nil)
	_ host.SlotCloser   = (*herdrHost)(nil)
	_ host.Splitter     = (*herdrHost)(nil)
	_ host.Streamer     = (*herdrHost)(nil)
	_ host.Controller   = (*herdrHost)(nil)
	_ host.FullClienter = (*herdrHost)(nil)
	_ host.Worktreer    = (*herdrHost)(nil)
)

// herdrPaneEnv is the variable herdr sets in every pane.
const herdrPaneEnv = nodeclient.HerdrPaneEnv

// herdrAgentEnv tells herdr which agent a pane runs. herdr knows Claude and Codex by the foreground program's name
// (basename claude / claude-code, codex), so a wrapper that execs a binary of another name ("claude-2.1.318") was never
// seen as Claude: the start waited out claudeStartWait with Claude's input box on screen (#3454). herdr reads this
// variable from the foreground process's environment as it was exec'd (/proc/<pid>/environ, macOS KERN_PROCARGS2)
// before it looks at the name: herdr 0.7.1 on Linux, 0.7.5 on macOS; older ones and Windows ignore it. So the node
// puts it on the agent's own command only, `HERDR_AGENT=claude <launcher> …` where the pane's shell takes that form
// (typedHerdrAgent; codex-start sets it in the environment it execs): set on the pane's shell, or the tab's
// environment, it would be authoritative for everything run there later.
const herdrAgentEnv = "HERDR_AGENT"

// herdrAgentName is what herdr calls an agent of kind, "" for one it has no integration for (a compatible CLI, pi: left to its
// own detection) and on Windows, where herdr does not read HERDR_AGENT. herdr has an integration for omp of its own
// (`herdr agent start --kind omp`), so omp is named as omp rather than as the Claude it is not.
func herdrAgentName(kind string) string {
	if runtime.GOOS != "windows" && (kind == "claude" || kind == "codex" || kind == ompAgent) {
		return kind
	}
	return ""
}

// herdrAgentFor is the HERDR_AGENT value for an agent of kind started in launch's panes: herdrAgentName on herdr, ""
// on other hosts.
func herdrAgentFor(launch host.Launcher, kind string) string {
	if _, ok := launch.(*herdrHost); !ok {
		return ""
	}
	return herdrAgentName(kind)
}

// typedHerdrAgent is herdrAgentFor for a command typed into a pane whose shell is shells (the programs in front of
// it): "" unless they take `VAR=val cmd`, so csh, pwsh, nu, xonsh, elvish and a shell not known get the command as
// before, without it.
func typedHerdrAgent(launch host.Launcher, kind string, shells ...string) string {
	if !takesAssign(shells...) {
		return ""
	}
	return herdrAgentFor(launch, kind)
}

// takesAssign: shells (names or paths, "-zsh" for a login shell) are there and each is one of assignShells.
func takesAssign(shells ...string) bool {
	for _, s := range shells {
		if !assignShells[strings.TrimPrefix(filepath.Base(s), "-")] {
			return false
		}
	}
	return len(shells) > 0
}

// assignShells are the shells whose `VAR=val cmd` runs cmd with VAR set and leaves the shell without it (fish from
// 3.1): the ones the node types HERDR_AGENT in front for.
var assignShells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "mksh": true, "fish": true}

// withHerdrAgent is the command line cmd with HERDR_AGENT=agent in front, for the shell that runs it to put in the
// agent's environment alone; cmd as it is when agent is "".
func withHerdrAgent(agent, cmd string) string {
	if agent == "" {
		return cmd
	}
	return herdrAgentEnv + "=" + agent + " " + cmd
}

func (h *herdrHost) Kind() string    { return host.Herdr }
func (h *herdrHost) PaneEnv() string { return herdrPaneEnv }

func (h *herdrHost) Version(ctx context.Context) string { return h.c.Version(ctx) }

func (h *herdrHost) Snapshot(ctx context.Context) (*host.Layout, error) {
	snap, err := h.c.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	l := &host.Layout{Agents: fromHerdrAgents(snap.Agents), Panes: fromHerdrAgents(snap.Panes)}
	if len(snap.Layouts) > 0 {
		l.PaneIndex = herdr.PaneIndexes(snap.Layouts)
	}
	for _, w := range snap.Workspaces {
		l.Spaces = append(l.Spaces, fromHerdrWorkspace(w))
	}
	for _, t := range snap.Tabs {
		l.Slots = append(l.Slots, host.Slot{ID: t.ID, SpaceID: t.WorkspaceID, Label: t.Label, Number: t.Number})
	}
	fillSpaceDirectories(l.Spaces, l.Panes)
	if len(l.Panes) == 0 {
		fillSpaceDirectories(l.Spaces, l.Agents)
	}
	return l, nil
}

func (h *herdrHost) Agents(ctx context.Context) ([]host.Agent, error) {
	agents, err := h.c.Agents(ctx)
	return fromHerdrAgents(agents), err
}

// Agent is agent.get; herdr answers *_not_found for a pane where it detects no agent (a claude it does not recognise,
// #3454), marked ErrNoAgent.
func (h *herdrHost) Agent(ctx context.Context, pane host.PaneRef) (*host.Agent, error) {
	a, err := h.c.AgentGet(ctx, pane.ID)
	if herdr.IsNotFound(err) {
		return nil, host.Mark(err, host.ErrNoAgent)
	}
	if err != nil {
		return nil, err
	}
	out := fromHerdrAgent(*a)
	return &out, nil
}

func (h *herdrHost) Read(ctx context.Context, pane host.PaneRef, o host.ReadOpts) (string, error) {
	if o.ANSI {
		return h.c.PaneReadANSI(ctx, pane.ID, o.Source, o.Lines)
	}
	return h.c.PaneRead(ctx, pane.ID, o.Source, o.Lines)
}

func (h *herdrHost) SendText(ctx context.Context, pane host.PaneRef, text string) error {
	return h.c.PaneSendText(ctx, pane.ID, text)
}

func (h *herdrHost) SendKeys(ctx context.Context, pane host.PaneRef, keys ...string) error {
	return h.c.PaneSendKeys(ctx, pane.ID, keys...)
}

// Prompt is agent.prompt; herdr answers *_not_found for a pane whose agent it does not detect (a compatible CLI).
func (h *herdrHost) Prompt(ctx context.Context, pane host.PaneRef, text string) error {
	err := h.c.AgentPrompt(ctx, pane.ID, text)
	if herdr.IsNotFound(err) {
		return host.Mark(err, host.ErrNoAgent)
	}
	if herdr.IsBlocked(err) {
		return host.Mark(err, host.ErrAgentBlocked)
	}
	return err
}

func (h *herdrHost) PaneProcesses(ctx context.Context, pane host.PaneRef) ([]int, error) {
	return h.c.PaneProcesses(ctx, pane.ID)
}

// PaneForeground names each foreground program by what it was started as (argv0), else its process name.
func (h *herdrHost) PaneForeground(ctx context.Context, pane host.PaneRef) ([]string, error) {
	procs, err := h.c.PaneForeground(ctx, pane.ID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(procs))
	for _, p := range procs {
		if p.Argv0 != "" {
			names = append(names, p.Argv0)
		} else {
			names = append(names, p.Name)
		}
	}
	return names, nil
}

func (h *herdrHost) ReportMetadata(ctx context.Context, pane host.PaneRef, source string, tokens map[string]*string) error {
	return h.c.PaneReportMetadata(ctx, pane.ID, source, tokens)
}

// Watch is one events.subscribe: the lifecycle events plus agent status for each pane (herdr only sends those per
// pane). A status change into or out of blocked is urgent.
func (h *herdrHost) Watch(ctx context.Context, panes []host.PaneRef, changed func(urgent bool)) error {
	ids := make([]string, len(panes))
	for i, p := range panes {
		ids[i] = p.ID
	}
	var w waits
	return h.c.Subscribe(ctx, subscriptions(ids), func(e herdr.Event) {
		var d struct {
			PaneID      string `json:"pane_id"`
			AgentStatus string `json:"agent_status"`
		}
		status := e.Event == "pane.agent_status_changed" || e.Event == "pane_agent_status_changed"
		changed(status && json.Unmarshal(e.Data, &d) == nil && w.changed(d.PaneID, d.AgentStatus == "blocked"))
	})
}

// herdrLifecycle are the events that change which sessions there are or how they are named.
var herdrLifecycle = []string{
	"workspace.created", "workspace.renamed", "workspace.closed", "workspace.moved",
	"tab.created", "tab.renamed", "tab.closed", "tab.moved",
	"pane.created", "pane.closed", "pane.exited", "pane.updated", "pane.moved", "pane.agent_detected",
}

// subscriptions: the lifecycle events plus agent status for each pane (herdr only sends those per pane).
func subscriptions(panes []string) []herdr.Subscription {
	subs := make([]herdr.Subscription, 0, len(herdrLifecycle)+len(panes))
	for _, t := range herdrLifecycle {
		subs = append(subs, herdr.Subscription{"type": t})
	}
	for _, p := range panes {
		subs = append(subs, herdr.Subscription{"type": "pane.agent_status_changed", "pane_id": p})
	}
	return subs
}

func (h *herdrHost) Spaces(ctx context.Context) ([]host.Space, error) {
	wss, err := h.c.Workspaces(ctx)
	out := make([]host.Space, 0, len(wss))
	for _, w := range wss {
		out = append(out, fromHerdrWorkspace(w))
	}
	return out, err
}

func (h *herdrHost) CreateSpace(ctx context.Context, cwd, label string) (*host.Created, error) {
	c, err := h.c.WorkspaceCreate(ctx, cwd, label)
	return fromHerdrCreated(c), err
}

func (h *herdrHost) CreateSlot(ctx context.Context, space, cwd, label string) (*host.Created, error) {
	c, err := h.c.TabCreate(ctx, space, cwd, label)
	return fromHerdrCreated(c), err
}

func (h *herdrHost) Run(ctx context.Context, pane host.PaneRef, command string) error {
	return h.c.PaneRun(ctx, pane.ID, command)
}

func (h *herdrHost) NameAgent(ctx context.Context, pane host.PaneRef, name string) error {
	return h.c.AgentRename(ctx, pane.ID, name)
}

func (h *herdrHost) RenameSpace(ctx context.Context, space, name string) error {
	return h.c.WorkspaceRename(ctx, space, name)
}

func (h *herdrHost) RenameSlot(ctx context.Context, slot, name string) error {
	return h.c.TabRename(ctx, slot, name)
}

// Close closes the pane when its tab is split, else the whole tab. When the panes cannot be counted it closes only the
// pane: herdr closes the tab itself when that was its last pane, and a split tab keeps its other panes (#4873).
func (h *herdrHost) Close(ctx context.Context, pane host.PaneRef, space, slot string) error {
	panes, err := h.c.TabPanes(ctx, space, slot)
	if err != nil || panes > 1 {
		return h.c.PaneClose(ctx, pane.ID)
	}
	return h.c.TabClose(ctx, slot)
}

// CloseSlot closes the whole tab, every pane in it.
func (h *herdrHost) CloseSlot(ctx context.Context, slot string) error {
	return h.c.TabClose(ctx, slot)
}

// Split splits the pane; the new pane keeps the host's kind (herdr panes carry no prefix).
func (h *herdrHost) Split(ctx context.Context, pane host.PaneRef, direction, cwd string) (host.PaneRef, error) {
	id, err := h.c.PaneSplit(ctx, pane.ID, direction, cwd)
	if err != nil {
		return host.PaneRef{}, err
	}
	if id == "" {
		return host.PaneRef{}, errors.New("herdr did not return the new pane")
	}
	return host.PaneRef{Kind: pane.Kind, ID: id}, nil
}

// CreateWorktree has herdr check the branch out at path and open it as a workspace grouped under the source one.
func (h *herdrHost) CreateWorktree(ctx context.Context, space, branch, path string) (*host.Created, error) {
	c, err := h.c.WorktreeCreate(ctx, space, branch, path)
	if err != nil {
		return nil, err
	}
	out := fromHerdrCreated(&c.Created)
	if out.SpaceID == "" {
		return nil, errors.New("herdr did not return the new workspace")
	}
	return out, nil
}

func (h *herdrHost) WorktreesDir(home string) string { return herdr.WorktreesDir(home) }

// worktreeSupport caches whether this herdr has `worktree` (herdr 0.9 on Unix).
var worktreeSupport struct {
	once sync.Once
	ok   bool
}

func (h *herdrHost) CanWorktree() bool {
	return h.has(&worktreeSupport, "worktree")
}

// observeSupport, controlSupport cache whether this herdr has `terminal session observe` / `control` (herdr 0.9 on
// Unix).
var observeSupport, controlSupport struct {
	once sync.Once
	ok   bool
}

// has runs `herdr <args> --help` once: whether this herdr knows the command.
func (h *herdrHost) has(cache *struct {
	once sync.Once
	ok   bool
}, args ...string) bool {
	cache.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cache.ok = exec.CommandContext(ctx, h.c.Bin, append(args, "--help")...).Run() == nil
	})
	return cache.ok
}

func (h *herdrHost) CanObserve() bool {
	return h.has(&observeSupport, "terminal", "session", "observe")
}
func (h *herdrHost) CanControl() bool {
	return h.has(&controlSupport, "terminal", "session", "control")
}

func (h *herdrHost) PaneSize(ctx context.Context, pane host.PaneRef) (int, int, error) {
	return h.c.PaneSize(ctx, pane.ID)
}

func (h *herdrHost) Observe(ctx context.Context, pane host.PaneRef, cols, rows int, fn func(host.Frame)) error {
	return h.c.Observe(ctx, pane.ID, cols, rows, func(f herdr.Frame) { fn(host.Frame(f)) })
}

func (h *herdrHost) Control(ctx context.Context, pane host.PaneRef, cols, rows int, takeover bool, fn func(host.Frame)) (host.Control, error) {
	c, err := h.c.StartControl(ctx, pane.ID, cols, rows, takeover, func(f herdr.Frame) { fn(host.Frame(f)) })
	if err != nil {
		if errors.Is(err, herdr.ErrAttachBusy) {
			err = host.Mark(err, host.ErrAttachBusy)
		}
		return nil, err
	}
	return herdrControl{c}, nil
}

// herdrControl is herdr's control with its errors marked for the host package.
type herdrControl struct{ *herdr.Control }

func (c herdrControl) Mouse(e host.MouseEvent) error { return c.Control.Mouse(herdr.MouseEvent(e)) }

func (c herdrControl) Err() error {
	err := c.Control.Err()
	if errors.Is(err, herdr.ErrTakenOver) {
		return host.Mark(err, host.ErrTakenOver)
	}
	return err
}

func fromHerdrWorkspace(w herdr.Workspace) host.Space {
	return host.Space{ID: w.ID, Label: w.Label, Number: w.Number, Cwd: w.Cwd, Branch: w.Branch}
}

func fromHerdrCreated(c *herdr.Created) *host.Created {
	if c == nil {
		return nil
	}
	out := &host.Created{SlotID: c.Tab.ID, Pane: host.PaneRef{Kind: host.Herdr, ID: c.RootPane.ID}}
	if c.Workspace != nil {
		out.SpaceID = c.Workspace.ID
	}
	return out
}

func fromHerdrAgents(agents []herdr.Agent) []host.Agent {
	if agents == nil {
		return nil
	}
	out := make([]host.Agent, len(agents))
	for i, a := range agents {
		out[i] = fromHerdrAgent(a)
	}
	return out
}

// fromHerdrAgent: herdr's pane ids are herdr PaneRefs as they are (no prefix).
func fromHerdrAgent(a herdr.Agent) host.Agent {
	out := host.Agent{Agent: a.Agent, Name: a.Name, AgentStatus: a.AgentStatus, Cwd: a.Cwd, ForegroundCwd: a.ForegroundCwd,
		PaneID: a.PaneID, TabID: a.TabID, WorkspaceID: a.WorkspaceID, Title: a.Title, RawTitle: a.RawTitle, Tokens: a.Tokens}
	if a.AgentSession != nil {
		out.AgentSession = &host.AgentSession{Kind: a.AgentSession.Kind, Value: a.AgentSession.Value}
	}
	return out
}

// CanFullClient: herdr on Unix, with this client's socket one of herdr's sessions.
func (h *herdrHost) CanFullClient() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := h.c.SessionName(ctx)
	return err == nil
}

func (h *herdrHost) StartFullClient(ctx context.Context, opts host.FullClientOptions, onData func([]byte)) (host.FullClient, error) {
	session, err := h.c.SessionName(ctx)
	if err != nil {
		return nil, err
	}
	if opts.Pane != nil && opts.Pane.ID != "" {
		_ = h.c.FocusPane(ctx, opts.Pane.ID) // the client then opens on it; not worth failing the open for
	}
	fc, err := h.c.StartFull(herdr.FullOptions{Session: session, Cols: opts.Cols, Rows: opts.Rows}, onData)
	if err != nil {
		return nil, err
	}
	return fc, nil
}

// Focused is the focus herdr shows now (host.Focuser).
func (h *herdrHost) Focused(ctx context.Context) (host.FocusState, error) {
	f, err := h.c.Focused(ctx)
	return host.FocusState{Space: f.Workspace, Slot: f.Tab, Pane: f.Pane}, err
}

func (h *herdrHost) WatchFocus(ctx context.Context, changed func()) error {
	return h.c.WatchFocus(ctx, changed)
}

func (h *herdrHost) FocusPane(ctx context.Context, pane host.PaneRef) error {
	return h.c.FocusPane(ctx, pane.ID)
}
