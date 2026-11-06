import { useCallback, useEffect, useState } from "react";
import * as api from "../api";
import { EmptyState } from "../components/EmptyState";
import { ErrorBox } from "../components/ErrorBox";
import { FileStateChip } from "../components/StateChips";
import { formatBytes, formatDateTime, SOURCE_LABELS } from "../format";
import { toError } from "../api";
import type { LibraryFile, Source } from "../types";

const SOURCE_FILTERS: { value: Source | ""; label: string }[] = [
  { value: "", label: "全部来源" },
  { value: "aliyun", label: SOURCE_LABELS.aliyun },
  { value: "gdrive", label: SOURCE_LABELS.gdrive },
];

export function LibraryPage() {
  const [sourceFilter, setSourceFilter] = useState<Source | "">("");
  const [files, setFiles] = useState<LibraryFile[] | null>(null);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);

  const load = useCallback(
    async (src: Source | "", cursor: string | null, append: boolean, silent: boolean) => {
      if (!silent) setLoading(true);
      try {
        const res = await api.fetchLibrary(src, cursor);
        setFiles((prev) => (append && prev !== null ? [...prev, ...res.files] : res.files));
        setNextCursor(res.nextCursor);
        if (!silent) setError(null);
      } catch (e) {
        if (!silent) setError(toError(e));
      } finally {
        if (!silent) setLoading(false);
      }
    },
    [],
  );

  useEffect(() => {
    void load(sourceFilter, null, false, false);
  }, [sourceFilter, load]);

  const hasPendingIndex = files?.some((f) => f.state === "pending_index") ?? false;

  useEffect(() => {
    if (!hasPendingIndex) return;
    const refreshWhenVisible = () => {
      if (document.visibilityState === "visible") void load(sourceFilter, null, false, true);
    };
    document.addEventListener("visibilitychange", refreshWhenVisible);
    const timer = window.setInterval(refreshWhenVisible, 5000);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", refreshWhenVisible);
    };
  }, [hasPendingIndex, sourceFilter, load]);

  const handleDelete = async (file: LibraryFile) => {
    const confirmed = window.confirm(
      `仅删除本地副本，尽力保护正在播放的会话。\n确定删除「${file.name}」吗？`,
    );
    if (!confirmed) return;
    setBusyId(file.id);
    setActionError(null);
    try {
      await api.deleteLibraryFile(file.id);
      await load(sourceFilter, null, false, true);
    } catch (e) {
      setActionError(toError(e).message);
    } finally {
      setBusyId(null);
    }
  };

  return (
    <section className="page">
      <div className="page-header">
        <h1>本地文件</h1>
        <button type="button" className="btn" onClick={() => void load(sourceFilter, null, false, true)}>
          刷新
        </button>
      </div>

      <div className="chip-row">
        {SOURCE_FILTERS.map((f) => (
          <button
            key={f.value}
            type="button"
            className={`chip filter-chip${sourceFilter === f.value ? " active" : ""}`}
            onClick={() => setSourceFilter(f.value)}
          >
            {f.label}
          </button>
        ))}
      </div>

      <p className="dim hint">本地删除仅移除应用下载的本地副本，不影响云端文件。</p>

      {actionError !== null && <div className="inline-error" role="alert">{actionError}</div>}
      {error !== null && <ErrorBox error={error} onRetry={() => void load(sourceFilter, null, false, false)} />}

      {loading ? (
        <div className="loading">加载中…</div>
      ) : files === null ? null : files.length === 0 ? (
        <EmptyState text="本地还没有完整文件，去「云端」页下载吧" />
      ) : (
        <table className="data-table">
          <thead>
            <tr>
              <th>名称</th>
              <th>来源</th>
              <th>大小</th>
              <th>状态</th>
              <th>添加时间</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {files.map((file) => (
              <tr key={file.id}>
                <td data-th="名称" className="mono path-cell">
                  {file.name}
                </td>
                <td data-th="来源">{SOURCE_LABELS[file.source]}</td>
                <td data-th="大小">{formatBytes(file.size)}</td>
                <td data-th="状态">
                  <FileStateChip state={file.state} />
                  {file.errorCode !== null && <span className="mono error-code">{file.errorCode}</span>}
                </td>
                <td data-th="添加时间">{formatDateTime(file.createdAt)}</td>
                <td data-th="操作" className="actions-cell">
                  {file.playbackURL !== null && (
                    <a className="btn btn-small btn-primary" href={file.playbackURL} target="_blank" rel="noreferrer">
                      播放
                    </a>
                  )}
                  <button
                    type="button"
                    className="btn btn-small btn-danger"
                    disabled={busyId === file.id}
                    onClick={() => void handleDelete(file)}
                  >
                    删除
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {nextCursor !== null && (
        <div className="pagination">
          <button type="button" className="btn" onClick={() => void load(sourceFilter, nextCursor, true, false)}>
            加载更多
          </button>
        </div>
      )}
    </section>
  );
}
