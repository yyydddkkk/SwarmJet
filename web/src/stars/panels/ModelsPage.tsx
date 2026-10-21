// 平台设置中的模型凭证与模型目录。
// It shows whether a provider credential exists, the Hive default model, the
// catalog from GET /api/models, and a form that writes a provider key through
// PUT /api/models/credentials/{provider}. The server only ever returns the
// masked value; the typed key stays in React state and is cleared on success.
import { useId, useState } from "react";
import { useDataAPI } from "../../data";
import type { ModelCredential, ModelOption } from "../../data";
import {
  BeeLoader,
  Callout,
  ConfirmDialog,
  ErrorNotice,
  Panel,
  StatusGrid,
  StatusItem,
} from "./primitives";
import { formatTokens } from "./format";
import { Icon } from "./icons";
import { useResource } from "./use-resource";

const providerLabels: Record<string, string> = {
  deepseek: "DeepSeek",
  openai: "OpenAI",
  anthropic: "Anthropic",
  google: "Google",
};

function providerLabel(provider: string): string {
  return providerLabels[provider] ?? provider;
}

export default function ModelsPage() {
  const api = useDataAPI();
  const models = useResource((signal) => api.listModels(signal), []);
  const [notice, setNotice] = useState("");
  const [removing, setRemoving] = useState<ModelCredential | null>(null);
  const [removeBusy, setRemoveBusy] = useState(false);
  const [removeError, setRemoveError] = useState("");

  const credentials = models.data?.providers ?? [];
  const configured = credentials.filter((credential) => credential.configured);
  const configuredProviders = new Set(
    configured.map((credential) => credential.provider),
  );
  const available = configured.length > 0;
  const gatewayAvailable = models.data?.gatewayAvailable ?? false;
  const defaultModel = models.data?.defaultModel ?? "—";
  const providers = uniqueProviders(models.data?.models ?? [], credentials);

  const remove = () => {
    if (!removing) return;
    setRemoveBusy(true);
    setRemoveError("");
    api
      .deleteModelCredential(removing.provider)
      .then(() => {
        setNotice(
          `已移除 ${providerLabel(removing.provider)} 的凭证，使用该提供方的模型暂时不可用。`,
        );
        setRemoving(null);
        models.reload();
      })
      .catch((cause) =>
        setRemoveError(cause instanceof Error ? cause.message : String(cause)),
      )
      .finally(() => setRemoveBusy(false));
  };

  return (
    <div className="pc-page">
      <header className="pc-page-head">
        <div>
          <h1>模型</h1>
          <p className="pc-page-sub">
            工作环境不保存模型凭证。凭证由平台的 凭证服务 保管并刷新，Agent
            通过平台代理调用模型。
          </p>
        </div>
        <div className="pc-page-actions">
          <button
            className="pc-btn"
            type="button"
            disabled={models.loading}
            onClick={models.reload}
          >
            <Icon name="refresh" size={16} />
            {models.loading ? "正在刷新…" : "刷新模型"}
          </button>
        </div>
      </header>

      {notice ? (
        <div className="pc-flash pc-flash-success" role="status">
          <Icon name="check" size={16} />
          <span>{notice}</span>
        </div>
      ) : null}

      {models.loading && !models.data ? (
        <BeeLoader label="正在读取模型与凭证…" />
      ) : models.error && !models.data ? (
        <ErrorNotice
          message={models.error}
          onRetry={models.reload}
          auth={models.errorStatus === 401}
          admin={models.errorStatus === 403}
        />
      ) : models.data ? (
        <>
          <Panel
            title="模型凭证"
            subtitle="写进平台的凭证只用于 模型代理；页面上只显示脱敏值。"
            extra={
              <span
                className={
                  available ? "pc-badge pc-tone-mint" : "pc-badge pc-tone-amber"
                }
              >
                <span
                  className={
                    available ? "pc-dot pc-tone-mint" : "pc-dot pc-tone-amber"
                  }
                  aria-hidden="true"
                />
                {available ? "已有可用凭证" : "还没有可用凭证"}
              </span>
            }
          >
            <StatusGrid>
              <StatusItem label="默认模型">
                <code>{defaultModel || "未配置"}</code>
              </StatusItem>
              <StatusItem label="可用提供方">
                {available
                  ? configured
                      .map((item) => providerLabel(item.provider))
                      .join("、")
                  : "无"}
              </StatusItem>
              <StatusItem label="模型网关">
                <span
                  className={
                    gatewayAvailable ? "pc-tone-mint" : "pc-tone-amber"
                  }
                  data-testid="gateway-available"
                >
                  {gatewayAvailable ? "可连接" : "暂不可用"}
                </span>
              </StatusItem>
              <StatusItem label="模型数量">
                {models.data.models.length} 个
              </StatusItem>
            </StatusGrid>

            {!available ? (
              <Callout tone="amber" title="先写入一个提供方凭证">
                <p>
                  第一个要用的是 DeepSeek API key。写入后，平台代理会按 Agent
                  记录用量和花费；工作环境 永远拿不到这个 key。
                </p>
              </Callout>
            ) : null}

            {!gatewayAvailable ? (
              <Callout tone="amber" title="模型网关暂时没有响应">
                <p>
                  平台读不到模型目录，下面的列表可能为空。凭证仍可以写入，网关恢复后刷新即可。
                </p>
              </Callout>
            ) : null}

            {credentials.length ? (
              <ul className="pc-credential-list">
                {credentials.map((credential) => (
                  <li key={credential.provider} className="pc-credential">
                    <div className="pc-credential-main">
                      <strong>{providerLabel(credential.provider)}</strong>
                      <code
                        className="pc-masked-key"
                        data-testid={`masked-${credential.provider}`}
                      >
                        {credential.masked || "已保存（无脱敏值）"}
                      </code>
                      <span className="pc-token-meta">
                        {credential.type === "oauth"
                          ? "OAuth"
                          : credential.type || "api_key"}{" "}
                        · {credential.configured ? "可用" : "未生效"}
                      </span>
                    </div>
                    <button
                      className="pc-btn pc-btn-danger pc-btn-small"
                      type="button"
                      aria-label={`移除 ${providerLabel(credential.provider)} 的凭证`}
                      onClick={() => {
                        setRemoveError("");
                        setRemoving(credential);
                      }}
                    >
                      移除
                    </button>
                  </li>
                ))}
              </ul>
            ) : null}

            <CredentialForm
              providers={providers}
              onSaved={(credential) => {
                setNotice(
                  `已保存 ${providerLabel(credential.provider)} 凭证，脱敏值 ${credential.masked || "已隐藏"}。`,
                );
                models.reload();
              }}
            />
          </Panel>

          <Panel
            title="模型列表"
            subtitle="平台提供的可用模型；没有单独指定模型的角色使用默认模型。"
          >
            {models.data.models.length === 0 ? (
              <p className="pc-muted">模型目录是空的；写入凭证后刷新一次。</p>
            ) : (
              <div className="pc-model-table-wrap">
                <table className="pc-model-table">
                  <thead>
                    <tr>
                      <th scope="col">模型</th>
                      <th scope="col">提供方</th>
                      <th scope="col">上下文</th>
                      <th scope="col">最大输出</th>
                      <th scope="col">状态</th>
                    </tr>
                  </thead>
                  <tbody>
                    {models.data.models.map((model) => (
                      <ModelRow
                        key={model.id}
                        model={model}
                        isDefault={model.id === models.data?.defaultModel}
                        usable={configuredProviders.has(model.provider)}
                      />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Panel>
        </>
      ) : null}

      {removing ? (
        <ConfirmDialog
          title={`移除 ${providerLabel(removing.provider)} 的凭证？`}
          confirmLabel="移除"
          danger
          busy={removeBusy}
          error={removeError}
          onConfirm={remove}
          onCancel={() => setRemoving(null)}
        >
          <p>
            使用这个提供方的 Agent 会立即无法调用模型。要恢复时需要重新输入 API
            key。
          </p>
        </ConfirmDialog>
      ) : null}
    </div>
  );
}

function ModelRow({
  model,
  isDefault,
  usable,
}: {
  model: ModelOption;
  isDefault: boolean;
  usable: boolean;
}) {
  return (
    <tr>
      <td>
        <code>{model.id}</code>
        {isDefault ? (
          <span className="pc-badge pc-tone-amber pc-model-default">默认</span>
        ) : null}
        {model.displayName ? (
          <span className="pc-model-name">{model.displayName}</span>
        ) : null}
        {model.kind ? <span className="pc-chip">{model.kind}</span> : null}
      </td>
      <td>{providerLabel(model.provider)}</td>
      <td>
        {model.contextLength
          ? `${formatTokens(model.contextLength)} tokens`
          : "—"}
      </td>
      <td>
        {model.maxOutputTokens
          ? `${formatTokens(model.maxOutputTokens)} tokens`
          : "—"}
      </td>
      <td>
        <span
          className={
            usable ? "pc-badge pc-tone-mint" : "pc-badge pc-tone-neutral"
          }
        >
          {usable ? "可用" : "缺少凭证"}
        </span>
      </td>
    </tr>
  );
}

function CredentialForm({
  providers,
  onSaved,
}: {
  providers: string[];
  onSaved: (credential: ModelCredential) => void;
}) {
  const api = useDataAPI();
  const providerId = useId();
  const keyId = useId();
  const [provider, setProvider] = useState(providers[0] ?? "deepseek");
  const [apiKey, setApiKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  return (
    <form
      className="pc-credential-form"
      autoComplete="off"
      onSubmit={(event) => {
        event.preventDefault();
        const key = apiKey.trim();
        if (!key) {
          setError("请输入 API key。");
          return;
        }
        setBusy(true);
        setError("");
        api
          .setModelCredential(provider, key)
          .then((result) => {
            // Drop the plaintext from component state as soon as the server confirms.
            setApiKey("");
            onSaved({
              provider: result?.provider || provider,
              type: result?.type,
              masked: result?.masked,
              configured: result?.configured ?? true,
            });
          })
          .catch((cause) =>
            setError(cause instanceof Error ? cause.message : String(cause)),
          )
          .finally(() => setBusy(false));
      }}
    >
      <div className="pc-form-grid">
        <label className="pc-field" htmlFor={providerId}>
          <span>提供方</span>
          <select
            id={providerId}
            value={provider}
            onChange={(event) => setProvider(event.target.value)}
          >
            {providers.map((item) => (
              <option key={item} value={item}>
                {providerLabel(item)}
              </option>
            ))}
          </select>
        </label>
        <label className="pc-field" htmlFor={keyId}>
          <span>API key</span>
          <input
            id={keyId}
            type="password"
            value={apiKey}
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => setApiKey(event.target.value)}
            placeholder="只在提交时发往平台，不写入 URL 或本地存储"
          />
        </label>
        <button className="pc-btn pc-btn-primary" type="submit" disabled={busy}>
          <Icon name="shield" size={16} />
          {busy ? "正在保存…" : "保存凭证"}
        </button>
      </div>
      <p className="pc-muted pc-credential-hint">
        提交后只显示服务器返回的脱敏值，明文不会出现在页面、地址栏或浏览器存储里。
      </p>
      {error ? (
        <p className="pc-form-error" role="alert">
          {error}
        </p>
      ) : null}
    </form>
  );
}

function uniqueProviders(
  models: ModelOption[],
  credentials: ModelCredential[],
): string[] {
  const seen = new Set<string>();
  const result: string[] = [];
  for (const provider of [
    ...credentials.map((credential) => credential.provider),
    ...models.map((model) => model.provider),
  ]) {
    if (!provider || seen.has(provider)) continue;
    seen.add(provider);
    result.push(provider);
  }
  if (!result.includes("deepseek")) result.unshift("deepseek");
  return result;
}
