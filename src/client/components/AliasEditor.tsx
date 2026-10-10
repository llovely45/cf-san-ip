import { useState } from "react";

interface AliasEditorProps {
  onSave(asn: number, alias: string | null): Promise<void>;
}

export default function AliasEditor({ onSave }: AliasEditorProps) {
  const [asn, setAsn] = useState("");
  const [alias, setAlias] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const parsedAsn = Number(asn);
    if (!Number.isSafeInteger(parsedAsn) || parsedAsn <= 0) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      await onSave(parsedAsn, alias.trim() || null);
      setAlias("");
    } catch (saveError) {
      setError(saveError instanceof Error ? saveError.message : "别名保存失败。");
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="panel settings-panel" aria-labelledby="alias-title">
      <div className="section-heading">
        <div>
          <p className="eyebrow">ASN LABELS</p>
          <h2 id="alias-title">ASN 别名</h2>
        </div>
      </div>
      <p className="muted">别名会应用到该 ASN 的所有 IP 记录；留空可清除。</p>
      <form className="inline-form" onSubmit={submit}>
        <label>
          ASN
          <input
            type="number"
            min="1"
            step="1"
            value={asn}
            onChange={(event) => setAsn(event.target.value)}
            required
          />
        </label>
        <label className="grow-field">
          别名
          <input value={alias} onChange={(event) => setAlias(event.target.value)} />
        </label>
        <button className="button button-primary" type="submit" disabled={busy}>
          {busy ? "正在保存…" : "保存别名"}
        </button>
      </form>
      {error && <p className="form-error" role="alert">{error}</p>}
    </section>
  );
}
