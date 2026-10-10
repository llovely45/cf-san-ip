import { useState } from "react";

interface TokenSettingsProps {
  token: string | null;
  onSetToken(token: string): Promise<void>;
  onGenerateToken(): Promise<void>;
}

export default function TokenSettings({
  token,
  onSetToken,
  onGenerateToken,
}: TokenSettingsProps) {
  const [newToken, setNewToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  const setToken = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!newToken) {
      return;
    }
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await onSetToken(newToken);
      setNewToken("");
      setMessage("Token 已更新。");
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : "Token 更新失败。");
    } finally {
      setBusy(false);
    }
  };

  const generateToken = async () => {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await onGenerateToken();
      setMessage("新 Token 已生成，请安全保存。");
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : "Token 生成失败。");
    } finally {
      setBusy(false);
    }
  };

  const copyToken = async () => {
    if (!token) {
      return;
    }
    try {
      await navigator.clipboard.writeText(token);
      setMessage("Token 已复制。");
      setError("");
    } catch {
      setError("无法访问剪贴板，请手动复制明文 Token。");
    }
  };

  return (
    <section className="panel settings-panel token-panel" aria-labelledby="token-title">
      <div className="section-heading">
        <div>
          <p className="eyebrow">API CREDENTIAL</p>
          <h2 id="token-title">Token 设置</h2>
        </div>
      </div>
      <p className="muted">当前 Token 仅在已登录的管理界面中显示。</p>
      <div className="token-display-row">
        <div className="token-display" aria-label="当前 API Token">
          {token || "尚未设置 Token"}
        </div>
        <button
          className="button button-quiet"
          type="button"
          onClick={copyToken}
          disabled={!token}
        >
          复制 Token
        </button>
      </div>
      <form className="inline-form token-form" onSubmit={setToken}>
        <label className="grow-field">
          设置 API Token
          <input
            autoComplete="off"
            value={newToken}
            onChange={(event) => setNewToken(event.target.value)}
          />
        </label>
        <button className="button button-primary" type="submit" disabled={busy || !newToken}>
          保存 Token
        </button>
        <button
          className="button button-secondary"
          type="button"
          onClick={generateToken}
          disabled={busy}
        >
          生成 Token
        </button>
      </form>
      {error && <p className="form-error" role="alert">{error}</p>}
      {message && <p className="form-success" role="status">{message}</p>}
    </section>
  );
}
