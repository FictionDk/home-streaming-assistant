import { SOURCE_LABELS, SOURCE_STATUS_LABELS } from "../format";
import type { Source, SourceInfo } from "../types";

export function SourceChips({
  sources,
  current,
  onSelect,
}: {
  sources: SourceInfo[];
  current: Source;
  onSelect: (source: Source) => void;
}) {
  return (
    <div className="source-chips">
      {(Object.keys(SOURCE_LABELS) as Source[]).map((id) => {
        const info = sources.find((s) => s.id === id);
        const status = info?.status ?? null;
        return (
          <button
            key={id}
            type="button"
            className={`source-chip${id === current ? " active" : ""}${status === "ready" ? " ready" : ""}`}
            onClick={() => onSelect(id)}
          >
            <span className="source-name">{SOURCE_LABELS[id]}</span>
            <span className={`chip status-${status ?? "unknown"}`}>
              {status === null ? "检测中" : SOURCE_STATUS_LABELS[status]}
            </span>
          </button>
        );
      })}
    </div>
  );
}

export function Breadcrumb({ path, onNavigate }: { path: string; onNavigate: (path: string) => void }) {
  const segments = path.split("/").filter((s) => s.length > 0);
  const crumbs: { name: string; target: string }[] = [{ name: "全部文件", target: "" }];
  let acc = "";
  for (const seg of segments) {
    acc = acc.length === 0 ? seg : `${acc}/${seg}`;
    crumbs.push({ name: seg, target: acc });
  }
  return (
    <nav className="breadcrumb" aria-label="目录路径">
      {crumbs.map((c, i) => (
        <span key={c.target} className="crumb">
          {i > 0 && <span className="crumb-sep">/</span>}
          {i === crumbs.length - 1 ? (
            <span className="crumb-current">{c.name}</span>
          ) : (
            <a
              href="/sources"
              onClick={(e) => {
                e.preventDefault();
                onNavigate(c.target);
              }}
            >
              {c.name}
            </a>
          )}
        </span>
      ))}
    </nav>
  );
}
