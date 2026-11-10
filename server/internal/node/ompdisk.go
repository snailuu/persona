package node

// omp's stopped sessions on disk (docs/integrations/omp.md): the node lists the recent ones as offline rows and, from
// the same files, resumes one in herdr. pi has a companion process for this; omp does not need one — the node reads
// what omp wrote: <agent dir>/sessions/<encoded cwd>/<time>_<uuid>.jsonl, a title line, a session line with the id and
// cwd, and the records the extension appends (the persona-host stamp last).
//
// Nothing of the conversation leaves this machine: a row carries the title, the cwd and the server's host, no words.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	ompDiskEvery   = 15 * time.Second // a list this soon after the last scan reuses it
	ompDiskTimeout = 3 * time.Second  // one scan's reading; a slower disk keeps the last result
	ompDiskDays    = 7                // how old a session file may be
	ompDiskMax     = 80               // the most rows listed
	ompDiskHead    = 8 << 10          // the head read for the title and the session line (over-long lines skipped)
	ompDiskTail    = 64 << 10         // the tail read for the last persona-host record
)

// ompDiskSession is one stopped session as its file tells it.
type ompDiskSession struct {
	ID       string    // the session id (session line, a UUID)
	Path     string    // the session file
	Cwd      string    // the session's working directory
	Title    string    // the title (the session line's, else the title line's)
	Host     string    // where it last ran (the last persona-host record): herdr, terminal, or ""
	Modified time.Time // the file's mtime: when it was last used
}

// ompDiskEntry is what a file's head and tail said, kept until its mtime or size changes.
type ompDiskEntry struct {
	mtime time.Time
	size  int64
	s     ompDiskSession
}

// ompDiskState is the scan's cache: entries by path, the sessions by id, and when it last ran.
type ompDiskState struct {
	mu      sync.Mutex
	dir     string
	files   map[string]ompDiskEntry
	byID    map[string]ompDiskSession
	order   []ompDiskSession
	at      time.Time
	reads   int // files read in all, a test's cache-hit count
	scanned time.Time
}

// ompAgentDir is omp's agent directory: PI_CODING_AGENT_DIR when it names an omp one (…/.omp/agent, so a pi's own
// directory is never taken for it), else <home>/.omp/agent.
func ompAgentDir(home string) string {
	if d := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); d != "" &&
		filepath.Base(d) == "agent" && filepath.Base(filepath.Dir(d)) == ".omp" {
		return d
	}
	return filepath.Join(home, ".omp", "agent")
}

// omps is this node's scan state, made on first use (tests build Nodes by hand).
func (n *Node) omps() *ompDiskState {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ompDisk == nil {
		n.ompDisk = &ompDiskState{}
	}
	return n.ompDisk
}

// ompSessions returns the recent stopped sessions, newest first: cached for ompDiskEvery, and a scan that runs out of
// time keeps the last result.
func (n *Node) ompSessions() []ompDiskSession {
	d := n.omps()
	dir := filepath.Join(ompAgentDir(n.home), "sessions")
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dir == dir && time.Since(d.at) < ompDiskEvery {
		return d.order
	}
	if d.dir != dir {
		d.files, d.order = nil, nil
	}
	if d.files == nil {
		d.files = map[string]ompDiskEntry{}
	}
	d.dir = dir
	d.scanLocked()
	d.at = time.Now()
	return d.order
}

// ompDiskSession is the recent session id on disk, live or not (herdr's restore needs the file of a running one).
func (n *Node) ompDiskSession(id string) (ompDiskSession, bool) {
	if id == "" {
		return ompDiskSession{}, false
	}
	for _, s := range n.ompSessions() {
		if s.ID == id {
			return s, true
		}
	}
	return ompDiskSession{}, false
}

// ompSession is the stopped session id, false when it is not one of the recent ones (a live session has a row of its
// own and is not listed from disk), or when a live family pane may be writing its file.
func (n *Node) ompSession(id string) (ompDiskSession, bool) {
	if _, live := n.agent(id); id == "" || live {
		return ompDiskSession{}, false
	}
	s, ok := n.ompDiskSession(id)
	if !ok || n.ompDiskWrittenByLivePane(s) {
		return ompDiskSession{}, false
	}
	return s, true
}

// ompDiskWrittenByLivePane: a family pane the node knows only by its fallback id runs in the session's directory, so
// its file may be the one that omp is still writing (no extension, no herdr integration: nothing says which session).
// A pane seen since the file was last written hides it; a pane from before this node started hides the directory's
// rows, and getting them back when the pane goes beats resuming into a second omp.
func (n *Node) ompDiskWrittenByLivePane(s ompDiskSession) bool {
	if s.Cwd == "" {
		return false
	}
	cwd := filepath.Clean(s.Cwd)
	for _, p := range n.piFamilyFallbackPanes() {
		if p.cwd != "" && filepath.Clean(p.cwd) == cwd && (p.seen.IsZero() || !s.Modified.Before(p.seen.Add(-2*time.Second))) {
			return true
		}
	}
	return false
}

