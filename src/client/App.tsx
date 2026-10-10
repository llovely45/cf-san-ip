import { useEffect, useRef, useState } from "react";

import type { IpRecord } from "../shared/ip";
import AliasEditor from "./components/AliasEditor";
import IpEditor from "./components/IpEditor";
import IpTable from "./components/IpTable";
import LoginForm from "./components/LoginForm";
import SetupForm from "./components/SetupForm";
import TokenSettings from "./components/TokenSettings";
import { clientApi } from "./api";
import type { ClientApi, PageSize } from "./api";

type AppMode = "loading" | "setup" | "login" | "admin" | "error";

interface AppProps {
  api?: ClientApi;
}

interface RecordEditorState {
  record?: IpRecord;
  originalIp?: string;
}

const isUnauthorized = (error: unknown): boolean =>
  typeof error === "object" &&
  error !== null &&
  "status" in error &&
  error.status === 401;

const errorMessage = (error: unknown): string =>
  error instanceof Error ? error.message : "请求失败，请重试。";

export default function App({ api = clientApi }: AppProps) {
  const [mode, setMode] = useState<AppMode>("loading");
  const [rows, setRows] = useState<IpRecord[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState<PageSize>(20);
  const [loading, setLoading] = useState(false);
  const [token, setToken] = useState<string | null>(null);
  const [editor, setEditor] = useState<RecordEditorState | null>(null);
  const [notice, setNotice] = useState("");
  const [globalError, setGlobalError] = useState("");
  const recordRequestSequence = useRef(0);

  const returnToLogin = (message = "登录状态已失效，请重新登录。") => {
    setRows([]);
    setTotal(0);
    setToken(null);
    setEditor(null);
    setGlobalError("");
    setNotice(message);
    setMode("login");
  };

  const handleProtectedError = (error: unknown) => {
    if (isUnauthorized(error)) {
      returnToLogin();
      return;
    }
    setGlobalError(errorMessage(error));
  };

  useEffect(() => {
    let active = true;

    const start = async () => {
      try {
        const status = await api.getSetupStatus();
        if (!active) {
          return;
        }
        if (!status.initialized) {
          setMode("setup");
          return;
        }

        try {
          const result = await api.listRecords(1, 20);
          const currentToken = await api.getToken();
          if (!active) {
            return;
          }
          setRows(result.rows);
          setTotal(result.total);
          setToken(currentToken.token);
          setMode("admin");
        } catch (error) {
          if (!active) {
            return;
          }
          if (isUnauthorized(error)) {
            setMode("login");
            return;
          }
          setGlobalError(errorMessage(error));
          setMode("error");
        }
      } catch (error) {
        if (active) {
          setGlobalError(errorMessage(error));
          setMode("error");
        }
      }
    };

    void start();
    return () => {
      active = false;
    };
  }, [api]);

  const loadRecords = async (
    nextPage: number,
    nextPageSize: PageSize,
  ): Promise<boolean> => {
    const requestSequence = ++recordRequestSequence.current;
    setLoading(true);
    setGlobalError("");
    try {
      const result = await api.listRecords(nextPage, nextPageSize);
      if (requestSequence !== recordRequestSequence.current) {
        return true;
      }
      setRows(result.rows);
      setTotal(result.total);
      setPage(nextPage);
      setPageSize(nextPageSize);
      return true;
    } catch (error) {
      if (requestSequence !== recordRequestSequence.current) {
        return false;
      }
      handleProtectedError(error);
      return false;
    } finally {
      if (requestSequence === recordRequestSequence.current) {
        setLoading(false);
      }
    }
  };

  const handleSetup = async (password: string) => {
    await api.setup(password);
    setNotice("管理员密码已设置，请登录。");
    setGlobalError("");
    setMode("login");
  };

  const handleLogin = async (password: string) => {
    await api.login(password);
    try {
      const result = await api.listRecords(1, pageSize);
      const currentToken = await api.getToken();
      setRows(result.rows);
      setTotal(result.total);
      setPage(1);
      setToken(currentToken.token);
      setNotice("");
      setGlobalError("");
      setMode("admin");
    } catch (error) {
      if (isUnauthorized(error)) {
        returnToLogin("登录状态验证失败，请重新登录。");
      }
      throw error;
    }
  };

  const handleLogout = async () => {
    try {
      await api.logout();
      setNotice("");
      setGlobalError("");
      setMode("login");
      setRows([]);
      setTotal(0);
      setToken(null);
      setEditor(null);
    } catch (error) {
      handleProtectedError(error);
    }
  };

  const handleSaveRecord = async (record: IpRecord) => {
    try {
      await api.saveRecord(record, editor?.originalIp);
      if (await loadRecords(page, pageSize)) {
        setEditor(null);
      }
    } catch (error) {
      if (isUnauthorized(error)) {
        handleProtectedError(error);
      }
      throw error;
    }
  };

  const handleDeleteRecord = async (ip: string) => {
    try {
      await api.deleteRecord(ip);
      const lastPage = Math.max(1, Math.ceil((total - 1) / pageSize));
      await loadRecords(Math.min(page, lastPage), pageSize);
    } catch (error) {
      handleProtectedError(error);
    }
  };

  const handleSetAlias = async (asn: number, alias: string | null) => {
    try {
      await api.setAsnAlias(asn, alias);
      await loadRecords(page, pageSize);
    } catch (error) {
      if (isUnauthorized(error)) {
        handleProtectedError(error);
      }
      throw error;
    }
  };

  const handleSetToken = async (newToken: string) => {
    try {
      await api.setToken(newToken);
      const currentToken = await api.getToken();
      setToken(currentToken.token);
    } catch (error) {
      if (isUnauthorized(error)) {
        handleProtectedError(error);
      }
      throw error;
    }
  };

  const handleGenerateToken = async () => {
    try {
      const generated = await api.generateToken();
      setToken(generated.token);
    } catch (error) {
      if (isUnauthorized(error)) {
        handleProtectedError(error);
      }
      throw error;
    }
  };

  return (
    <main className={`app-shell ${mode === "admin" ? "admin-shell" : "auth-shell"}`}>
      {mode === "loading" && (
        <section className="auth-card loading-card" role="status">
          <span className="loading-mark" aria-hidden="true" />
          <p>正在检查管理状态…</p>
        </section>
      )}
      {mode === "error" && (
        <section className="auth-card" aria-labelledby="startup-error-title">
          <p className="eyebrow">CONNECTION ISSUE</p>
          <h1 id="startup-error-title">无法连接管理服务</h1>
          <p className="form-error" role="alert">{globalError}</p>
          <button
            className="button button-primary"
            type="button"
            onClick={() => window.location.reload()}
          >
            重试
          </button>
        </section>
      )}
      {mode === "setup" && (
        <>
          {globalError && <p className="global-error" role="alert">{globalError}</p>}
          <SetupForm onSetup={handleSetup} />
        </>
      )}
      {mode === "login" && (
        <>
          {notice && <p className="global-notice" role="status">{notice}</p>}
          {globalError && <p className="global-error" role="alert">{globalError}</p>}
          <LoginForm onLogin={handleLogin} />
        </>
      )}
      {mode === "admin" && (
        <>
          <header className="app-header">
            <div>
              <p className="eyebrow">NETWORK CONTROL PANEL</p>
              <h1>IP 管理</h1>
              <p className="muted">管理 IP 记录、ASN 别名与访问 Token。</p>
            </div>
            <button className="button button-quiet" type="button" onClick={handleLogout}>
              退出登录
            </button>
          </header>
          {globalError && <p className="global-error" role="alert">{globalError}</p>}
          <div className="dashboard-grid">
            <div className="primary-column">
              {editor ? (
                <IpEditor
                  key={editor.originalIp ?? "new-record"}
                  record={editor.record}
                  onSave={handleSaveRecord}
                  onCancel={() => setEditor(null)}
                />
              ) : (
                <div className="toolbar">
                  <p className="muted">记录按 IP 地址排序，并由服务器分页。</p>
                  <button
                    className="button button-primary"
                    type="button"
                    onClick={() => setEditor({})}
                  >
                    添加记录
                  </button>
                </div>
              )}
              <IpTable
                rows={rows}
                total={total}
                page={page}
                pageSize={pageSize}
                loading={loading}
                onPageChange={(nextPage) => {
                  if (nextPage >= 1) {
                    void loadRecords(nextPage, pageSize);
                  }
                }}
                onPageSizeChange={(nextPageSize) => {
                  void loadRecords(1, nextPageSize);
                }}
                onEdit={(record) =>
                  setEditor({ record, originalIp: record.ip })
                }
                onDelete={(ip) => void handleDeleteRecord(ip)}
              />
            </div>
            <aside className="settings-column" aria-label="管理设置">
              <AliasEditor onSave={handleSetAlias} />
              <TokenSettings
                token={token}
                onSetToken={handleSetToken}
                onGenerateToken={handleGenerateToken}
              />
            </aside>
          </div>
        </>
      )}
    </main>
  );
}
