// 简单分页：上一页 / 第 n / m 页 / 下一页。页码以服务端返回的 page 为准，超出末页时显示末页。
export function PageLinks(props: { page: number; total: number; pageSize: number; href: (n: number) => string }) {
  const last = Math.max(1, Math.ceil(props.total / Math.max(props.pageSize, 1)));
  if (last <= 1) return null;
  return (
    <nav className="pager" aria-label="分页">
      {props.page > 1 && (
        <a className="button" href={props.href(Math.min(props.page - 1, last))}>
          上一页
        </a>
      )}
      <span>
        第 {Math.min(props.page, last)} / {last} 页
      </span>
      {props.page < last && (
        <a className="button" href={props.href(props.page + 1)}>
          下一页
        </a>
      )}
    </nav>
  );
}
