import { useEffect, useState } from "react";
import { api } from "./api";
import { Field } from "./App";
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
          部署仓库并配置 TLS 和权限。
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
          <Field label="推送密码（留空保留）">
            <input
              type="password"
              value={push}
              onChange={(e) => setPush(e.target.value)}
            />
          </Field>
          <Field label="拉取密码（留空保留）">
            <input
              type="password"
              value={pull}
              onChange={(e) => setPull(e.target.value)}
            />
          </Field>
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
