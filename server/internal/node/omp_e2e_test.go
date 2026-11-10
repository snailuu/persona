package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
)

// An omp from the web to a resumed session: start (its own command), herdr's pane, the extension's session and row,
// a message to the extension, the process gone and the session on disk, a message that resumes it, and the message
// delivered to the extension that came up.
func TestOmpEndToEnd(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	withPiFamilyGrace(t, 200*time.Millisecond)
	n, f, home, rh := piHerdrNode(t)
	cwd := t.TempDir()
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": filepath.Base(cwd)}}})
	ompStartHerdr(f)

	// Bounded: a start that never sees its pane fails here with what herdr was asked, not in a test timeout.
	startCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{"cwd": cwd, "launcher": "omp", "agent_type": "omp", "prompt": "hi"})
	out, err := n.handle(startCtx, "session.start", raw)
	if err != nil {
		t.Fatalf("start: %v; herdr was asked %v", err, f.trace())
	}
	var res map[string]any
	b, _ := json.Marshal(out)
	_ = json.Unmarshal(b, &res)
	started, _ := res["session"].(map[string]any)
	if started["agent"] != ompAgent {
		t.Fatalf("started %v", res)
	}
	if typed := strings.Join(f.trace("pane.send_input"), "\n"); !strings.Contains(typed, " omp ") {
		t.Fatalf("omp was not started with its own command: %v", f.trace("pane.send_input"))
	}

	rt := ompExtension(t, rh, "omp-e2e", map[string]any{"session_id": ompSID, "status": "working", "title": "fix the thing",
		"last_text": "reading the grammar", "model": "claude-x", "rev": 3, "pane_id": "w1:p2", "cwd": cwd})
	s := onlyRow(t, listSessions(t, n), ompSID)
	if s.Agent != ompAgent || s.Title != "fix the thing" || s.PaneID != "w1:p2" || s.ReadOnlyReason != "" {
		t.Fatalf("row %+v", s)
	}
	if _, err := request(t, n, "session.send", map[string]any{"session_id": ompSID, "text": "go on", "client_msg_id": "m-1"}); err != nil {
		t.Fatal(err)
	}
	if len(rt.got("session.send")) != 1 {
		t.Fatalf("the extension got %d sends", len(rt.got("session.send")))
	}

	// The omp exits: its runtime goes, herdr's pane is gone, and the session file it wrote is on disk.
	_ = rt.conn.Close()
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	file := writeOmpSession(t, ompSessionsDir(home), "-w-e2e", ompSID, "fix the thing", cwd, "herdr", time.Now())
	waitFor(t, "the extension gone", func() bool { _, owner, _ := n.runtimes.rowOf(ompSID); return owner == nil })
	n.omps().at = time.Time{} // the file was written after the last scan
	if s := onlyRow(t, listSessions(t, n), ompSID); s.Status != "offline" || s.Agent != ompAgent {
		t.Fatalf("offline row %+v", s)
	}

	// A message resumes it: the tab runs omp --resume <file>, and the extension that comes up gets the message.
	rt2 := ompExtensionIdle(t, rh, "omp-new") // a new omp process: another pid, so another runtime name
	rt2.mu.Lock()
	rt2.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.items" {
			return nil, errors.New("no such session")
		}
		return map[string]any{"ok": true}, nil
	}
	rt2.mu.Unlock()
	argv := ompResumeHerdr(t, f, cwd, ompSID, rt2)
	if _, err := request(t, n, "session.send", map[string]any{"session_id": ompSID, "text": "continue", "client_msg_id": "m-2"}); err != nil {
		t.Fatal(err)
	}
	if got := argv(); strings.Join(got, " ") != "omp --resume "+file {
		t.Fatalf("ran %q, want omp --resume %s", got, file)
	}
	if len(rt2.got("session.send")) != 1 {
		t.Fatalf("the resumed session got %d sends", len(rt2.got("session.send")))
	}
}

// The two refusals the feature promises: a running omp without its extension is not sent to, and a stopped one
// without a session file is not resumed (nothing opened, nothing sent).
func TestOmpEndToEndRefusals(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home, _ := piHerdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, []any{ompPaneAgent("w1:p6", "/w", "")}, nil))
	fallback := otherSession(host.Agent{Agent: ompAgent, PaneID: "w1:p6"})
	if s := onlyRow(t, listSessions(t, n), fallback); s.ReadOnlyReason == "" {
		t.Fatalf("row %+v", s)
	}
	if _, err := request(t, n, "session.send", map[string]any{"session_id": fallback, "text": "hi", "client_msg_id": "m-a"}); !isRefusal(err, "omp_no_extension") {
		t.Fatalf("send without an extension: %v", err)
	}

	// A stopped session whose file went away between the scan and the send: refused, nothing opened.
	id := "01b20000-0000-7000-8000-000000000001"
	file := writeOmpSession(t, ompSessionsDir(home), "-w-gone", id, "gone", t.TempDir(), "herdr", time.Now())
	n.omps().at = time.Time{} // the file was written after the last scan
	_ = listSessions(t, n)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	f.reset()
	if _, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": "hi", "client_msg_id": "m-b"}); !isRefusal(err, "omp_resume_unknown") {
		t.Fatalf("send to a stopped omp without its file: %v", err)
	}
	if got := f.trace("tab.create"); len(got) != 0 {
		t.Fatalf("tab.create %v", got)
	}
}
