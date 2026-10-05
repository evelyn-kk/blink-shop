import type { ReactNode } from 'react';
import { Empty } from './StateView';

export interface Column<T> {
  key: string;
  header: string;
  render: (row: T) => ReactNode;
  /** 数字、金额右对齐。 */
  align?: 'start' | 'end';
}

// 通用表格：表头用 <th scope="col">，窄屏下每行变成一张卡片，单元格前显示列名（data-label）。
// 分页、加载和错误由页面负责（配合 PageLinks / StateView）。
export function DataTable<T>(props: { caption: string; columns: Column<T>[]; rows: T[]; rowKey: (row: T) => string; empty: string }) {
  if (props.rows.length === 0) return <Empty text={props.empty} />;
  return (
    <div className="table-wrap">
      <table className="data-table">
        <caption className="visually-hidden">{props.caption}</caption>
        <thead>
          <tr>
            {props.columns.map((c) => (
              <th key={c.key} scope="col" className={c.align === 'end' ? 'num' : undefined}>
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {props.rows.map((row) => (
            <tr key={props.rowKey(row)}>
              {props.columns.map((c) => (
                <td key={c.key} data-label={c.header} className={c.align === 'end' ? 'num' : undefined}>
                  {c.render(row)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
