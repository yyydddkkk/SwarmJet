import { useState, type FormEvent } from "react";
import "./periphery.css";
import { APIError, useDataAPI, type Principal } from "../data";
// Task D owns the login surface. Authentication uses the injectable shared API.
export default function LoginSlot({
  onLogin,
}: {
  onLogin(principal: Principal): void;
}) {
  const api = useDataAPI();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
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
      onLogin(await api.getSession());
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
    <main className="stars-app">
      <form className="stars-login" onSubmit={submit}>
        <h1>登录 SwarmJet</h1>
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
        <button disabled={busy}>{busy ? "正在登录…" : "登录"}</button>
        {error && <p role="alert">{error}</p>}
      </form>
    </main>
  );
}
