package node

import (
	"os/exec"
	"strings"
	"testing"
)

func TestOtherAgentStartCommand(t *testing.T) {
	const prompt = "a 'quoted' prompt; $(printf unexpected)"
	cmd := otherAgentCommand(traexAgent, "wrapper", "00000000-0000-4000-8000-000000000001", prompt)
	// Execute only a fake shell function: a first prompt must remain a single literal argument.
	out, err := exec.Command("sh", "-c", `wrapper() { printf '%s\n' "$#" "$@"; }; `+cmd).Output()
	if err != nil || string(out) != "3\nresume\n00000000-0000-4000-8000-000000000001\n"+prompt+"\n" {
		t.Fatalf("command arguments = %q (%v)", out, err)
	}
	if strings.Contains(cmd, "--session-id") || strings.Contains(cmd, "--settings") || strings.Contains(cmd, "--mcp-config") {
		t.Fatal("a non-Claude start must not receive Claude flags")
	}
}

// omp takes its resume as a flag, not as a subcommand: `omp --resume <id> <prompt>`. Typed as `omp resume <id>` it
// starts a new conversation with that id as the prompt.
func TestOmpStartCommand(t *testing.T) {
	const prompt = "a 'quoted' prompt; $(printf unexpected)"
	cmd := otherAgentCommand(ompAgent, "omp", "01a1bfd2-2b1c-7438-bbbf-b1361c7a041b", prompt)
	out, err := exec.Command("sh", "-c", `omp() { printf '%s\n' "$#" "$@"; }; `+cmd).Output()
	if err != nil || string(out) != "3\n--resume\n01a1bfd2-2b1c-7438-bbbf-b1361c7a041b\n"+prompt+"\n" {
		t.Fatalf("command arguments = %q (%v)", out, err)
	}
	if strings.Contains(cmd, " resume ") {
		t.Fatal("omp takes --resume, not a resume subcommand")
	}
	if strings.Contains(cmd, "--session-id") || strings.Contains(cmd, "--settings") || strings.Contains(cmd, "--mcp-config") {
		t.Fatal("omp must not receive Claude flags")
	}
	// A new omp session: the first message is the argument, nothing else.
	if cmd := otherAgentCommand(ompAgent, "omp", "", prompt); cmd != "omp "+shellQuote(prompt) {
		t.Fatalf("new omp start = %q", cmd)
	}
}

// A launcher found on the PATH as omp is omp, not the Claude Code it cannot be started as.
func TestOmpIsItsOwnAgentKind(t *testing.T) {
	for name, want := range map[string]string{
		"omp": ompAgent, "codex": "codex", traexAgent: traexAgent,
		"claude": "claude", "work_claude": "claude", "anything": "claude",
	} {
		if got := launcherAgentType(name); got != want {
			t.Fatalf("launcherAgentType(%q) = %q, want %q", name, got, want)
		}
	}
	// herdr has an integration for omp; telling it claude would make it look for a Claude that is not there.
	if got := herdrAgentName(ompAgent); got != ompAgent {
		t.Fatalf("herdrAgentName(omp) = %q", got)
	}
}

// The resume herdr restores an omp pane with is omp's own flag form.
func TestOmpResumeArgs(t *testing.T) {
	const id = "01a1bfd2-2b1c-7438-bbbf-b1361c7a041b"
	if got := resumeArgs(ompAgent, id); got != "--resume "+id {
		t.Fatalf("omp resume = %q", got)
	}
	if got := resumeArgs(traexAgent, id); got != "resume "+id {
		t.Fatalf("compatible CLI resume = %q", got)
	}
}
