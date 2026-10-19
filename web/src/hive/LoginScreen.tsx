// The only sign-in surface: one administrator account (round 3 removed
// registration, password resets and multi-user access).
import { useState, type FormEvent } from "react";
import { hiveAPI } from "./api";
import type { Principal } from "./types";
import { Icon } from "./icons";

export default function LoginScreen({
  onLogin,
  outerError = "",
}: {
  onLogin: (principal: Principal) => void;
  outerError?: string;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const fields = new FormData(event.currentTarget);
    setBusy(true);
    setError("");
    try {
      await hiveAPI.login(String(fields.get("username") || ""), String(fields.get("password") || ""));
      onLogin(await hiveAPI.getSession());
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="hg-app hg-login">
      <section className="hg-login-story" aria-label="SwarmJet 简介">
        <a className="hg-brand" href="#/hives">
          <span className="hg-brand-mark" aria-hidden="true">
            <Icon name="bee" size={32} />
          </span>
          <span className="hg-brand-name">SwarmJet</span>
          <span className="hg-brand-note">蜂巢控制台</span>
        </a>
        <div className="hg-login-copy">
          <span className="hg-eyebrow">HIVE / QUEEN / FORAGER</span>
          <h1>
            让蜂群干活，
            <br />
            让花园清晰。
          </h1>
          <p>
            一个 Hive 对应一个仓库。Queen 负责判断和派活，Forager 去执行，
            你在这里看进展、做审批、接本地 Agent。
          </p>
          <div className="hg-login-pixels" aria-hidden="true">
            <Icon name="hive" size={64} />
            <Icon name="bee" size={64} />
            <Icon name="crown" size={64} />
          </div>
        </div>
        <small>SWARMJET / HONEY GARDEN CONSOLE</small>
      </section>

      <section className="hg-login-form" aria-label="管理员登录">
        <form onSubmit={submit}>
          <span className="hg-eyebrow">管理员访问</span>
          <h2>登录蜂巢控制台</h2>
          <p className="hg-muted">只有一个管理员账号，由安装脚本生成或由环境变量指定。</p>
          {error || outerError ? (
            <p className="hg-form-error" role="alert">
              {error || outerError}
            </p>
          ) : null}
          <label className="hg-field">
            <span>用户名</span>
            <input name="username" autoComplete="username" required autoFocus />
          </label>
          <label className="hg-field">
            <span>密码</span>
            <input name="password" type="password" autoComplete="current-password" required />
          </label>
          <button className="hg-btn hg-btn-primary hg-btn-block" type="submit" disabled={busy}>
            {busy ? "正在登录…" : "登录"}
          </button>
          <p className="hg-login-hint">忘记密码时，在服务器上用安装脚本重新生成管理员密码。</p>
        </form>
      </section>
    </div>
  );
}
