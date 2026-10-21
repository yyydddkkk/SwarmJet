// Development-only entry for visual review. Vite's production entry never imports it.
import { createRoot } from "react-dom/client";
import StarsApp from "../src/stars/StarsApp";
import { createMockDataSource, type Message } from "../src/data";
import { round8Project } from "../src/fixtures/round8";
import { modelCatalog, swarmjetUsage } from "../src/fixtures/round4";
import { methodologyTemplates } from "../src/fixtures/round5";
const snapshot = round8Project();
snapshot.hive.name = "test-for-myself";
snapshot.hive.repository = "yyydddkkk/test-for-myself";
snapshot.messages[0] = { ...snapshot.messages[0], from: `forager:${snapshot.agents[0].id}`, to: `forager:${snapshot.agents[0].id}` };
snapshot.prs[1].attestations = [{
  id: "mock-verification", hiveId: "swarmjet", prNumber: 2,
  headSha: "b".repeat(40), attesterId: snapshot.agents[9].id,
  authorId: snapshot.agents[1].id, isAuthor: false, result: "pass_notes",
  summary: "独立验证已覆盖旧数据与失败路径。".repeat(25),
  superseded: false, createdAt: new Date(Date.now() - 3600000).toISOString(),
}];
const mock = createMockDataSource({ swarmjet: snapshot }, {
  getSession: async () => ({ id: "preview-admin", username: "演示管理员", platformAdmin: true }),
  listHives: async () => ({ hives: [{ hive: snapshot.hive, activeForagers: 4, pendingApprovals: 3, inboxBacklog: 0 }], localAddress: snapshot.localAddress }),
  getHive: async () => ({ hive: snapshot.hive, localAddress: snapshot.localAddress }),
  getQueen: async () => snapshot.queen,
  listForagers: async () => ({ foragers: snapshot.agents, queen: snapshot.queenAgent ?? undefined, localAddress: snapshot.localAddress }),
  getForager: async (id) => ({ forager: snapshot.agents.find((agent) => agent.id === id)!, hive: snapshot.hive, localAddress: snapshot.localAddress }),
  listMessages: async (_id, options) => ({ to: options.to, messages: snapshot.messages.filter((message): message is Message => message.to === options.to && "body" in message), truncated: false }),
  listModels: async () => structuredClone(modelCatalog),
  getUsage: async () => structuredClone(swarmjetUsage),
  listTokens: async () => ({ tokens: [] }),
  listGithubRepositories: async () => ({ repositories: [] }),
  listMethodologyTemplates: async () => structuredClone(methodologyTemplates),
  listMethodologyVersions: async () => ({ hiveId: "swarmjet", activeVersion: snapshot.methodology.version, versions: [snapshot.methodology], changes: [] }),
  listMethodologyEvents: async () => ({ events: [] }),
  listQueue: async () => ({ queue: snapshot.queue }),
  getSetupStatus: async () => ({
    github: { configured: true, source: "database", appId: 1, appSlug: "demo", htmlUrl: "https://example.test/app", installUrl: "https://example.test/install", installations: 1 },
    tunnel: { enabled: true, url: "https://example.test", webhookSynced: true, lastError: "" },
    cells: { enabled: true, image: "demo", model: "test/model", dockerAvailable: true },
    models: { available: true, configured: true, defaultModel: "test/model" },
  }),
});
const connect = mock.source.connect;
mock.source.connect = (id, ready, event, error) => {
  const connection = connect(id, ready, event, error);
  queueMicrotask(ready);
  return connection;
};
createRoot(document.getElementById("root")!).render(<StarsApp source={mock.source} />);
