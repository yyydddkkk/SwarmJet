import { useEffect, useId, useState } from "react";
import { api } from "./api";
import { Field } from "./App";
import "./registry-settings.css";

function PasswordField({ label, value, onChange }: { label: string; value: string; onChange: (value: string) => void }) {
  const id = useId();
  const [visible, setVisible] = useState(false);
  return (
    <div className="field registry-password-field">
      <label htmlFor={id}>{label}（留空保留）</label>
      <div className="registry-password-control">
        <input id={id} type={visible ? "text" : "password"} autoComplete="new-password" value={value} onChange={(e) => onChange(e.target.value)} />
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
  address: string;
  repository: string;
  pushUser: string;
  pullUser: string;
  hasPushPassword: boolean;
  hasPullPassword: boolean;
};
export default function RegistrySettings() {
  const [c, setC] = useState<Config>({
      address: "",
      repository: "swarmjet/agent",
      pushUser: "",
      pullUser: "",
      hasPushPassword: false,
      hasPullPassword: false,
    }),
    [push, setPush] = useState(""),
    [pull, setPull] = useState(""),
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
        <p>
          构建服务使用推送账号，节点使用只读拉取账号。请先通过 Compose
          部署仓库并配置 TLS 和权限。这里填写镜像仓库账号，不是平台管理员账号。
        </p>
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
        <button
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            try {
              setC(
                await api<Config>("/registry", "PUT", {
                  ...c,
                  ...(push ? { pushPassword: push } : {}),
                  ...(pull ? { pullPassword: pull } : {}),
                }),
              );
              setPush("");
              setPull("");
              setMessage("仓库配置已保存。");
            } catch (e) {
              setMessage((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          保存仓库配置
        </button>
        <p role="status">{message}</p>
      </div>
    </details>
  );
}
