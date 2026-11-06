import { useState, type FormEvent } from "react";
import * as api from "../api";
import { toError } from "../api";
import type { Session } from "../types";

export function LoginPage({ onAuthorized }: { onAuthorized: (session: Session) => void }) {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const session = await api.login(username, password);
      onAuthorized(session);
    } catch (err) {
      setError(toError(err).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-wrap">
      <form className="card login-card" onSubmit={(e) => void handleSubmit(e)}>
        <h1>家庭影音助手</h1>
        <p className="dim">请使用管理员账号登录</p>
        <label className="field">
          <span className="field-label">用户名</span>
          <input
            type="text"
            value={username}
            autoComplete="username"
            onChange={(e) => setUsername(e.target.value)}
            required
          />
        </label>
        <label className="field">
          <span className="field-label">密码</span>
          <input
            type="password"
            value={password}
            autoComplete="current-password"
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </label>
        {error !== null && <div className="inline-error" role="alert">{error}</div>}
        <button type="submit" className="btn btn-primary" disabled={busy}>
          {busy ? "登录中…" : "登录"}
        </button>
      </form>
    </div>
  );
}
