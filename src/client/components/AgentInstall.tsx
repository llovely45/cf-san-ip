import { useEffect, useState } from "react";

import type { ClientApi } from "../api";

interface AgentInstallProps {
  api: ClientApi;
  token: string | null;
}

export default function AgentInstall({ api, token }: AgentInstallProps) {
  const [command, setCommand] = useState("");
  const [warning, setWarning] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);

  const loadCommand = async () => {
    if (!token) {
      setCommand("");
      setWarning("");
      setError("");
      return;
    }

    setBusy(true);
    setError("");
    setCopied(false);
    try {
      const result = await api.getAgentInstallCommand();
      setCommand(result.command);
      setWarning(result.warning);
    } catch (loadError) {
      const message = loadError instanceof Error ? loadError.message : "加载安装命令失败。";
      setCommand("");
      setError(
        message === "release_repository_not_configured"
          ? "Worker 尚未配置 CF_SAN_RELEASE_REPOSITORY=owner/repository。配置并重新部署后即可生成命令。"
          : message === "api_token_not_configured"
            ? "请先设置 API Token，再生成安装命令。"
            : message,
      );
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    void loadCommand();
    // Token rotation must refresh the command. The API client is stable for the app lifetime.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [api, token]);

  const copyCommand = async () => {
    if (!command) {
      return;
    }
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      setError("");
    } catch {
      setError("无法访问剪贴板；请手动复制上方命令。");
    }
  };

  return (
    <section className="panel settings-panel agent-install-panel" aria-labelledby="agent-install-title">
      <div className="section-heading">
        <div>
          <p className="eyebrow">SCANNER AGENT</p>
          <h2 id="agent-install-title">安装 cf-san Agent</h2>
        </div>
        <button
          className="button button-quiet button-small"
          type="button"
          onClick={() => void loadCommand()}
          disabled={busy}
        >
          刷新
        </button>
      </div>
      {!token ? (
        <p className="muted">请先在 Token 设置中配置 API Token。</p>
      ) : busy && !command ? (
        <p className="muted" role="status">正在生成安装命令…</p>
      ) : command ? (
        <>
          <p className="muted">在目标 Debian/Alpine Linux 服务器上执行：</p>
          <pre className="install-command"><code>{command}</code></pre>
          <button className="button button-primary" type="button" onClick={() => void copyCommand()}>
            复制安装命令
          </button>
          {warning && <p className="install-warning">{warning}</p>}
        </>
      ) : null}
      {error && <p className="form-error" role="alert">{error}</p>}
      {copied && <p className="form-success" role="status">安装命令已复制。</p>}
    </section>
  );
}
