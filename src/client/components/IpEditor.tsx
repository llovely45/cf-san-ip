import { useState } from "react";

import type { IpRecord } from "../../shared/ip";

interface IpEditorProps {
  record?: IpRecord;
  onSave(record: IpRecord): Promise<void>;
  onCancel(): void;
}

export default function IpEditor({ record, onSave, onCancel }: IpEditorProps) {
  const [ip, setIp] = useState(record?.ip ?? "");
  const [port, setPort] = useState(record ? String(record.port) : "");
  const [asn, setAsn] = useState(record ? String(record.asn) : "");
  const [region, setRegion] = useState(record?.region ?? "");
  const [asnAlias, setAsnAlias] = useState(record?.asnAlias ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await onSave({
        ip: ip.trim(),
        port: Number(port),
        asn: Number(asn),
        region: region.trim(),
        asnAlias: asnAlias.trim() || null,
      });
    } catch (saveError) {
      setError(saveError instanceof Error ? saveError.message : "保存失败，请重试。");
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="editor-card" aria-label={record ? "编辑 IP 记录" : "添加 IP 记录"}>
      <div className="section-heading">
        <div>
          <p className="eyebrow">IP RECORD</p>
          <h2>{record ? "编辑记录" : "添加记录"}</h2>
        </div>
        <button className="button button-quiet" type="button" onClick={onCancel}>
          取消
        </button>
      </div>
      <form className="record-form" onSubmit={submit}>
        <label>
          IP 地址
          <input value={ip} onChange={(event) => setIp(event.target.value)} required />
        </label>
        <label>
          端口
          <input
            type="number"
            min="1"
            max="65535"
            value={port}
            onChange={(event) => setPort(event.target.value)}
            required
          />
        </label>
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
        <label>
          地区
          <input value={region} onChange={(event) => setRegion(event.target.value)} required />
        </label>
        <label>
          ASN 别名
          <input value={asnAlias} onChange={(event) => setAsnAlias(event.target.value)} />
        </label>
        <div className="form-actions">
          {error && <p className="form-error" role="alert">{error}</p>}
          <button className="button button-primary" type="submit" disabled={busy}>
            {busy ? "正在保存…" : "保存"}
          </button>
        </div>
      </form>
    </section>
  );
}
