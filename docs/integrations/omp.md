# omp (Oh My Pi)

omp (Oh My Pi) is a pi fork with the same extension API, session files and conversation model. Persona reads it
as a pi-family agent (`docs/integrations/pi.md`): in a herdr pane it appears in Agents with lists and status, chat content (text,
thinking, tool calls and results, images), token-by-token output, sending messages from the phone, interrupting, renaming, starting a
new omp with a first message, answering its `ask_user` questions and approving the tools you list. Its stopped sessions are listed
from disk and resumed in herdr.

Verified with omp 18.18.x and herdr 0.9.5. Everything here needs a herdr engine; no other engine starts, lists or resumes omp.

## Scope

| Works from the phone | Not supported |
| --- | --- |
| Start `omp` in a new herdr tab, with the first message | Choosing its model, thinking effort, permission mode or ultracode (omp's own `config.yml` decides) |
| List running omp sessions and their state | engines other than herdr (Orca, Paseo, Herdr App), and Windows |
| Read the chat, including live text | a web client for omp (the equivalent of pi-web) |
| Send messages, interrupt, rename | sessions under an `omp --profile` directory |
| Answer `ask_user` questions and `approve` cards | automatic updates of the Persona extension |
| List stopped sessions from the last 7 days, and resume one | plan approval (omp has none), ultracode, branches and to-dos |

Persona never installs or edits herdr's omp integration, and never writes into omp's agent directory; only the extension described
here is yours to install.

## Installation

On the computer running omp (it must be in the hub's machine list, and its node must be able to run omp):

1. Install the Persona extension in omp from this checkout:

   ```sh
   omp install <Persona>/integrations/omp
   ```

   `omp install` links a local directory; it does not copy it. The extension follows the checkout, so update the checkout (and
   restart omp) to update it; there is no automatic update. Install only this package into omp: the pi extension in the same omp
   would report the same sessions under pi's identity, and node keeps one owner per session.
2. To use omp in herdr, make sure herdr sees it as an agent: herdr 0.9.5 recognizes `omp` itself. Optionally install herdr's
   own omp integration (`herdr integration install omp`), which reports the session file it writes for the pane: node can then
   resume a stopped session from that file directly. Without it, herdr still reports the pane as omp, and the Persona extension
   reports the pane, so the session is listed; Persona never installs or changes this integration itself.
3. Restart omp. From then on its sessions reach the node; no companion process is needed.

### Settings

Optional, in `<agent dir>/persona.json` — `~/.omp/agent/persona.json` by default, or `$PI_CODING_AGENT_DIR/persona.json` when omp
is told to keep its agent directory elsewhere (the same variable omp reads):

```json
{
  "approve": ["bash", "write"],
  "askUser": true,
  "answerFirst": "auto"
}
```

| Field | Default | Description |
| --- | --- | --- |
| `approve` | `[]` | Tools whose calls ask for approval on the phone before they run, such as `["bash", "write"]`. omp itself has no permission dialog; nothing is blocked by default. The phone card's "Always approve" records the tool for that session in the session file, so it stays approved when another omp process resumes it |
| `askUser` | `true` | Gives the model an `ask_user` tool (question, optionally with choices), answered on the phone or in omp's terminal |
| `answerFirst` | `auto` | Who gets approvals and questions first: `auto` = the phone if it drove this session within the last 6 hours, otherwise local first; `phone` / `local` fixes one side |
| `socket` | `~/.persona/runtime.sock` | The node's runtime socket, when it is not the default. A full path; a leading `~` is not expanded here |

## How a session is identified

The node takes an omp session's id from the first source that has it:

1. **herdr's omp integration**, which reports the session file it wrote (`agent_session {kind: "path"}`) for the pane. The id is
   the session id in that file.
2. **the Persona extension in that omp process**, which lists the session with the pane it runs in (`pane_id` = `HERDR_PANE_ID`).
   Node joins that runtime row and herdr's pane into one session row.
3. **a fallback id**, `omp-<pane>` (for example `omp-w1-p2`), when neither has answered 8 seconds after herdr reports the pane.
   The row is usable at once; when the real id arrives, node aliases the fallback id to it, so an open chat keeps working and the
   list keeps one row. Closing the pane drops the alias.

Without the extension, a running omp is still listed, with its terminal only: the chat explains that the extension is missing
(`omp install <Persona>/integrations/omp`) and sending is refused (`omp_no_extension`).

## Stopped sessions and resume

Node reads omp's own session files — `<agent dir>/sessions/<encoded cwd>/<time>_<uuid>.jsonl` — and lists the recent ones as
offline rows: files changed in the last 7 days, newest first, at most 80. It reads only the title, session id, directory and the
last recorded host, never the conversation; the scan is cached for 15 seconds and stops waiting after 3 seconds, so a slow disk
keeps the previous result. A session listed from disk is a row of the runtime when an omp process still has it, and node's offline
row otherwise (one row per session).

A running omp that only its pane names (no extension, no herdr omp integration) hides the stopped sessions of its directory: node
cannot tell which file that process is writing, so it lists the live row alone and refuses a send aimed at such a file
(`omp_resume_unknown`) instead of starting a second omp on it. The rows come back when the omp exits.

Sending to a stopped session resumes it in a new herdr tab, in the session's directory, with `omp --resume <session file>`; node
waits for the extension to list the session again, then sends the message. The file comes only from node's own scan or from what
herdr's omp integration reported while the session ran — never from the client — and must be a `.jsonl` file under
`<agent dir>/sessions`; a file with an apostrophe in its path is not used (herdr cannot quote it). Without a usable file the resume
is refused (`omp_resume_unknown`) and nothing starts. If herdr is unavailable, the request is refused (`engine_missing`); omp is
never handed to another engine. A stopped session answers a rename, interrupt, permission-mode change, queue edit or close with a
refusal (`unsupported`): it has no terminal until a message resumes it.

Herdr's own restore after a restart also gets the session file from node: for a session Persona started, the reported resume
command is the launcher and `--resume <file>`; an omp whose file node does not know gets no command (and a log line), rather than
a guess.

## Differences from pi

- **No pi-web and no companion process.** omp has no web client; its sessions live in its own process or on disk, and node reads the
  disk itself (`server/internal/node/ompdisk.go`) where pi needs a companion process.
- **Its own agent directory.** `~/.omp/agent`, or `$PI_CODING_AGENT_DIR` when set; pi's `~/.pi/agent` is never read. Settings and
  sessions come from there, and node's disk scan (and its resume path check) use the same directory.
- **Its own runtime identity and slot.** runtime `omp-<pid>`, apps' name and agent `omp`, card ids starting with `omp`. The extension
  keeps its host in `__personaOmp` (`integrations/pi` uses `__personaPi`), so an omp and a pi loaded in the same process do not
  collide.
- **Its own resume command.** `omp --resume <session file>`; pi resumes with `pi --session`.
- **No automatic update.** omp has no package store integration; `omp install` links a checkout, which you update yourself.

Everything else — the extension API, the conversation mapping, approval and question handling, live text, the host's record of
where a session last ran — is pi's code under omp's identity.

## Known limitations

- Only omp's default agent directory is listed. Sessions started with `omp --profile <name>` live elsewhere and are not listed.
  Node's disk scan and resume path follow `PI_CODING_AGENT_DIR` only when it names an `…/.omp/agent` directory (so a pi directory is
  never taken for omp); the extension itself writes `<agent dir>/persona.json` wherever omp is told to keep its settings.
- A launcher is treated as omp only when its name is exactly `omp` (or a runtime says so). A wrapper script or alias around omp is
  a Claude launcher unless `hub.json`'s `machines[].launchers` marks it (`agent_type: "omp"`, `docs/operations/config.md`).
- A start with a model, permission mode, thinking effort or ultracode is refused with `omp_options`, before anything runs.
- A resume needs a session file node can trust; a foreign `PI_CODING_AGENT_DIR` profile, a deleted file or a path outside the
  agent directory is refused rather than guessed at.
- `omp -p`, `--print` and `--mode json` do not register with Persona (one-shot runs have nothing to drive from the phone).

## Validation

- `cd integrations/omp && npm test`: agent directory and `persona.json` parsing, the omp identity (`agent` / `name` / runtime
  `omp-<pid>`), the `ask_user` and one-shot rules, and the slot shared with pi in one process.
- node (fake herdr and fake runtimes): `server/internal/node/pi_test.go` covers the family's rows, routing, missing extension and
  resume; `server/internal/node/ompdisk.go`'s tests cover the disk scan and session parsing, and `ompresume.go`'s tests cover the
  file rules and refusal.
