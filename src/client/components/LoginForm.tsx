import { useState } from "react";

interface LoginFormProps {
  onLogin(password: string): Promise<void>;
}

export default function LoginForm({ onLogin }: LoginFormProps) {
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await onLogin(password);
      setPassword("");
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : "登录失败，请重试。");
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="auth-card" aria-labelledby="login-title">
      <p className="eyebrow">ADMIN ACCESS</p>
      <h1 id="login-title">管理员登录</h1>
      <p className="muted">登录后管理 IP 记录、ASN 别名和 API Token。</p>
      <form className="stack-form" onSubmit={submit}>
        <label>
          管理员密码
          <input
            autoComplete="current-password"
            type="password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            required
          />
        </label>
        {error && <p className="form-error" role="alert">{error}</p>}
        <button className="button button-primary" type="submit" disabled={busy}>
          {busy ? "正在登录…" : "登录"}
        </button>
      </form>
    </section>
  );
}
