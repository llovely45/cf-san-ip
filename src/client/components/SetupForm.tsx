import { useState } from "react";

interface SetupFormProps {
  onSetup(password: string): Promise<void>;
}

export default function SetupForm({ onSetup }: SetupFormProps) {
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (password.length === 0) {
      setError("请输入管理员密码。");
      return;
    }
    if (password !== confirmation) {
      setError("两次输入的密码不一致。");
      return;
    }

    setBusy(true);
    setError("");
    try {
      await onSetup(password);
      setPassword("");
      setConfirmation("");
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : "初始化失败，请重试。");
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="auth-card" aria-labelledby="setup-title">
      <p className="eyebrow">FIRST-RUN SETUP</p>
      <h1 id="setup-title">初始化管理员密码</h1>
      <p className="muted">首次使用请设置密码，完成后使用该密码登录。</p>
      <form className="stack-form" onSubmit={submit}>
        <label>
          管理员密码
          <input
            autoComplete="new-password"
            type="password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            required
          />
        </label>
        <label>
          确认管理员密码
          <input
            autoComplete="new-password"
            type="password"
            value={confirmation}
            onChange={(event) => setConfirmation(event.target.value)}
            required
          />
        </label>
        {error && <p className="form-error" role="alert">{error}</p>}
        <button className="button button-primary" type="submit" disabled={busy}>
          {busy ? "正在初始化…" : "初始化管理员密码"}
        </button>
      </form>
    </section>
  );
}
