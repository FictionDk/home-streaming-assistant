import { useCallback, useEffect, useState } from "react";
import * as api from "../api";
import { JobActions } from "../components/JobActions";
import { Link } from "../components/Link";
import { ProgressBar } from "../components/ProgressBar";
import { ErrorBox } from "../components/ErrorBox";
import { JobStateChip } from "../components/StateChips";
import { formatBytes, formatDateTime, ORIGIN_LABELS, shortVersion, SOURCE_LABELS } from "../format";
import { toError } from "../api";
import type { Job } from "../types";

export function DownloadDetailPage({ id }: { id: string }) {
  const [job, setJob] = useState<Job | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(
    async (silent: boolean) => {
      if (!silent) setLoading(true);
      try {
        const fresh = await api.fetchJob(id);
        setJob(fresh);
        if (!silent) setError(null);
      } catch (e) {
        if (!silent) setError(toError(e));
      } finally {
        if (!silent) setLoading(false);
      }
    },
    [id],
  );

  useEffect(() => {
    void load(false);
  }, [load]);

  const active =
    job !== null &&
    (job.state === "queued" || job.state === "downloading" || job.state === "verifying" || job.state === "publishing");

  useEffect(() => {
    if (!active) return;
    const refreshWhenVisible = () => {
      if (document.visibilityState === "visible") void load(true);
    };
    document.addEventListener("visibilitychange", refreshWhenVisible);
    const timer = window.setInterval(refreshWhenVisible, 3000);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", refreshWhenVisible);
    };
  }, [active, load]);

  const handleAction = async (action: (id: string) => Promise<Job>) => {
    if (job === null) return;
    setBusy(true);
    setActionError(null);
    try {
      setJob(await action(job.id));
    } catch (e) {
      setActionError(toError(e).message);
    } finally {
      setBusy(false);
    }
  };

  if (loading) {
    return (
      <section className="page">
        <div className="loading">加载中…</div>
      </section>
    );
  }

  if (error !== null) {
    return (
      <section className="page">
        <div className="page-header">
          <h1>任务详情</h1>
          <Link href="/downloads">返回任务列表</Link>
        </div>
        <ErrorBox error={error} onRetry={() => void load(false)} />
      </section>
    );
  }

  if (job === null) return null;

  return (
    <section className="page">
      <div className="page-header">
        <h1>任务详情</h1>
        <Link href="/downloads">返回任务列表</Link>
      </div>

      <div className="card">
        <div className="card-header">
          <JobStateChip state={job.state} />
          {job.cancelRequested && job.state !== "cancelled" && job.state !== "failed" && (
            <span className="chip cancelling">取消中</span>
          )}
        </div>
        <dl className="detail-grid">
          <dt>任务 ID</dt>
          <dd className="mono">{job.id}</dd>
          <dt>来源</dt>
          <dd>{SOURCE_LABELS[job.source]}</dd>
          <dt>路径</dt>
          <dd className="mono">{job.path}</dd>
          <dt>版本</dt>
          <dd className="mono">{shortVersion(job.version)}</dd>
          <dt>方式</dt>
          <dd>{ORIGIN_LABELS[job.origin]}</dd>
          <dt>尝试次数</dt>
          <dd>{job.attempt}</dd>
          <dt>进度</dt>
          <dd>
            <ProgressBar done={job.bytesDone} total={job.bytesTotal} />
            <span className="dim">
              {formatBytes(job.bytesDone)} / {job.bytesTotal > 0 ? formatBytes(job.bytesTotal) : "未知"}
            </span>
          </dd>
          <dt>错误码</dt>
          <dd className="mono">{job.errorCode ?? "—"}</dd>
          <dt>创建时间</dt>
          <dd>{formatDateTime(job.createdAt)}</dd>
          <dt>更新时间</dt>
          <dd>{formatDateTime(job.updatedAt)}</dd>
          {job.libraryId !== undefined && (
            <>
              <dt>本地文件</dt>
              <dd>
                <Link href="/library">查看本地库</Link>
              </dd>
            </>
          )}
        </dl>
        <div className="detail-actions">
          <JobActions
            job={job}
            busy={busy}
            onCancel={() => void handleAction(api.cancelJob)}
            onRetry={() => void handleAction(api.retryJob)}
          />
          {actionError !== null && <span className="inline-error">{actionError}</span>}
        </div>
      </div>
    </section>
  );
}
