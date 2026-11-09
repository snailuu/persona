// @vitest-environment jsdom
// "New agent" with omp (docs/integrations/omp.md): omp's own config.yml decides the model and thinking effort, so choosing an
// omp launcher hides the model row (and the Claude-only permission / effort / ultracode controls) and the start carries none of
// them, which node would refuse with `omp_options`.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Machine } from "../lib/types";

const state = vi.hoisted(() => ({ loaded: true, features: ["ultracode"] as string[], machines: [] as Machine[] }));
vi.mock("../lib/store", () => ({
  getState: () => state,
  useStore: (selector: (value: typeof state) => unknown) => selector(state),
  hubHas: (features: string[], feature: string, unknown = false) => (features.length ? features.includes(feature) : unknown),
}));
vi.mock("react-dom", async (original) => ({ ...(await original<typeof import("react-dom")>()), createPortal: (children: React.ReactNode) => children }));
const launched = vi.hoisted(() => [] as Record<string, unknown>[]);
vi.mock("../lib/new-agent", async (original) => ({ ...(await original<typeof import("../lib/new-agent")>()), launchAgent: (input: Record<string, unknown>) => { launched.push(input); return "local-1"; } }));

import { NewAgent } from "./NewAgent";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
let host: HTMLDivElement;
let root: Root;
const flush = () => act(async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); });
const button = (text: string) => [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === text)!;

beforeEach(() => {
  state.machines = [{ id: "mac", name: "Mac", online: true, claude_version: "2.1.320" }];
  launched.length = 0;
  localStorage.clear();
  localStorage.setItem("new.cwd.mac", "/code/app");
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/model-config/purposes/")) return json({ error: "not found" }, 404);
    if (url.includes("/launchers/omp/models")) {
      return json({ agent: "omp", models: [{ id: "default", name: "Default", default: true }, { id: "omp-model", name: "omp model" }] });
    }
    if (url.includes("/launchers/") && url.includes("/models")) {
      return json({ agent: "claude", models: [{ id: "default", name: "Default", default: true, efforts: ["low", "high"] }, { id: "opus", name: "Opus" }] });
    }
    if (url.includes("/launchers")) return json({ launchers: [{ name: "claude", agent: "claude" }, { name: "omp", agent: "omp" }] });
    if (url.includes("/usage/windows")) return json({ accounts: [] });
    return json({ path: "/code", entries: [] });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

async function open() {
  act(() => root.render(<NewAgent open onClose={() => {}} />));
  await flush();
}

describe("New agent: omp", () => {
  it("hides the model row for an omp launcher and sends no model, effort, permission mode or ultracode", async () => {
    await open();
    act(() => button("claude").click());
    await flush();
    expect(document.body.querySelector("button[aria-expanded]")).not.toBeNull();
    expect(document.body.querySelector('[data-testid="new-agent-ultracode"]')).not.toBeNull();

    act(() => [...document.body.querySelectorAll<HTMLButtonElement>("button[aria-pressed]")].find((b) => b.textContent?.trim().endsWith("omp"))!.click());
    await flush();
    expect(document.body.querySelector("button[aria-expanded]")).toBeNull();
    expect(document.body.querySelector('[data-testid="new-agent-ultracode"]')).toBeNull();
    expect(document.body.textContent).not.toContain("思考强度");
    expect(document.body.textContent).not.toContain("权限模式");

    act(() => button("启动").click());
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({ machineId: "mac", cwd: "/code/app", launcher: "omp" });
    expect(launched[0].model).toBeUndefined();
    expect(launched[0].effort).toBeUndefined();
    expect(launched[0].permissionMode).toBeUndefined();
    expect(launched[0].ultracode).toBeUndefined();
  });
});
