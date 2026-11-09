import { t } from "../i18n";
import { isEcho, type Seen } from "./echo";
import { api, ApiError, errorText, sessionPath } from "./api";
import { parseUserText } from "./turns";
import { imagePayload, quotePath, releaseAttachments, sendFile, type LocalAttachment } from "./attachments";
import { uuid } from "./format";
import { currentPlatform, modName, type Platform } from "./shortcuts";
import { href, navigate } from "./router";
import { getState, putLaunch, upsertSession } from "./store";
import { ultracodeErrorText, ultracodeRefused } from "./ultracode";
import type { Session } from "./types";

export interface AgentLaunch {
  id: string;
  machineId: string;
  cwd: string;
  launcher: string;
  /** The agent the launcher runs (launcher-models.ts launcherAgent): "" or "claude": Claude Code, started in the
   *  background with its first message as an ordinary message. An agent with a session id of its own (codex, omp) is
   *  started with its first message in `prompt` and `agent_type` set. */
  agent?: string;
  /** Picked in "New agent" for this start only (launcher-models.ts startPicks); none: the launcher's own. */
  model?: string;
  effort?: string;
  /** Claude Code's permission mode for this start ("New agent"'s "Permission mode"); none: the launcher's own. */
  permissionMode?: string;
  /** The engine host to open in (#2576), when the user picked one other than the machine's default; none: the default. */
  host?: string;
  /** Turn ultracode on once Claude is up (#3428); only ever true: off is what a new Claude is (docs/protocol/protocol.md). */
  ultracode?: boolean;
  text: string;
  attachments: LocalAttachment[];
  todoId?: string;
  phase: "creating" | "uploading" | "sending" | "failed" | "ready";
  session?: Session;
  outgoing?: { text: string; images: { mime: string; data: string }[] };
  uploaded: Record<string, string>;
  error?: string;
  uncertain?: boolean;
  retryNeedsNewId?: boolean;
  messageId?: string;
  clearable?: string;
  draft?: string;
}

export function launchAgent(input: Pick<AgentLaunch, "machineId" | "cwd" | "launcher" | "agent" | "model" | "effort" | "permissionMode" | "host" | "ultracode" | "text" | "attachments" | "todoId">) {
  const launch: AgentLaunch = { ...input, id: `local-${uuid()}`, phase: "creating", uploaded: {} };
  putLaunch(launch);
  navigate({ name: "session", mid: launch.machineId, sid: launch.id });
  // Let the local conversation paint before serialization, uploads and remote startup.
  setTimeout(() => void deliverLaunch(launch.id), 0);
  return launch.id;
}

/** The client_msg_id the first message goes with (a retry the hub may have taken gets a new one, StartingAgent). */
export function launchMessageId(launch: Pick<AgentLaunch, "id" | "messageId">): string {
  return launch.messageId ?? launch.id;
}

/** `user` (a user message, or an item the hub tied to a message) is the copy of a started conversation's first message. */
export function acknowledgeLaunch(mid: string, sid: string, user: Seen) {
  for (const launch of Object.values(getState().launches)) {
    if (launch.phase !== "ready" || launch.machineId !== mid || launch.session?.session_id !== sid || !launch.outgoing) continue;
    // the conversation's first message: nothing older in it can say the same, so no time to compare
    if (isEcho({ id: launchMessageId(launch), text: parseUserText(launch.outgoing.text).text, images: launch.outgoing.images, at: 0 }, user)) putLaunch({ ...launch, outgoing: undefined });
  }
}

const delivering = new Set<string>();