// ompOfflineRows are the stopped sessions no live row covers, as the list wants them.
func (n *Node) ompOfflineRows(seen map[string]bool) []proto.Session {
	var out []proto.Session
	for _, d := range n.ompSessions() {
		if seen[d.ID] || d.ID == "" || n.ompDiskWrittenByLivePane(d) {
			continue // a live row covers it, or a live pane may be writing it
		}
		s := proto.Session{
			SessionID: d.ID, Agent: ompAgent, Launcher: ompAgent, Status: proto.StatusOffline,
			Title: d.Title, Cwd: d.Cwd, UpdatedAt: d.Modified, Rev: d.Modified.UnixMilli(),
		}
		out = append(out, s)
	}
	return out
}

// scanLocked walks the session files (newest ompDiskMax within ompDiskDays) and parses the ones that changed.
func (d *ompDiskState) scanLocked() {
	files, err := filepath.Glob(filepath.Join(d.dir, "*", "*.jsonl"))
	if err != nil {
		return
	}
	type cand struct {
		path string
		st   os.FileInfo
	}
	var cands []cand
	now := time.Now()
	for _, p := range files {
		st, err := os.Stat(p)
		if err != nil || now.Sub(st.ModTime()) > ompDiskDays*24*time.Hour {
			continue
		}
		cands = append(cands, cand{p, st})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].st.ModTime().After(cands[j].st.ModTime()) })
	if len(cands) > ompDiskMax {
		cands = cands[:ompDiskMax]
	}
	kept := make(map[string]ompDiskEntry, len(cands))
	seen := map[string]bool{}
	var order []ompDiskSession
	deadline := time.Now().Add(ompDiskTimeout)
	full := true
	for _, c := range cands {
		if time.Now().After(deadline) {
			full = false // a slow disk: the last result stands, the rest next time
			break
		}
		var s ompDiskSession
		if e, ok := d.files[c.path]; ok && e.mtime.Equal(c.st.ModTime()) && e.size == c.st.Size() {
			s = e.s
		} else {
			d.reads++
			s = readOmpSession(c.path)
			s.Modified = c.st.ModTime()
		}
		if s.ID == "" {
			continue
		}
		kept[c.path] = ompDiskEntry{mtime: c.st.ModTime(), size: c.st.Size(), s: s}
		if seen[s.ID] {
			continue // the same session in two directories: the newest file is the one above
		}
		seen[s.ID] = true
		order = append(order, s)
	}
	if !full && d.files != nil {
		return
	}
	d.files = kept
	d.order = order
}

// readOmpSession reads the head for the session and its title and the tail for the last persona-host record.
func readOmpSession(path string) ompDiskSession {
	s := ompDiskSession{Path: path}
	f, err := os.Open(path)
	if err != nil {
		return s
	}
	defer f.Close()
	head := make([]byte, ompDiskHead)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	var titleLine string
	for _, line := range strings.Split(string(head), "\n") {
		var e struct {
			Type  string `json:"type"`
			Title string `json:"title"`
			ID    string `json:"id"`
			Cwd   string `json:"cwd"`
		}
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		switch {
		case e.Type == "title" && titleLine == "":
			titleLine = e.Title
		case e.Type == "session" && e.ID != "" && s.ID == "":
			s.ID, s.Cwd, s.Title = e.ID, e.Cwd, e.Title
		}
	}
	if s.ID == "" {
		s.ID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if i := strings.LastIndexByte(s.ID, '_'); i >= 0 {
			s.ID = s.ID[i+1:]
		}
	}
	if s.Title == "" {
		s.Title = titleLine
	}
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		start := st.Size() - ompDiskTail
		if start < 0 {
			start = 0
		}
		tail := make([]byte, st.Size()-start)
		if _, err := f.ReadAt(tail, start); err == nil {
			if start > 0 { // the first line is cut: not a record
				if i := strings.IndexByte(string(tail), '\n'); i >= 0 {
					tail = tail[i+1:]
				} else {
					tail = nil
				}
			}
			s.Host = ompHostIn(string(tail))
		}
	}
	return s
}

// ompHostIn is the host of the last persona-host record in lines (the detail the extension appends at a session's start).
func ompHostIn(tail string) string {
	lines := strings.Split(tail, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var e struct {
			Type       string `json:"type"`
			CustomType string `json:"customType"`
			Data       *struct {
				Host string `json:"host"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(lines[i]), &e) != nil || e.Data == nil {
			continue
		}
		if e.Type == "custom" && (e.CustomType == "persona-host" || e.CustomType == "jarvis-host") {
			return e.Data.Host
		}
	}
	return ""
}
