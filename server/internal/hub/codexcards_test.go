package hub

import (
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCodexPushTitles(t *testing.T) {
	q := interactionPayload(proto.Interaction{Kind: proto.IxQuestion, Agent: "codex", Questions: []proto.Question{{Question: "Which color?"}}}, "server")
	p := interactionPayload(proto.Interaction{Kind: proto.IxPermission, Agent: "traex", Summary: "rm -rf build"}, "server")
	o := interactionPayload(proto.Interaction{Kind: proto.IxQuestion, Agent: "omp", Questions: []proto.Question{{Question: "Which one?"}}}, "server")
	alert := func(m map[string]any) map[string]any { return m["aps"].(map[string]any)["alert"].(map[string]any) }
	if alert(q)["title"] != "Codex 在问你" || alert(q)["body"] != "Which color?" || alert(p)["title"] != "TRAEX 需要你授权" {
		t.Errorf("%v / %v", alert(q), alert(p))
	}
	if alert(o)["title"] != "omp 在问你" {
		t.Errorf("omp question title = %v, want its own name", alert(o)["title"])
	}
}
