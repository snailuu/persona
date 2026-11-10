package node

// What a stopped omp session (an omp disk row, ompdisk.go) answers: a notice in place of the conversation, and no
// sending — until the node can resume it in herdr.

import (
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const ompOfflineNotice = "这个 omp 会话已停止。发一条消息，会在电脑的 herdr 里接着它运行。"

// ompOfflineCalls answers the calls only a reader of the conversation could, for a stopped session: an empty
// conversation saying it is stopped, and a send refused (its session file is not used to resume here yet). Terminal
// calls are not this branch's: a stopped session has no terminal.
func (n *Node) ompOfflineCalls(method, id string) (any, error, bool) {
	switch method {
	case "session.items":
		return map[string]any{"items": []proto.Item{{ID: "omp#0", Seq: 1, Role: "system", Kind: "notice",
			Text: ompOfflineNotice, TS: time.Now().UTC()}}, "has_more": false, "history": "omp-disk:" + id, "to_seq": 1}, nil, true
	case "session.item", "session.image":
		return nil, fmt.Errorf("no item"), true
	case "session.watch", "session.unwatch":
		return okResult(nil), nil, true
	case "session.send":
		return nil, refusal("omp_resume_unknown", "这个 omp 会话找不到会话文件，请在电脑上恢复，这次没有发送。", nil), true
	}
	return nil, nil, false
}
