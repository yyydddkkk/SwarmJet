import { useState, type FormEvent } from "react";
import "./periphery.css";
import { APIError, useDataAPI, type Principal } from "../data";
export default function LoginSlot({
  onLogin,
  notice,
}: {
  onLogin(principal: Principal): void;
  notice?: string;
}) {
  const api = useDataAPI();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [exiting, setExiting] = useState(false);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const fields = new FormData(event.currentTarget);
    setBusy(true);
    setError("");
    try {
      await api.login(
        String(fields.get("username")),
        String(fields.get("password")),
      );
      const principal = await api.getSession();
      setExiting(true);
      window.setTimeout(() => onLogin(principal), 300);
    } catch (e) {
      setError(
        e instanceof APIError && e.status === 401
          ? "用户名或密码不正确，请重新输入。"
          : e instanceof APIError && e.status === 429
            ? "登录尝试过于频繁，请稍后重试。"
            : "无法连接登录服务，请检查网络后重试。",
      );
    } finally {
      setBusy(false);
    }
  }
  return (
    <main className={`stars-app stars-login-page${exiting ? " leaving" : ""}`}>
      <div className="stars-login-mark">SwarmJet</div>
      <form className="stars-login" onSubmit={submit}>
        <p className="stars-login-kicker">平台入口</p>
        <h1>登录 SwarmJet</h1>
        <p className="stars-login-lead">登录后查看项目与正在进行的工作。</p>
        {notice && <p role="status">{notice}</p>}
        <label>
          用户名
          <input name="username" autoComplete="username" required />
        </label>
        <label>
          密码
          <input
            name="password"
            type="password"
            autoComplete="current-password"
            required
          />
        </label>
        <button className="stars-login-submit" disabled={busy || exiting}>{busy || exiting ? "正在登录…" : "登录"}</button>
        {error && <p role="alert">{error}</p>}
      </form>
    </main>
  );
}
