import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "./api";
import type { Project } from "./types";
import type { ProjectRole } from "./permissions";
import type { Account } from "./AccessSettings";
import { ErrorBox, Field } from "./ui";
import AuditEvents from "./AuditEvents";
import "./access-settings.css";

type Member = { userId: string; email: string; displayName: string; role: ProjectRole };
type Policy = { gitlabTriggerMode: "off" | "owner" | "operator"; githubAutoIntake: boolean };
const roles: Record<ProjectRole, string> = { viewer: "观察者", operator: "操作员", owner: "负责人" };

export default function ProjectAccess({ project, platformAdmin }: { project: Project; platformAdmin: boolean }) {
  const [members, setMembers] = useState<Member[]>([]);
  const [users, setUsers] = useState<Account[]>([]);
  const [policy, setPolicy] = useState<Policy>({ gitlabTriggerMode: "off", githubAutoIntake: false });
  const [error, setError] = useState(""); const [notice, setNotice] = useState(""); const [busy, setBusy] = useState(false);
  const base = `/projects/${encodeURIComponent(project.id)}`;
  const load = useCallback(async () => {
    const [memberResult, userResult, triggerResult] = await Promise.all([
      api<{ members: Member[] }>(`${base}/members`),
      api<{ users: Account[] }>("/users"),
      api<Policy>(`${base}/trigger-policy`),
    ]);
    setMembers(memberResult.members || []); setUsers(userResult.users || []); setPolicy(triggerResult);
  }, [base]);
  useEffect(() => { void load().catch(cause => setError((cause as Error).message)); }, [load]);
  const run = async (action: () => Promise<unknown>, message: string) => {
    setBusy(true); setError(""); setNotice("");
    try { await action(); await load(); setNotice(message); }
    catch (cause) { setError((cause as Error).message); }
    finally { setBusy(false); }
  };
  return <div className="access-page"><div className="page-head"><div><a href="#/projects">← 项目列表</a><h1>{project.name} · 成员与触发</h1><p>直接成员和团队授权取较高角色。项目负责人可以管理项目协作。</p></div></div><ErrorBox error={error} retry={() => void load().catch(cause => setError((cause as Error).message))} />{notice && <p role="status" className="notice">{notice}</p>}
    <section className="panel panel-padding"><h2>直接成员</h2><div className="access-list">{members.map(member => <div className="access-row" key={member.userId}><div><strong>{member.displayName || member.email}</strong><small>{member.email} · {roles[member.role]}</small></div><div className="actions"><select aria-label={`${member.displayName || member.email} 的角色`} value={member.role} disabled={busy} onChange={event => void run(() => api(`${base}/members/${encodeURIComponent(member.userId)}`, "PUT", { role: event.target.value }), "成员角色已更新。")}><option value="viewer">观察者</option><option value="operator">操作员</option><option value="owner">负责人</option></select><button disabled={busy} onClick={() => void run(() => api(`${base}/members/${encodeURIComponent(member.userId)}`, "DELETE"), "直接授权已移除。")}>移除</button></div></div>)}</div><form className="access-inline" onSubmit={(event: FormEvent<HTMLFormElement>) => { event.preventDefault(); const data = new FormData(event.currentTarget); const userId = String(data.get("userId") || ""); if (userId) void run(() => api(`${base}/members/${encodeURIComponent(userId)}`, "PUT", { role: data.get("role") }), "成员已授权。"); }}><Field label="添加成员"><select name="userId" required defaultValue=""><option value="">选择成员</option>{users.filter(user => user.status === "active" && !members.some(member => member.userId === user.id)).map(user => <option key={user.id} value={user.id}>{user.displayName || user.email}</option>)}</select></Field><Field label="角色"><select name="role" defaultValue="viewer"><option value="viewer">观察者</option><option value="operator">操作员</option><option value="owner">负责人</option></select></Field><button className="primary" disabled={busy}>添加</button></form></section>
    <section className="panel panel-padding"><h2>自动接单</h2><p className="muted">外部事件中的操作者必须能被核验，并满足此项目的触发权限。</p><form className="access-inline" onSubmit={event => { event.preventDefault(); const data = new FormData(event.currentTarget); void run(() => api(`${base}/trigger-policy`, "PUT", { gitlabTriggerMode: data.get("gitlabTriggerMode"), githubAutoIntake: platformAdmin && data.get("githubAutoIntake") === "on" || !platformAdmin && policy.githubAutoIntake }), "自动接单策略已保存。"); }}><Field label="GitLab 标签与指令"><select name="gitlabTriggerMode" value={policy.gitlabTriggerMode} onChange={event => setPolicy(previous => ({ ...previous, gitlabTriggerMode: event.target.value as Policy["gitlabTriggerMode"] }))}><option value="off">关闭</option><option value="owner">仅负责人</option><option value="operator">负责人和操作员</option></select></Field>{project.provider === "github" && platformAdmin && <label className="field"><span>GitHub 旧式自动接单</span><input type="checkbox" name="githubAutoIntake" checked={policy.githubAutoIntake} onChange={event => setPolicy(previous => ({ ...previous, githubAutoIntake: event.target.checked }))} /><small>开启后按项目标签接单。</small></label>}{project.provider === "github" && !platformAdmin && <p>GitHub 自动接单由平台管理员设置，目前{policy.githubAutoIntake ? "已开启" : "已关闭"}。</p>}<button className="primary" disabled={busy}>保存策略</button></form></section>
    <AuditEvents projectId={project.id} />
  </div>;
}
