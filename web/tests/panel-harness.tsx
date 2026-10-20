import React from "react";
import { createRoot } from "react-dom/client";
import PanelsSlot from "../src/stars/PanelsSlot";
import {
  DataProvider,
  api,
  createMockDataSource,
  createProjectStore,
} from "../src/data";
import { createStarsUI } from "../src/stars/ui";
import { hives, hiveAgents } from "../src/fixtures/hive-console";
import { methodologyResponses } from "../src/fixtures/round4";
import "../src/stars/stars.css";
const ui = createStarsUI({ hiveId: "swarmjet" });
const mock = createMockDataSource(
  {
    swarmjet: {
      hive: hives[0],
      localAddress: "local:web-admin",
      agents: hiveAgents.swarmjet.filter((a) => a.role !== "queen"),
      queen: {
        enabled: true,
        state: "active",
        model: "test/model",
        agentId: "queen",
        lastTurnAt: null,
      },
      prs: [
        {
          number: 88,
          headSha: "panel-observed-head",
          ready: true,
          merged: false,
          state: "open",
        },
      ],
      queue: [],
      signals: [
        {
          messageId: "signal-full",
          source: "github",
          kind: "issue",
          number: 42,
          title: "待处理任务",
          url: "https://github.example/issue/42",
          claimed: false,
          claimedBy: "",
          createdAt: "2026-10-20T00:00:00Z",
        },
      ],
      methodology: methodologyResponses[3],
      methodologyEvents: [],
      messages: [],
    },
  },
  { ...api },
);
mock.setSnapshot("orders", {
  ...mock.snapshots.swarmjet,
  hive: hives[1],
  agents: [],
  prs: [],
  methodology: { ...methodologyResponses[3], hiveId: "orders", version: 1 },
});
const ordersStore = createProjectStore("orders", mock.source);
ordersStore.start();
const store = createProjectStore("swarmjet", mock.source);
store.start();
const props = {
  ui,
  stores: new Map([
    ["swarmjet", store],
    ["orders", ordersStore],
  ]),
  projects: [
    { hive: hives[0], activeForagers: 1, pendingApprovals: 1, inboxBacklog: 0 },
  ],
  principal: { id: "admin", username: "admin" },
  onLogout: () => api.logout(),
  reloadProjects: () => {},
};
createRoot(document.getElementById("root")!).render(
  <DataProvider source={mock.source}>
    <main
      className="stars-app"
      style={
        {
          "--paper": "#fcfcf9",
          "--veil": "#fcfcf9",
          "--ink": "#1b2233",
          "--ink2": "#515967",
          "--ink3": "#88909c",
          "--hair": "#ccc",
          "--accent": "#94701f",
          "--body": "serif",
          "--title": "serif",
        } as React.CSSProperties
      }
    >
      <div
        aria-label="测试入口"
        style={{ position: "fixed", top: 0, left: 450, zIndex: 30 }}
      >
        <button onClick={() => ui.openProject("methodology")}>项目</button>
        <button
          onClick={() => {
            ui.selectProject("orders");
            ui.openProject("methodology");
          }}
        >
          切到B项目
        </button>
        <button
          onClick={() =>
            mock.push("swarmjet", {
              type: "pr",
              data: {
                hiveId: "swarmjet",
                number: 88,
                headSha: "panel-observed-head",
                ready: false,
                merged: false,
                state: "open",
              },
            })
          }
        >
          PR条件变化
        </button>
        <button
          onClick={() =>
            mock.push("swarmjet", {
              type: "pr",
              data: {
                hiveId: "swarmjet",
                number: 88,
                headSha: "panel-new-head",
                ready: true,
                merged: false,
                state: "open",
              },
            })
          }
        >
          PR新提交
        </button>
        <button
          onClick={() =>
            ui.selectObject({
              hiveId: "swarmjet",
              kind: "agent",
              id: hiveAgents.swarmjet.find((a) => a.role !== "queen")!.id,
            })
          }
        >
          Agent详情
        </button>
        <button
          onClick={() =>
            ui.selectObject({
              hiveId: "swarmjet",
              kind: "queen",
              id: "swarmjet:queen",
            })
          }
        >
          主Agent详情
        </button>
        <button
          onClick={() =>
            ui.selectObject({
              hiveId: "swarmjet",
              kind: "pr",
              id: "swarmjet:pr:88",
              number: 88,
            })
          }
        >
          PR详情
        </button>
        <button
          onClick={() =>
            ui.selectObject({
              hiveId: "swarmjet",
              kind: "signal",
              id: "signal-full",
              number: 42,
            })
          }
        >
          事件详情
        </button>
        <button onClick={() => ui.openPlatform("setup", "github=connected")}>
          平台
        </button>
        <button
          onClick={() =>
            mock.push("swarmjet", {
              type: "message",
              data: {
                id: "live-full",
                hiveId: "swarmjet",
                from: "queen",
                to: "hive",
                kind: "report",
                chainId: "live",
                trust: "agent",
                createdAt: "2026-10-20T00:00:01Z",
                preview: "短预览",
              },
            })
          }
        >
          消息事件
        </button>
      </div>
      <PanelsSlot {...props} />
    </main>
  </DataProvider>,
);
