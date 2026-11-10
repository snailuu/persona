//go:build omplive

package node

// Against a real herdr and a real omp, in a home of its own (never the user's): the omp in a herdr pane, loaded with
// Persona's extension, is listed by the node with its pane. The script drives it:
//
//	scripts/test/test-omp.sh
//
// Nothing of the user's is touched: HOME, XDG_CONFIG_HOME and the persona directory are a fresh temp directory, and the
// herdr session is its own. No message is sent to omp, so no model request is made.

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
)

func TestLiveOmp(t *testing.T) {
	root := os.Getenv("PERSONA_OMP_LIVE")
	if root == "" || os.Getenv("HERDR_SOCKET_PATH") == "" || os.Getenv("PERSONA_RUNTIME_SOCKET") == "" {
		t.Skip("PERSONA_OMP_LIVE, HERDR_SOCKET_PATH and PERSONA_RUNTIME_SOCKET are not set")
	}
	n, _ := testNode(t)
	n.dir = filepath.Join(root, "persona")
	if err := os.MkdirAll(n.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	n.logger = log.New(io.Discard, "", 0)
	n.hosts = herdrHosts(herdr.New())
	rh := NewRuntimeHost("test", os.Getenv("PERSONA_RUNTIME_SOCKET"))
	rh.logger = log.New(io.Discard, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rh.Listen(ctx); err != nil {
		t.Fatal(err)
	}
	n.runtimeSock = rh.path
	rh.attach(n)

	deadline := time.Now().Add(45 * time.Second)
	for {
		if err := n.refreshHosts(ctx); err != nil {
			t.Logf("herdr: %v", err)
		}
		list := n.sessionList(ctx, false)
		for _, s := range list {
			if s.Agent == ompAgent && s.PaneID != "" {
				t.Logf("LIVE omp row %s pane %s title %q", s.SessionID, s.PaneID, s.Title)
				return
			}
		}
		if time.Now().After(deadline) {
			var rows []string
			for _, s := range list {
				rows = append(rows, s.Agent+":"+s.PaneID+":"+s.SessionID)
			}
			t.Fatalf("no omp row in 45s; rows %v", rows)
		}
		time.Sleep(time.Second)
	}
}
