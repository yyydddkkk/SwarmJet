import { useState, type FormEvent } from "react";
import { api } from "./api";
import type { Principal } from "./permissions";
import { Icon } from "./ConsoleIcon";
import { ErrorBox, Field } from "./ui";

type Mode = "login" | "register" | "verify" | "forgot" | "reset" | "pending";

function routeMode(): Mode {
  const path = location.hash.slice(1).split("?")[0];
  if (path === "/register") return "register";
  if (path === "/verify") return "verify";
  if (path === "/forgot-password") return "forgot";
  if (path === "/reset-password") return "reset";
  if (path === "/pending") return "pending";
  return "login";
}

function tokenFromLocation() {
  return new URLSearchParams(location.hash.split("?")[1] || location.search.slice(1)).get("token") || "";
}

export default function AuthScreen({ onLogin, outerError }: { onLogin: (principal: Principal) => void; outerError: string }) {
  const [mode, setMode] = useState<Mode>(routeMode);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [token, setToken] = useState(tokenFromLocation);
  const go = (next: Mode) => { setMode(next); setError(""); setNotice(""); location.hash = next === "login" ? "/overview" : `/${next === "forgot" ? "forgot-password" : next === "reset" ? "reset-password" : next}`; };
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault(); setBusy(true); setError(""); setNotice("");
    const fields = new FormData(event.currentTarget);
    try {
      if (mode === "login") {
        await api("/login", "POST", { username: fields.get("username"), password: fields.get("password") });
        onLogin(await api<Principal>("/session"));
      } else if (mode === "register") {
        await api("/register", "POST", { email: fields.get("email") });
        setMode("verify"); setNotice("验证邮件已发送。请打开链接设置姓名和密码，然后等待平台管理员审批。");
      } else if (mode === "verify") {
        await api("/register/verify", "POST", { token: token || fields.get("token"), displayName: fields.get("displayName"), password: fields.get("password") });
        setMode("pending"); setNotice("邮箱已验证，等待平台管理员审批。");
      } else if (mode === "forgot") {
        await api("/password-resets", "POST", { email: fields.get("email") });
        setNotice("若该邮箱已注册，密码重置邮件会发送到邮箱。");
      } else if (mode === "reset") {
        await api("/password-resets/confirm", "POST", { token: token || fields.get("token"), password: fields.get("password") });
        setMode("login"); setNotice("密码已更新，请登录。");
      }
    } catch (cause) { setError((cause as Error).message); }
    finally { setBusy(false); }
  };
  return <div className="login">
    <div className="login-story"><a className="brand" href="#/overview"><span className="brand-mark"><Icon name="bolt" /></span>SwarmJet</a><div><span className="eyebrow">YOUR ENGINEERING WORKSPACE</span><h1>让工作前进。<br />让过程清晰。</h1><p>连接需求、开发、审查与交付，<br />在一个工作空间里掌握全程。</p><div className="login-flow"><span>需求</span><i /><span>开发与验证</span><i /><span>审查与交付</span></div></div><small>SWARMJET / WORKSPACE</small></div>
    <div className="login-form"><form onSubmit={submit}>
      <span className="eyebrow">成员访问</span>
      <h1>{({ login: "登录工作空间", register: "申请加入工作空间", verify: "验证邮箱", forgot: "找回密码", reset: "设置新密码", pending: "等待审批" } as Record<Mode, string>)[mode]}</h1>
      <p className="muted">{mode === "pending" ? "账号验证后，由平台管理员审批访问权限。" : "单组织私有工作空间"}</p>
      <ErrorBox error={error || outerError} />{notice && <p className="notice" role="status">{notice}</p>}
      {mode === "login" && <><Field label="邮箱或应急管理员用户名"><input name="username" autoComplete="username" required autoFocus /></Field><Field label="密码"><input name="password" type="password" autoComplete="current-password" required /></Field><button className="primary full" disabled={busy}>{busy ? "正在登录…" : "登录 →"}</button><div className="auth-links"><button type="button" onClick={() => go("register")}>申请账号</button><button type="button" onClick={() => go("forgot")}>忘记密码</button></div></>}
      {mode === "register" && <><Field label="邮箱"><input name="email" type="email" autoComplete="email" required /></Field><button className="primary full" disabled={busy}>{busy ? "正在提交…" : "发送验证邮件"}</button></>}
      {mode === "verify" && <>{!token && <Field label="邮件中的验证令牌"><input name="token" required onChange={event => setToken(event.target.value)} /></Field>}<Field label="姓名"><input name="displayName" autoComplete="name" required /></Field><Field label="设置密码"><input name="password" type="password" minLength={12} autoComplete="new-password" required /></Field><button className="primary full" disabled={busy || !token}>{busy ? "正在验证…" : "验证并提交申请"}</button></>}
      {mode === "forgot" && <><Field label="注册邮箱"><input name="email" type="email" autoComplete="email" required /></Field><button className="primary full" disabled={busy}>{busy ? "正在提交…" : "发送重置邮件"}</button></>}
      {mode === "reset" && <>{!token && <Field label="邮件中的重置令牌"><input name="token" required onChange={event => setToken(event.target.value)} /></Field>}<Field label="新密码"><input name="password" type="password" minLength={12} autoComplete="new-password" required /></Field><button className="primary full" disabled={busy || !token}>{busy ? "正在重置…" : "更新密码"}</button></>}
      {mode !== "login" && <button className="auth-back" type="button" onClick={() => go("login")}>返回登录</button>}
    </form></div>
  </div>;
}
