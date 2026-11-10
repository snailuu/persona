package node

import (
	"time"

	"github.com/nwverse/persona/server/internal/host"
)

// The agents Persona reads as pi (pi.go): pi itself, and omp (Oh My Pi), a pi fork whose extension API, session files and
// conversation model are pi's (docs/integrations/omp.md). A pane and its keys are the host's; the conversation and the
// control (send, interrupt, cards, live text) are the Persona extension in that same process. Both start in herdr and
// resume there, with their own command.

const (
	piAgent  = "pi"
	ompAgent = "omp"
)

// piFamily says whether agent is one Persona reads as pi.
func piFamily(agent string) bool { return agent == piAgent || agent == ompAgent }

// piFamilyNoExtension is what a running agent of the family says without Persona's extension: its terminal only, and
// how to install the extension.
func piFamilyNoExtension(agent string) string {
	if agent == ompAgent {
		return "这个 omp 没有装 Persona 的扩展：手机上只能看终端。装上（omp install <Persona>/integrations/omp）再重开 omp，就能看对话、发消息"
	}
	return piNoExtension
}

// piFamilyCode is the code a refused send to a running agent of the family carries without its extension (pi's own
// code is pi_no_extension, unchanged).
func piFamilyCode(agent string) string { return agent + "_no_extension" }

// noteFamilyPanes remembers when each family pane listed only by its fallback id (omp-w1-p2) was first seen, and
// forgets the panes gone. Claude's own first-seen map is paneSeen (panes_bind.go); this one is the family's, so a
// stopped session's file can be told from the one a live pane is writing (ompdisk.go).
func (n *Node) noteFamilyPanes(fallbacks []host.Agent) {
	n.mu.Lock()
	defer n.mu.Unlock()
	first := n.familySeen == nil
	if first {
		n.familySeen = map[string]time.Time{}
	}
	now, here := time.Now(), make(map[string]bool, len(fallbacks))
	for _, a := range fallbacks {
		here[a.PaneID] = true
		if _, ok := n.familySeen[a.PaneID]; !ok && !first {
			n.familySeen[a.PaneID] = now
		} else if !ok {
			n.familySeen[a.PaneID] = time.Time{}
		}
	}
	for p := range n.familySeen {
		if !here[p] {
			delete(n.familySeen, p)
		}
	}
}

// familyPane is a live family pane the node knows only by its fallback id: where it runs, and when the pane was first
// seen (zero: it was there before this node started).
type familyPane struct {
	cwd  string
	seen time.Time
}

// piFamilyFallbackPanes are those panes now.
func (n *Node) piFamilyFallbackPanes() []familyPane {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []familyPane
	for id, a := range n.agents {
		if a.PaneID == "" || !piFamily(a.Agent) || validID(id) {
			continue // a real session: its row is the session's, not the pane's
		}
		out = append(out, familyPane{cwd: a.Cwd, seen: n.familySeen[a.PaneID]})
	}
	return out
}

// piFamilyIDGrace is how long a start of the family waits for the extension to report the new session's id after the
// host has reported its pane: without one the pane's fallback id (omp-w1-p2) is returned instead.
var piFamilyIDGrace = 8 * time.Second

// paneAlias is a fallback row id (omp-w1-p2) and the pane it is the row of.
type paneAlias struct{ id, pane string }

// resolveAlias turns a fallback row id a client opened (or sent to) into the session its pane turned out to run.
func (n *Node) resolveAlias(id string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if a, ok := n.idAlias[id]; ok {
		return a.id
	}
	return id
}