export async function deliverLaunch(id: string, clearDraft?: string) {
  let launch: AgentLaunch = getState().launches[id];
  if (!launch || delivering.has(id) || (launch.phase !== "failed" && launch.phase !== "creating")) return;
  delivering.add(id);
  const controller = new AbortController();
  const deadline = setTimeout(() => controller.abort(), 240_000);
  const update = (patch: Partial<AgentLaunch>) => {
    launch = { ...launch, ...patch };
    if (getState().launches[id]) putLaunch(launch);
  };
  update({ phase: launch.session ? "uploading" : "creating", error: undefined, uncertain: false, clearable: undefined, draft: undefined });
  try {
    const imageBytes = launch.attachments.filter((a) => a.mime.startsWith("image/")).reduce((total, a) => total + Math.ceil(a.file.size / 3) * 4, 0);
    if (imageBytes + new TextEncoder().encode(launch.text).byteLength > 31 * 1024 * 1024) throw new Error(t("lib-new-agent.attachments_too_large_start"));
    if (!launch.session) {
      let session: Session;
      // Only Claude Code has a session id before it starts: it is started in the background and the first message goes
      // as an ordinary message once it is up. Another agent (codex, omp) picks its own id, so it starts with the first
      // message in `prompt` and `agent_type` set (#1842).
      const withPrompt = Boolean(launch.agent) && launch.agent !== "claude";
      // An agent started with its first message takes no uploads: there is no transcript for Persona to attach a file to.
      if (withPrompt && launch.attachments.length) throw new Error(t("lib-new-agent.launcher_no_attachments"));
      try {
        const r = await api<{ session: Session }>("/sessions", { method: "POST", signal: controller.signal, json: withPrompt
          ? { machine_id: launch.machineId, cwd: launch.cwd, launcher: launch.launcher || undefined, agent_type: launch.agent, model: launch.model || undefined, host: launch.host || undefined, prompt: launch.text || undefined, todo_id: launch.todoId || undefined }
          : { machine_id: launch.machineId, cwd: launch.cwd, launcher: launch.launcher || undefined, model: launch.model || undefined, effort: launch.effort || undefined, permission_mode: launch.permissionMode || undefined, host: launch.host || undefined, ultracode: launch.ultracode === true ? true : undefined, async: true, name_hint: launch.text || undefined, todo_id: launch.todoId || undefined } });
        session = r.session;
        if (withPrompt) update({ outgoing: { text: "", images: [] } });
      } catch (e) {
        if (!(e instanceof ApiError) || e.code !== "codex_needs_prompt") throw e;
        if (launch.attachments.length) throw new Error(t("lib-new-agent.launcher_no_attachments"));
        if (!launch.text) throw new Error(t("lib-new-agent.launcher_needs_text"));
        // A Codex start takes the picked model but no effort.
        const r = await api<{ session: Session }>("/sessions", { method: "POST", signal: controller.signal, json: { machine_id: launch.machineId, cwd: launch.cwd, launcher: launch.launcher || undefined, agent_type: "codex", model: launch.model || undefined, host: launch.host || undefined, prompt: launch.text, todo_id: launch.todoId || undefined } });
        session = r.session;
        update({ outgoing: { text: "", images: [] } });
      }
      if (!getState().launches[id]) return;
      update({ session });
      upsertSession(session);
    }
    if (!launch.outgoing) {
      update({ phase: "uploading" });
      const paths: string[] = [];
      const images: { mime: string; data: string }[] = [];
      for (const att of launch.attachments) {
        if (att.mime.startsWith("image/")) {
          images.push(await imagePayload(att.file));
        } else {
          let path = launch.uploaded[att.id];
          if (!path) {
            path = await sendFile(launch.machineId, att.file, { name: att.name, signal: controller.signal });
            update({ uploaded: { ...launch.uploaded, [att.id]: path } });
          }
          paths.push(quotePath(path));
        }
      }
      update({ outgoing: { text: [launch.text, ...paths].filter(Boolean).join("\n"), images } });
    }
    if (!getState().launches[id]) return;
    controller.signal.throwIfAborted();
    const out = launch.outgoing!;
    if (out.text || out.images.length) {
      update({ phase: "sending" });
      const payload = { ...out, client_msg_id: launchMessageId(launch), clear_draft: clearDraft, todo_id: launch.todoId || undefined };
      if (new TextEncoder().encode(JSON.stringify(payload)).byteLength > 32 * 1024 * 1024) throw new Error(t("lib-new-agent.attachments_too_large_retry"));
      await api(`${sessionPath(launch.machineId, launch.session!.session_id)}/messages`, {
        method: "POST", signal: controller.signal, json: payload,
      });
    }
    update({ phase: "ready" });
    releaseAttachments(launch.attachments);
    update({ attachments: [] });
    const local = { name: "session" as const, mid: launch.machineId, sid: launch.id };
    if (location.pathname === href(local)) navigate({ ...local, sid: launch.session!.session_id }, true);
  } catch (e) {
    const phase: AgentLaunch["phase"] = getState().launches[id]?.phase ?? "failed";
    const sending = phase === "sending";
    const creating = phase === "creating";
    const uncertain = (creating || sending) && (e instanceof TypeError || (e instanceof DOMException && e.name === "AbortError") || (e instanceof ApiError && (e.body.delivered === "unknown" || (e.status >= 500 && e.body.delivered !== "none"))));
    // the hub won't start it with ultracode here (an old node, a CLI or engine that can't take it): asking again would
    // get the same answer, so "Retry" starts it without, and the card says so (#3428)
    const noUltracode = creating && launch.ultracode === true && ultracodeRefused(e);
    update({ phase: "failed", uncertain, retryNeedsNewId: e instanceof ApiError && e.body.delivered === "unknown",
      ...(noUltracode ? { ultracode: undefined } : {}),
      error: noUltracode ? t("ultracode.start_refused", { error: ultracodeErrorText(e) }) : t(creating ? "lib-new-agent.start_failed" : sending ? "lib-new-agent.first_message_failed" : "lib-new-agent.attachments_failed", { error: errorText(e) }),
      clearable: e instanceof ApiError && e.code === "input_draft" && typeof e.body.clearable === "string" ? e.body.clearable : undefined,
      draft: e instanceof ApiError && e.code === "input_draft" ? String(e.body.draft ?? "") : undefined,
    });
  } finally {
    clearTimeout(deadline);
    delivering.delete(id);
  }
}

