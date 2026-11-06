import { useCallback, useEffect, useRef, useState } from "react";
import * as api from "../api";
import { Link } from "../components/Link";
import { Breadcrumb, SourceChips } from "../components/SourceNav";
import { EmptyState } from "../components/EmptyState";
import { ErrorBox } from "../components/ErrorBox";
import { formatBytes, formatDateTime, shortVersion, SOURCE_LABELS, SOURCE_STATUS_LABELS } from "../format";
import { toError } from "../api";
import type { Entry, Source, SourceInfo } from "../types";

interface DirPage {
  entries: Entry[];
  nextCursor: string | null;
}

interface DetailState {
  path: string;
  entry: Entry | null;
  loading: boolean;
  error: Error | null;
}

function EntryTable({
  entries,
  onOpenDir,
  onOpenDetail,
}: {
  entries: Entry[];
  onOpenDir: (path: string) => void;
  onOpenDetail: (entry: Entry) => void;
}) {
  return (
    <table className="data-table">
      <thead>
        <tr>
          <th>名称</th>
          <th>类型</th>
          <th>大小</th>
          <th>修改时间</th>
          <th>本地状态</th>
        </tr>
      </thead>
      <tbody>
        {entries.map((entry) => (
          <tr key={entry.path}>
            <td data-th="名称">
              {entry.isDir ? (
                <a
                  href={`/sources#dir`}
                  className="entry-link dir"
                  onClick={(e) => {
                    e.preventDefault();
                    onOpenDir(entry.path);
                  }}
                >
                  📁 {entry.name}
                </a>
              ) : (
                <a
                  href={`/sources#file`}
                  className="entry-link"
                  onClick={(e) => {
                    e.preventDefault();
                    onOpenDetail(entry);
                  }}
                >
                  {entry.name}
                </a>
              )}
            </td>
            <td data-th="类型">{entry.isDir ? "文件夹" : (entry.mimeType ?? "文件")}</td>
            <td data-th="大小">{entry.isDir ? "—" : formatBytes(entry.size)}</td>
            <td data-th="修改时间">{formatDateTime(entry.modified)}</td>
            <td data-th="本地状态">{entry.localState ?? "—"}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function DetailPanel({
  detail,
  downloadBusy,
  downloadMessage,
  downloadError,
  downloadJobId,
  onDownload,
  onClose,
}: {
  detail: DetailState;
  downloadBusy: boolean;
  downloadMessage: string | null;
  downloadError: string | null;
  downloadJobId: string | null;
  onDownload: (entry: Entry) => void;
  onClose: () => void;
}) {
  return (
    <div className="card detail-card">
      <div className="card-header">
        <h2>文件详情</h2>
        <button type="button" className="btn btn-ghost" onClick={onClose}>
          关闭
        </button>
      </div>
      {detail.loading && <div className="loading">加载中…</div>}
      {detail.error !== null && <ErrorBox error={detail.error} />}
      {detail.entry !== null && (
        <>
          <dl className="detail-grid">
            <dt>名称</dt>
            <dd>{detail.entry.name}</dd>
            <dt>来源</dt>
            <dd>{SOURCE_LABELS[detail.entry.source]}</dd>
            <dt>大小</dt>
            <dd>{detail.entry.isDir ? "—" : formatBytes(detail.entry.size)}</dd>
            <dt>修改时间</dt>
            <dd>{formatDateTime(detail.entry.modified)}</dd>
            <dt>MIME 类型</dt>
            <dd>{detail.entry.mimeType ?? "—"}</dd>
            <dt>版本</dt>
            <dd className="mono">{shortVersion(detail.entry.version)}</dd>
            <dt>本地状态</dt>
            <dd>{detail.entry.localState ?? "—"}</dd>
          </dl>
          {!detail.entry.isDir && (
            <div className="detail-actions">
              <button
                type="button"
                className="btn btn-primary"
                disabled={downloadBusy}
                onClick={() => onDownload(detail.entry as Entry)}
              >
                下载
              </button>
              {downloadMessage !== null && (
                <span className="inline-ok">
                  {downloadMessage}
                  {downloadJobId !== null && (
                    <>
                      {"　"}
                      <Link href={`/downloads/${downloadJobId}`}>查看任务</Link>
                    </>
                  )}
                </span>
              )}
              {downloadError !== null && <span className="inline-error">{downloadError}</span>}
            </div>
          )}
        </>
      )}
    </div>
  );
}

export function SourcesPage() {
  const [sources, setSources] = useState<SourceInfo[] | null>(null);
  const [sourcesError, setSourcesError] = useState<Error | null>(null);
  const [source, setSource] = useState<Source>("aliyun");
  const [path, setPath] = useState("");
  const [pages, setPages] = useState<DirPage[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const [detail, setDetail] = useState<DetailState | null>(null);
  const [downloadBusy, setDownloadBusy] = useState(false);
  const [downloadMessage, setDownloadMessage] = useState<string | null>(null);
  const [downloadError, setDownloadError] = useState<string | null>(null);
  const [downloadJobId, setDownloadJobId] = useState<string | null>(null);
  const genRef = useRef(0);

  const loadSources = useCallback(async () => {
    setSourcesError(null);
    try {
      const res = await api.fetchSources();
      setSources(res.sources);
    } catch (e) {
      setSourcesError(toError(e));
    }
  }, []);

  useEffect(() => {
    void loadSources();
  }, [loadSources]);

  const loadPage = useCallback(
    async (src: Source, p: string, cursor: string | null, refresh: boolean, append: boolean) => {
      const gen = ++genRef.current;
      setLoading(true);
      setError(null);
      try {
        const page = await api.fetchEntries(src, p, cursor, refresh);
        if (gen !== genRef.current) return;
        if (append) {
          setPages((prev) => [...prev, page]);
        } else {
          setPages([page]);
        }
      } catch (e) {
        if (gen !== genRef.current) return;
        setError(toError(e));
      } finally {
        if (gen === genRef.current) setLoading(false);
      }
    },
    [],
  );

  useEffect(() => {
    void loadPage(source, path, null, false, false);
  }, [source, path, loadPage]);

  const sourceInfo = sources?.find((s) => s.id === source) ?? null;
  const sourceNotReady = sourceInfo !== null && sourceInfo.status !== "ready";
  const current = pages.length > 0 ? pages[pages.length - 1] : null;
  const canPrev = pages.length > 1;
  const canNext = current?.nextCursor != null;

  const openDir = (p: string) => {
    setDetail(null);
    setDownloadMessage(null);
    setDownloadError(null);
    setDownloadJobId(null);
    setPath(p);
  };

  const openDetail = async (entry: Entry) => {
    setDownloadMessage(null);
    setDownloadError(null);
    setDownloadJobId(null);
    setDetail({ path: entry.path, entry: null, loading: true, error: null });
    try {
      const fresh = await api.fetchEntry(entry.source, entry.path);
      setDetail({ path: entry.path, entry: fresh, loading: false, error: null });
    } catch (e) {
      setDetail({ path: entry.path, entry: null, loading: false, error: toError(e) });
    }
  };

  const startDownload = async (entry: Entry) => {
    setDownloadBusy(true);
    setDownloadMessage(null);
    setDownloadError(null);
    setDownloadJobId(null);
    try {
      const job = await api.createDownload(entry.source, entry.path);
      setDownloadJobId(job.id);
      setDownloadMessage("下载任务已提交");
    } catch (e) {
      setDownloadError(toError(e).message);
    } finally {
      setDownloadBusy(false);
    }
  };

  return (
    <section className="page">
      <div className="page-header">
        <h1>云端文件</h1>
        <button type="button" className="btn" onClick={() => void loadSources()}>
          刷新状态
        </button>
      </div>

      {sourcesError !== null && <ErrorBox error={sourcesError} onRetry={() => void loadSources()} />}

      <SourceChips
        sources={sources ?? []}
        current={source}
        onSelect={(src) => {
          setDetail(null);
          setDownloadMessage(null);
          setDownloadError(null);
          setDownloadJobId(null);
          setSource(src);
          setPath("");
        }}
      />

      {sourceInfo !== null && sourceInfo.status !== "ready" && (
        <div className="notice" role="status">
          来源当前{SOURCE_STATUS_LABELS[sourceInfo.status]}
          {sourceInfo.errorCode !== null ? `（错误码：${sourceInfo.errorCode}）` : ""}，暂时无法浏览该来源。
        </div>
      )}

      <Breadcrumb path={path} onNavigate={openDir} />

      {detail !== null && (
        <DetailPanel
          detail={detail}
          downloadBusy={downloadBusy}
          downloadMessage={downloadMessage}
          downloadError={downloadError}
          downloadJobId={downloadJobId}
          onDownload={(entry) => void startDownload(entry)}
          onClose={() => setDetail(null)}
        />
      )}

      {!sourceNotReady && (
        <>
          <div className="toolbar">
            <button
              type="button"
              className="btn"
              disabled={loading}
              onClick={() => void loadPage(source, path, null, true, false)}
            >
              刷新目录
            </button>
          </div>

          {error !== null && <ErrorBox error={error} onRetry={() => void loadPage(source, path, null, false, false)} />}

          {loading ? (
            <div className="loading">加载中…</div>
          ) : current === null ? null : current.entries.length === 0 ? (
            <EmptyState text="目录为空" />
          ) : (
            <EntryTable entries={current.entries} onOpenDir={openDir} onOpenDetail={(entry) => void openDetail(entry)} />
          )}

          <div className="pagination">
            <button
              type="button"
              className="btn"
              disabled={!canPrev || loading}
              onClick={() => setPages((prev) => prev.slice(0, -1))}
            >
              上一页
            </button>
            <button
              type="button"
              className="btn"
              disabled={!canNext || loading}
              onClick={() => {
                if (current !== null) void loadPage(source, path, current.nextCursor, false, true);
              }}
            >
              下一页
            </button>
          </div>
        </>
      )}
    </section>
  );
}
