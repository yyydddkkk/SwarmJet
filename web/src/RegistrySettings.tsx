import { useEffect, useId, useState } from "react";
import { api } from "./api";
import { Field } from "./ui";
import "./registry-settings.css";

function PasswordField({ label, value, onChange, readOnly = false, initialVisible = false }: { label: string; value: string; onChange: (value: string) => void; readOnly?: boolean; initialVisible?: boolean }) {
  const id = useId();
  const [visible, setVisible] = useState(initialVisible);
  return (
    <div className="field registry-password-field">
      <label htmlFor={id}>{label}{readOnly ? "" : "（留空保留）"}</label>
      <div className="registry-password-control">
        <input id={id} type={visible ? "text" : "password"} autoComplete="new-password" readOnly={readOnly} value={value} onChange={(e) => onChange(e.target.value)} />
        <button type="button" className="registry-password-toggle" aria-label={`${visible ? "隐藏" : "显示"}${label}`} aria-pressed={visible} onClick={() => setVisible(!visible)}>
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6S2 12 2 12Z" />
            <circle cx="12" cy="12" r="3" />
            {visible && <path d="M3 3 21 21" />}
          </svg>
        </button>
      </div>
    </div>
  );
}
type Config = {
  mode?: "builtin" | "custom";
  builtinAvailable?: boolean;
  builtinAddress?: string;
  address: string;
  repository: string;
  pushUser: string;
  pullUser: string;
  hasPushPassword: boolean;
  hasPullPassword: boolean;
};
export default function RegistrySettings() {
  const [c, setC] = useState<Config>({
      mode: "custom",
      address: "",
      repository: "swarmjet/agent",
      pushUser: "",
      pullUser: "",
      hasPushPassword: false,
      hasPullPassword: false,
    }),
    [push, setPush] = useState(""),
    [pull, setPull] = useState(""),
    [revealed, setRevealed] = useState(false),
    [message, setMessage] = useState(""),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    api<Config>("/registry")
      .then((v) => setC({ ...v, repository: v.repository || "swarmjet/agent" }))
      .catch((e) => setMessage(e.message));
  }, []);
  return (
    <details className="panel">
      <summary>Distribution 镜像仓库</summary>
      <div className="panel-body">
        <Field label="仓库来源">
          <select value={c.mode || "custom"} onChange={(e) => { setC({ ...c, mode: e.target.value as Config["mode"], address: e.target.value === "custom" && c.mode === "builtin" ? "" : c.address }); setPush(""); setPull(""); setRevealed(false); }}>
            {c.builtinAvailable && <option value="builtin">内置仓库（单机）</option>}
            <option value="custom">自定义仓库地址</option>
          </select>
        </Field>
        {c.mode === "builtin" ? (
          <>
          <p>内置仓库随 Compose 启动，地址为 <code>{c.builtinAddress || c.address}</code>；同一局域网的节点可通过此地址拉取镜像。首次安装会生成独立的推送和拉取密码。</p>
            <div className="image-fields">
              <Field label="推送用户名"><input readOnly value={c.pushUser} /></Field>
              <Field label="拉取用户名"><input readOnly value={c.pullUser} /></Field>
              {revealed && <>
                <PasswordField label="推送密码" value={push} onChange={() => {}} readOnly initialVisible />
                <PasswordField label="拉取密码" value={pull} onChange={() => {}} readOnly initialVisible />
              </>}
            </div>
          </>
        ) : <>
          <p>自定义仓库需要先部署并配置 TLS。推送和只读拉取账号属于镜像仓库，不是平台管理员账号。</p>
          <div className="image-fields">
          {(["address", "repository", "pushUser", "pullUser"] as const).map(
            (k) => (
              <Field
                key={k}
                label={
                  {
                    address: "仓库地址（host:port）",
                    repository: "镜像路径",
                    pushUser: "推送用户名",
                    pullUser: "拉取用户名",
                  }[k]
                }
              >
                <input
                  value={c[k] || ""}
                  onChange={(e) => setC({ ...c, [k]: e.target.value })}
                />
              </Field>
            ),
          )}
          <PasswordField label="推送密码" value={push} onChange={setPush} />
          <PasswordField label="拉取密码" value={pull} onChange={setPull} />
          </div>
        </>}
        {(c.hasPushPassword || c.hasPullPassword) && <button type="button" disabled={busy} onClick={async () => {
          setBusy(true);
          try {
            const secrets = await api<{pushPassword: string; pullPassword: string}>("/registry/reveal", "POST");
            setPush(secrets.pushPassword);
            setPull(secrets.pullPassword);
            setRevealed(true);
          } catch (e) { setMessage((e as Error).message); }
          finally { setBusy(false); }
        }}>查看已保存密码</button>}
        <button
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            try {
              setC(
                await api<Config>("/registry", "PUT", {
                  ...c,
                  ...(c.mode === "custom" && push ? { pushPassword: push } : {}),
                  ...(c.mode === "custom" && pull ? { pullPassword: pull } : {}),
                }),
              );
              setPush("");
              setPull("");
              setRevealed(false);
              setMessage("仓库配置已保存。");
            } catch (e) {
              setMessage((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          保存仓库选择
        </button>
        <p role="status">{message}</p>
      </div>
    </details>
  );
}
