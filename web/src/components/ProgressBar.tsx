import type { CSSProperties } from "react";

export function ProgressBar({ done, total }: { done: number; total: number }) {
  const pct = total > 0 ? Math.min(100, Math.round((done / total) * 100)) : null;
  const style: CSSProperties | undefined = pct === null ? undefined : { width: `${pct}%` };
  return (
    <div className="progress">
      <div className={pct === null ? "progress-fill indeterminate" : "progress-fill"} style={style} />
      <span className="progress-text">{pct === null ? "—" : `${pct}%`}</span>
    </div>
  );
}
