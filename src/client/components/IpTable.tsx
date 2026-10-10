import type { IpRecord } from "../../shared/ip";
import type { PageSize } from "../api";

interface IpTableProps {
  rows: IpRecord[];
  total: number;
  page: number;
  pageSize: PageSize;
  loading: boolean;
  onPageChange(page: number): void;
  onPageSizeChange(pageSize: PageSize): void;
  onEdit(record: IpRecord): void;
  onDelete(ip: string): void;
}

export default function IpTable({
  rows,
  total,
  page,
  pageSize,
  loading,
  onPageChange,
  onPageSizeChange,
  onEdit,
  onDelete,
}: IpTableProps) {
  const pageCount = Math.max(1, Math.ceil(total / pageSize));

  return (
    <section className="panel records-panel" aria-labelledby="records-title">
      <div className="section-heading records-heading">
        <div>
          <p className="eyebrow">INVENTORY</p>
          <h2 id="records-title">IP 记录</h2>
          <p className="muted">共 {total} 条记录 · 第 {page} / {pageCount} 页</p>
        </div>
        <label className="page-size-control">
          每页条数
          <select
            aria-label="每页条数"
            value={pageSize}
            onChange={(event) =>
              onPageSizeChange(Number(event.target.value) as PageSize)
            }
          >
            <option value="20">20</option>
            <option value="50">50</option>
            <option value="100">100</option>
          </select>
        </label>
      </div>
      <div className="table-scroll" aria-busy={loading}>
        <table aria-label="IP 记录">
          <thead>
            <tr>
              <th scope="col">IP 地址</th>
              <th scope="col">端口</th>
              <th scope="col">ASN</th>
              <th scope="col">地区</th>
              <th scope="col">ASN 别名</th>
              <th scope="col">操作</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((record) => (
              <tr key={record.ip}>
                <td className="ip-cell">{record.ip}</td>
                <td>{record.port}</td>
                <td>{record.asn}</td>
                <td>{record.region}</td>
                <td>{record.asnAlias || <span className="muted">—</span>}</td>
                <td>
                  <div className="row-actions">
                    <button
                      className="button button-small button-quiet"
                      type="button"
                      aria-label={`编辑 ${record.ip}`}
                      onClick={() => onEdit(record)}
                    >
                      编辑
                    </button>
                    <button
                      className="button button-small button-danger-quiet"
                      type="button"
                      aria-label={`删除 ${record.ip}`}
                      onClick={() => {
                        if (window.confirm(`确定删除 ${record.ip} 吗？`)) {
                          onDelete(record.ip);
                        }
                      }}
                    >
                      删除
                    </button>
                  </div>
                </td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr>
                <td className="empty-cell" colSpan={6}>
                  {loading ? "正在加载记录…" : "当前页没有记录"}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <div className="pagination" aria-label="记录分页">
        <button
          className="button button-quiet"
          type="button"
          onClick={() => onPageChange(page - 1)}
          disabled={loading || page <= 1}
        >
          上一页
        </button>
        <span>第 {page} 页</span>
        <button
          className="button button-quiet"
          type="button"
          onClick={() => onPageChange(page + 1)}
          disabled={loading || page >= pageCount}
        >
          下一页
        </button>
      </div>
    </section>
  );
}
