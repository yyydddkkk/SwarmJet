import { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import { ErrorBox, Field } from "./ui";
import "./workbench-pages.css";

type Identity = { gitlabUrl: string; username: string; gitlabUserId?: number; configured: boolean };

export default function GitLabIdentitySettings() {
  const [identities, setIdentities] = useState<Identity[]>([]);
  const [tokens, setTokens] = useState<Record<string, string>>({});
  const [error, setError] = useState(""); const [notice, setNotice] = useState(""); const [busy, setBusy] = useState("");
  const load = useCallback(async () => { try { const result = await api<{ identities: Identity[] }>("/gitlab-identities"); setIdentities(result.identities || []); setError(""); } catch (cause) { setError((cause as Error).message); } }, []);
  useEffect(() => { void load(); }, [load]);
  const run = async (url: string, action: () => Promise<unknown>, message: string) => { setBusy(url); setError(""); setNotice(""); try { await action(); setTokens(previous => ({ ...previous, [url]: "" })); await load(); setNotice(message); } catch (cause) { setError((cause as Error).message); } finally { setBusy(""); } };
  return <section className="panel workbench-pad" aria-label="个人 GitLab 身份"><h2>个人 GitLab 身份</h2><p className="muted">为每个已接入的 GitLab 实例配置你自己的 Token。保存时会验证并绑定 GitLab 用户 ID，Issue 和 MR 工作台以该身份操作。</p><ErrorBox error={error} retry={() => void load()} />{notice && <p className="notice" role="status">{notice}</p>}{identities.length ? identities.map(identity => <form key={identity.gitlabUrl} className="workbench-identity" onSubmit={event => { event.preventDefault(); void run(identity.gitlabUrl, () => api("/gitlab-identities", "PUT", { gitlabUrl: identity.gitlabUrl, token: tokens[identity.gitlabUrl] }), "个人身份已验证并保存。"); }}><div><strong>{identity.gitlabUrl}</strong><small>{identity.configured ? `已绑定：${identity.username}${identity.gitlabUserId ? ` (#${identity.gitlabUserId})` : ""}` : "尚未配置"}</small></div><Field label={identity.configured ? "更换个人 Token" : "个人 Token"}><input type="password" required autoComplete="off" value={tokens[identity.gitlabUrl] || ""} onChange={event => setTokens(previous => ({ ...previous, [identity.gitlabUrl]: event.target.value }))} /></Field><div className="actions"><button className="primary" disabled={busy === identity.gitlabUrl || !tokens[identity.gitlabUrl]}>{busy === identity.gitlabUrl ? "验证中…" : "验证并保存"}</button>{identity.configured && <button type="button" disabled={busy === identity.gitlabUrl} onClick={() => void run(identity.gitlabUrl, () => api(`/gitlab-identities?gitlabUrl=${encodeURIComponent(identity.gitlabUrl)}`, "DELETE"), "个人身份已移除。")}>移除</button>}</div></form>) : <p>当前没有已接入的 GitLab 实例。请先在项目配置中添加 GitLab 项目。</p>}</section>;
}