// ── opening "New agent" from another page ──────────────

/** What "New agent" opens with when another page opens it ("Knowledge"'s "Start agent"): the first message, and where if known. */
export interface NewAgentPreset {
  prompt?: string;
  machineId?: string;
  cwd?: string;
}

const openers = new Set<(preset: NewAgentPreset) => void>();

/** Opens "New agent" (the app shell listens) with `preset` filled in. */
export function openNewAgent(preset: NewAgentPreset = {}) {
  openers.forEach((l) => l(preset));
}

export function onOpenNewAgent(l: (preset: NewAgentPreset) => void) {
  openers.add(l);
  return () => {
    openers.delete(l);
  };
}

/**
 * The keys that start "New agent" from the first-message box, for its placeholder: Mod ↵ (⌘ on Apple, Ctrl elsewhere, as
 * the shortcuts name it), and none on a touch-only device (a phone has no ⌘ key, so the hint would only take up room).
 */
export function startKeys(platform: Platform, touchOnly: boolean): string | null {
  return touchOnly ? null : `${modName(platform)} ↵`;
}

/** startKeys for this browser. */
export function browserStartKeys(): string | null {
  const touchOnly = typeof matchMedia === "function" && matchMedia("(hover: none) and (pointer: coarse)").matches;
  return startKeys(currentPlatform(), touchOnly);
}

/** The first message with a preset: its text first, then what was already being written (a draft isn't lost). */
export function presetPrompt(preset: string | undefined, draft: string): string {
  const p = (preset ?? "").trimEnd();
  if (!p) return draft;
  if (!draft.trim()) return `${p}\n\n`;
  if (draft.startsWith(p)) return draft;
  return `${p}\n\n${draft.trim()}`;
}

