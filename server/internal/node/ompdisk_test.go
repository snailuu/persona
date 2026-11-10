package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
)

// writeOmpSession writes the head of an omp session file and returns its path. host "" leaves the persona-host stamp out.
func writeOmpSession(t *testing.T, sessions, dir, id, title, cwd, host string, modified time.Time) string {
	t.Helper()
	d := filepath.Join(sessions, dir)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	body := ""
	if title != "" {
		body += `{"type":"title","v":1,"title":` + q(title) + "}\n"
	}
	body += `{"type":"session","version":3,"id":"` + id + `","cwd":` + q(cwd) + `,"title":` + q(title) + "}\n"
	if host != "" {
		body += `{"type":"custom","customType":"persona-host","data":{"host":` + q(host) + `},"id":"c1"}` + "\n"
	}
	p := filepath.Join(d, "2026-11-09T00-00-00-000Z_"+id+".jsonl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, modified, modified); err != nil {
		t.Fatal(err)
	}
	return p
}

func ompSessionsDir(home string) string { return filepath.Join(home, ".omp", "agent", "sessions") }

// The recent stopped sessions are listed with their id, cwd, title, host and mtime, newest first; an old one is left
// out, and a second list within the cache window does not read the files again.
func TestOmpDiskScan(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, _, home := herdrNode(t)
	sessions := ompSessionsDir(home)
	now := time.Now()
	writeOmpSession(t, sessions, "-w-app", "01a10000-0000-7000-8000-000000000001", "fix the parser", "/w/app", "herdr", now.Add(-time.Minute))
	writeOmpSession(t, sessions, "-w-lib", "01a10000-0000-7000-8000-000000000002", "", "/w/lib", "", now.Add(-2*time.Minute))
	writeOmpSession(t, sessions, "-w-old", "01a10000-0000-7000-8000-000000000003", "old", "/w/old", "terminal", now.Add(-8*24*time.Hour))

	rows := n.ompSessions()
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	if rows[0].ID != "01a10000-0000-7000-8000-000000000001" || rows[0].Title != "fix the parser" ||
		rows[0].Cwd != "/w/app" || rows[0].Host != "herdr" || rows[1].ID != "01a10000-0000-7000-8000-000000000002" ||
		rows[1].Host != "" {
		t.Fatalf("rows %+v", rows)
	}
	reads := n.omps().reads
	_ = n.ompSessions()
	if n.omps().reads != reads {
		t.Fatalf("the files were read again within the cache window")
	}

	// A changed file is read again; an unchanged one is not.
	p := writeOmpSession(t, sessions, "-w-app", "01a10000-0000-7000-8000-000000000001", "fix the parser", "/w/app", "herdr", now)
	n.omps().at = time.Time{}
	_ = n.ompSessions()
	if n.omps().reads != reads+1 {
		t.Fatalf("one changed file read %d times", n.omps().reads-reads)
	}
	_ = p
}

// A file with a cut last line or a corrupt line does not lose the session: what parses is used.
func TestOmpDiskToleratesBadLines(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, _, home := herdrNode(t)
	sessions := ompSessionsDir(home)
	d := filepath.Join(sessions, "-w-x")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "01a10000-0000-7000-8000-000000000004"
	body := "not json\n" +
		`{"type":"session","version":3,"id":"` + id + `","cwd":"/w/x"}` + "\n" +
		`{"type":"custom","customType":"persona-host","data":{"host":"herdr"` + "\n" // cut by the writer
	if err := os.WriteFile(filepath.Join(d, "t_"+id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	n.omps().at = time.Time{}
	rows := n.ompSessions()
	if len(rows) != 1 || rows[0].ID != id || rows[0].Cwd != "/w/x" || rows[0].Host != "" {
		t.Fatalf("rows %+v", rows)
	}
}

// At most ompDiskMax rows, newest first; no directory at all is an empty list, no error.
func TestOmpDiskCapAndMissingDir(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, _, home := herdrNode(t)
	if rows := n.ompSessions(); len(rows) != 0 {
		t.Fatalf("no directory: %+v", rows)
	}
	n.omps().at = time.Time{} // the directory appears: the next list scans it
	sessions := ompSessionsDir(home)
	now := time.Now()
	for i := range ompDiskMax + 5 {
		id := fmt.Sprintf("01a10000-0000-7000-8000-%012d", i)
		writeOmpSession(t, sessions, "-w-many", id, "", "/w/many", "", now.Add(-time.Duration(i)*time.Second))
	}
	rows := n.ompSessions()
	if len(rows) != ompDiskMax {
		t.Fatalf("%d rows", len(rows))
	}
	if rows[0].ID != "01a10000-0000-7000-8000-000000000000" || !rows[0].Modified.After(rows[1].Modified) {
		t.Fatalf("not newest first: %+v", rows[:2])
	}
}

// A stopped session's row: no words, a notice in their place, and a send refused until it can be resumed.
func TestOmpOfflineRowAndCalls(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home := herdrNode(t)
	now := time.Now()
	id := "01a10000-0000-7000-8000-000000000005"
	writeOmpSession(t, ompSessionsDir(home), "-w-off", id, "stopped work", "/w/off", "herdr", now)

	list := listSessions(t, n)
	s := onlyRow(t, list, id)
	f.reset()
	if s.Agent != ompAgent || s.Status != "offline" || s.Title != "stopped work" || s.Launcher != ompAgent {
		t.Fatalf("row %+v", s)
	}
	if s.LastText != "" {
		t.Fatalf("the disk row carries words: %+v", s)
	}
	// A live row of the same id hides the disk one.
	if hidden := n.ompOfflineRows(map[string]bool{id: true}); len(hidden) != 0 {
		t.Fatalf("a live id was listed from disk: %+v", hidden)
	}

	res, err := request(t, n, "session.items", map[string]any{"session_id": id, "turns": 2})
	if err != nil || !strings.Contains(fmt.Sprint(res), ompOfflineNotice) {
		t.Fatalf("items %v %v", res, err)
	}
	// A send resumes it in herdr (ompresume.go); this session's directory is gone, so it is refused before a tab.
	if _, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": "go on", "client_msg_id": "m-1"}); !isRefusal(err, "not_running") {
		t.Fatalf("send: %v", err)
	}
	if got := f.trace(); len(got) != 0 {
		t.Fatalf("a stopped session asked herdr: %v", got)
	}
}

// A live omp the node knows only by its fallback id (no extension, no herdr integration) hides its session's file:
// that file is the one the running process is writing, not a stopped session to resume.
func TestOmpDiskHidesALivePane(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home := herdrNode(t)
	cwd := t.TempDir()
	id := "01a10000-0000-7000-8000-000000000007"
	writeOmpSession(t, ompSessionsDir(home), "-w-live", id, "live", cwd, "herdr", time.Now())
	seedAgents(t, n, f, ompPaneAgent("w1:p7", cwd, ""))
	list := listSessions(t, n)
	onlyRow(t, list, otherSession(host.Agent{Agent: ompAgent, PaneID: "w1:p7"}))
	for _, s := range list {
		if s.SessionID == id {
			t.Fatalf("a live pane's file was listed as stopped: %+v", s)
		}
	}
	if _, ok := n.ompSession(id); ok {
		t.Fatal("a live pane's file was taken as resumable")
	}
}
