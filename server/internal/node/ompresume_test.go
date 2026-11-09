package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// tabStartRE finds the file `personad tab-start` was given in the typed command.
var tabStartRE = regexp.MustCompile(` tab-start '?([^' ]+?)'?$`)

// ompExtensionIdle is an omp extension connected with no session open yet (nothing to resume).
func ompExtensionIdle(t *testing.T, rh *RuntimeHost, runtime string) *fakeRuntime {
	t.Helper()
	return dialRuntimeAt(t, rh.path, map[string]any{"runtime": runtime, "agent": "omp", "name": "omp",
		"caps": map[string]any{"send": true, "interrupt": true, "rename": true}})
}

// ompResumeHerdr answers the tab a resume opens: the start file's argv is recorded, and the extension lists the
// session once the command was typed (what a real omp does on its way up).
func ompResumeHerdr(t *testing.T, f *fakeHerdr, cwd, id string, rt *fakeRuntime) func() []string {
	t.Helper()
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": filepath.Base(cwd)}}})
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	var mu sync.Mutex
	var argv []string
	var once sync.Once
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" {
			return
		}
		m := tabStartRE.FindStringSubmatch(str(c.Params["text"]))
		if m == nil {
			return
		}
		b, _ := os.ReadFile(m[1])
		var file codexStartFile
		_ = json.Unmarshal(b, &file)
		mu.Lock()
		argv = file.Argv
		mu.Unlock()
		once.Do(func() {
			_ = rt.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
				{"session_id": id, "status": "idle", "pane_id": "w1:p2", "cwd": cwd}}})
		})
	})
	return func() []string { mu.Lock(); defer mu.Unlock(); return argv }
}

// A stopped omp is resumed in a new tab with `omp --resume <file>`, and the phone's message goes to the extension
// once it lists the session again.
func TestOmpResumeFromDisk(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home, rh := piHerdrNode(t)
	cwd := t.TempDir()
	id := "01b10000-0000-7000-8000-000000000001"
	file := writeOmpSession(t, ompSessionsDir(home), "-tmp-x", id, "resume me", cwd, "herdr", time.Now())
	rt := ompExtensionIdle(t, rh, "omp-7") // connected, nothing listed yet
	rt.mu.Lock()
	rt.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.items" {
			return nil, errors.New("no such session") // what a runtime that does not know it answers (runtimes_calls.go)
		}
		return map[string]any{"ok": true}, nil
	}
	rt.mu.Unlock()
	argv := ompResumeHerdr(t, f, cwd, id, rt)

	_, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": "go on", "client_msg_id": "m-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := argv(); strings.Join(got, " ") != "omp --resume "+file {
		t.Fatalf("ran %q, want omp --resume %s; typed %v", got, file, f.trace("pane.send_input"))
	}
	if len(rt.got("session.send")) != 1 {
		t.Fatalf("the message did not reach the extension: %d", len(rt.got("session.send")))
	}
}

// Nothing to resume with: no file, a deleted one, or one outside the agent's sessions directory. Nothing is started.
func TestOmpResumeRefusals(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home, _ := piHerdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	// A session listed from disk whose file is then deleted.
	id := "01b10000-0000-7000-8000-000000000002"
	file := writeOmpSession(t, ompSessionsDir(home), "-tmp-y", id, "gone", t.TempDir(), "herdr", time.Now())
	_ = listSessions(t, n) // the scan caches the file
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	_, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": "hi", "client_msg_id": "m-gone"})
	if code, _ := refusalOf(t, err); code != "omp_resume_unknown" {
		t.Fatalf("code %q (%v)", code, err)
	}
	if got := f.trace("tab.create"); len(got) != 0 {
		t.Fatalf("a tab was opened: %v", got)
	}

	// A file outside the agent's sessions directory, or with an apostrophe, is never run.
	root := filepath.Join(ompAgentDir(home), "sessions")
	for _, p := range []string{
		filepath.Join(root, "..", "x.jsonl"),
		filepath.Join(root, "a'b.jsonl"),
		filepath.Join(root, "a.txt"),
		"/etc/passwd",
	} {
		if ompResumePath(home, p) {
			t.Errorf("%s was taken as a resume", p)
		}
	}
	if !ompResumePath(home, filepath.Join(root, "-w", "t_uuid.jsonl")) {
		t.Error("a session file under the agent's sessions directory was refused")
	}
}

// The tab opened and no extension came: not_running, with the screen's last lines, and the wait is the pi resume's.
func TestOmpResumeWaitsThenRefuses(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	prev := piResumeWait
	piResumeWait = 300 * time.Millisecond
	t.Cleanup(func() { piResumeWait = prev })
	n, f, home, _ := piHerdrNode(t)
	cwd := t.TempDir()
	id := "01b10000-0000-7000-8000-000000000004"
	writeOmpSession(t, ompSessionsDir(home), "-tmp-z", id, "quiet", cwd, "herdr", time.Now())
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": filepath.Base(cwd)}}})
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	f.setScreen("w1:p2", "omp: starting\n")
	_, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": "hi", "client_msg_id": "m-wait"})
	if code, _ := refusalOf(t, err); code != "not_running" {
		t.Fatalf("code %q (%v)", code, err)
	}
	if got := f.trace("tab.create"); len(got) != 1 {
		t.Fatalf("tab.create %v", got)
	}
}

// herdr's own restore of the pane: an omp gets `omp --resume <file>` once its file is known, and no command at all
// (never `omp resume <pane>`) when it is not.
func TestOmpHerdrResumeReport(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, _, home, _ := piHerdrNode(t)
	id := "01b10000-0000-7000-8000-000000000005"
	file := filepath.Join(ompAgentDir(home), "sessions", "-w", "t_"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"type":"session","id":"`+id+`","cwd":"/w"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := agentLaunch{Agent: ompAgent, Launcher: ompAgent, ID: id}
	unknown := agentLaunch{Agent: ompAgent, Launcher: ompAgent, ID: "01b10000-0000-7000-8000-000000000006"}
	if argv, ok := n.agentResumeArgv(unknown, unknown.ID); ok {
		t.Fatalf("without a known file herdr was given %q", argv)
	}
	n.ompFiles = map[string]string{id: file}
	argv, ok := n.agentResumeArgv(spec, id)
	if !ok || argv[0] != "eval" || !strings.Contains(argv[1], "omp --resume "+file) {
		t.Fatalf("resume_argv %q %v", argv, ok)
	}
	if strings.Contains(argv[1], "omp resume") {
		t.Fatalf("the pane id was used as a session: %q", argv)
	}
}
