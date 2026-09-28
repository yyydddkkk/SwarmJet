import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "./api";
import type { Project } from "./types";
import type { ProjectRole } from "./permissions";
import { ErrorBox, Field } from "./ui";
import AuditEvents from "./AuditEvents";
import "./access-settings.css";

export type Account = { id: string; email: string; displayName: string; username?: string; status: string; platformAdmin?: boolean };
type Team = { id: string; name: string; members: { userId: string; email: string; displayName: string }[]; projects: { projectId: string; role: ProjectRole }[] };
const roleNames: Record<ProjectRole, string> = { viewer: "观察者", operator: "操作员", owner: "负责人" };

export default function AccessSettings({ projects, currentUserId }: { projects: Project[]; currentUserId: string }) {
  const [section, setSection] = useState<"users" | "teams" | "audit">("users");
  const [users, setUsers] = useState<Account[]>([]);
  const [teams, setTeams] = useState<Team[]>([]);
  const [error, setError] = useState(""); const [notice, setNotice] = useState(""); const [busy, setBusy] = useState("");
  const load = useCallback(async () => {
    const [userResult, teamResult] = await Promise.all([api<{ users: Account[] }>("/users"), api<{ teams: Team[] }>("/teams")]);
    setUsers(userResult.users || []); setTeams(teamResult.teams || []);
  }, []);
  useEffect(() => { void load().catch(cause => setError((cause as Error).message)); }, [load]);
  const run = async (key: string, action: () => Promise<unknown>, message: string) => {
    setBusy(key); setError(""); setNotice("");
    try { await action(); await load(); setNotice(message); }
    catch (cause) { setError((cause as Error).message); }
    finally { setBusy(""); }
  };
  const availableUsers = users.filter(user => user.status === "active");
  return <div className="access-page">
    <div className="page-head"><div><span className="eyebrow">ACCESS</span><h1>成员与团队</h1><p>审批成员账号，并将团队授权给项目。</p></div></div>
    <nav className="system-tabs" aria-label="成员与团队分区"><button aria-current={section === "users" ? "page" : undefined} onClick={() => setSection("users")}>账号</button><button aria-current={section === "teams" ? "page" : undefined} onClick={() => setSection("teams")}>团队</button><button aria-current={section === "audit" ? "page" : undefined} onClick={() => setSection("audit")}>审计</button></nav>
    <ErrorBox error={error} retry={() => void load().catch(cause => setError((cause as Error).message))} />{notice && <p role="status" className="notice">{notice}</p>}
    {section === "users" && <section className="panel panel-padding"><h2>账号审批</h2><p className="muted">成员完成邮箱验证后才可审批；停用账号会撤销其会话。</p><div className="access-list">{users.map(user => <div className="access-row" key={user.id}><div><strong>{user.displayName || user.username || user.email}</strong><small>{user.email} · {({ pending_approval: "待审批", active: "已启用", disabled: "已停用", rejected: "已拒绝", pending_email: "待邮箱验证" } as Record<string, string>)[user.status] || user.status}{user.platformAdmin ? " · 平台管理员" : ""}</small></div><div className="actions">{user.status === "pending_approval" && <><button className="primary" disabled={!!busy} onClick={() => void run(user.id, () => api(`/users/${encodeURIComponent(user.id)}/approve`, "POST"), "账号已批准。")}>批准</button><button disabled={!!busy} onClick={() => void run(user.id, () => api(`/users/${encodeURIComponent(user.id)}/reject`, "POST"), "申请已拒绝。")}>拒绝</button></>}{user.status === "active" && user.id !== currentUserId && <><button disabled={!!busy} onClick={() => void run(user.id, () => api(`/users/${encodeURIComponent(user.id)}/platform-admin`, "POST", { enabled: !user.platformAdmin }), user.platformAdmin ? "已移除平台管理员权限。" : "已授予平台管理员权限。")}>{user.platformAdmin ? "移除管理员" : "设为管理员"}</button><button disabled={!!busy} onClick={() => void run(user.id, () => api(`/users/${encodeURIComponent(user.id)}/disable`, "POST"), "账号已停用。")}>停用</button></>}</div></div>)}{!users.length && <p>暂无成员账号。</p>}</div></section>}
    {section === "audit" && <AuditEvents />}
    {section === "teams" && <><section className="panel panel-padding"><h2>创建团队</h2><form className="access-inline" onSubmit={(event: FormEvent<HTMLFormElement>) => { event.preventDefault(); const form = event.currentTarget; const name = String(new FormData(form).get("name") || "").trim(); if (name) void run("new", () => api("/teams", "POST", { name }), "团队已创建。").then(() => form.reset()); }}><Field label="团队名称"><input name="name" required maxLength={100} /></Field><button className="primary" disabled={!!busy}>创建</button></form></section>{teams.map(team => <section className="panel panel-padding access-team" key={team.id}><h2>{team.name}</h2><div className="access-columns"><div><h3>团队成员</h3><ul>{team.members?.map(member => <li key={member.userId}><span>{member.displayName || member.email}</span><button disabled={!!busy} onClick={() => void run(`member-${member.userId}`, () => api(`/teams/${encodeURIComponent(team.id)}/members/${encodeURIComponent(member.userId)}`, "DELETE"), "成员已移出团队。")}>移除</button></li>)}</ul><form className="access-inline" onSubmit={event => { event.preventDefault(); const userId = String(new FormData(event.currentTarget).get("userId") || ""); if (userId) void run(`member-${userId}`, () => api(`/teams/${encodeURIComponent(team.id)}/members`, "PUT", { userId }), "成员已加入团队。"); }}><Field label="添加成员"><select name="userId" required defaultValue=""><option value="">选择成员</option>{availableUsers.filter(user => !team.members?.some(member => member.userId === user.id)).map(user => <option key={user.id} value={user.id}>{user.displayName || user.email}</option>)}</select></Field><button disabled={!!busy}>添加</button></form></div><div><h3>项目授权</h3><ul>{team.projects?.map(grant => <li key={grant.projectId}><span>{projects.find(project => project.id === grant.projectId)?.name || grant.projectId} · {roleNames[grant.role]}</span><button disabled={!!busy} onClick={() => void run(`grant-${grant.projectId}`, () => api(`/teams/${encodeURIComponent(team.id)}/projects/${encodeURIComponent(grant.projectId)}`, "DELETE"), "项目授权已移除。")}>移除</button></li>)}</ul><form className="access-inline" onSubmit={event => { event.preventDefault(); const data = new FormData(event.currentTarget); const projectId = String(data.get("projectId") || ""); const role = String(data.get("role") || "viewer"); if (projectId) void run(`grant-${projectId}`, () => api(`/teams/${encodeURIComponent(team.id)}/projects/${encodeURIComponent(projectId)}`, "PUT", { role }), "项目授权已保存。"); }}><Field label="项目"><select name="projectId" required defaultValue=""><option value="">选择项目</option>{projects.map(project => <option key={project.id} value={project.id}>{project.name}</option>)}</select></Field><Field label="角色"><select name="role" defaultValue="viewer"><option value="viewer">观察者</option><option value="operator">操作员</option><option value="owner">负责人</option></select></Field><button disabled={!!busy}>授权</button></form></div></div></section>)}{!teams.length && <p>尚无团队。</p>}</>}
  </div>;
}
