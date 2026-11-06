import { useCallback, useEffect, useState } from "react";
import * as api from "../api";
import { JobActions } from "../components/JobActions";
import { Link } from "../components/Link";
import { ProgressBar } from "../components/ProgressBar";
import { EmptyState } from "../components/EmptyState";
import { ErrorBox } from "../components/ErrorBox";
import { JobStateChip } from "../components/StateChips";
import { formatBytes, formatDateTime, ORIGIN_LABELS, SOURCE_LABELS } from "../format";
import { toError } from "../api";
import type { Job, JobState } from "../types";

const FILTERS: { value: JobState | ""; label: string }[] = [
  { value: "", label: "全部" },
  { value: "queued", label: "排队中" },
  { value: "downloading", label: "下载中" },
  { value: "verifying", label: "校验中" },
  { value: "publishing", label: "发布中" },
  { value: "completed", label: "已完成" },
  { value: "cancelled", label: "已取消" },
  { value: "failed", label: "失败" },
];

const ACTIVE_JOB_STATES: Record<JobState, boolean> = {
  queued: true,
  downloading: true,
  verifying: true,
  publishing: true,
  completed: false,
  cancelled: false,
  failed: false,
};

export function DownloadsPage() {
  const [filter, setFilter] = useState<JobState | "">("");
  const [jobs, setJobs] = useState<Job[] | null>(null);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);

  const load = useCallback(
    async (stateFilter: JobState | "", cursor: string | null, append: boolean, silent: boolean) => {
      if (!silent) setLoading(true);
      try {
        const res = await api.fetchJobs(stateFilter, cursor);
        setJobs((prev) => (append && prev !== null ? [...prev, ...res.jobs] : res.jobs));
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
    void load(filter, null, false, false);
  }, [filter, load]);

  const hasActive = jobs?.some((j) => ACTIVE_JOB_STATES[j.state]) ?? false;

  useEffect(() => {
    if (!hasActive) return;
    const refreshWhenVisible = () => {
      if (document.visibilityState === "visible") void load(filter, null, false, true);
    };
    document.addEventListener("visibilitychange", refreshWhenVisible);
    const timer = window.setInterval(refreshWhenVisible, 3000);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", refreshWhenVisible);
    };
  }, [hasActive, filter, load]);

  const patchJob = (updated: Job) => {
    setJobs((prev) => (prev !== null ? prev.map((j) => (j.id === updated.id ? updated : j)) : prev));
  };

  const handleCancel = async (job: Job) => {
    setBusyId(job.id);
    setActionError(null);
    try {
      patchJob(await api.cancelJob(job.id));
    } catch (e) {
      setActionError(toError(e).message);
    } finally {
      setBusyId(null);
    }
  };

  const handleRetry = async (job: Job) => {
    setBusyId(job.id);
    setActionError(null);
    try {
      patchJob(await api.retryJob(job.id));
    } catch (e) {
      setActionError(toError(e).message);
    } finally {
      setBusyId(null);
    }
  };

  return (
    <section className="page">
      <div className="page-header">
        <h1>下载任务</h1>
      </div>

      <div className="chip-row">
        {FILTERS.map((f) => (
          <button
            key={f.value}
            type="button"
            className={`chip filter-chip${filter === f.value ? " active" : ""}`}
            onClick={() => setFilter(f.value)}
          >
            {f.label}
          </button>
        ))}
      </div>

      {actionError !== null && <div className="inline-error" role="alert">{actionError}</div>}
      {error !== null && <ErrorBox error={error} onRetry={() => void load(filter, null, false, false)} />}

      {loading ? (
        <div className="loading">加载中…</div>
      ) : jobs === null ? null : jobs.length === 0 ? (
        <EmptyState text="暂无任务" />
      ) : (
        <table className="data-table">
          <thead>
            <tr>
              <th>创建时间</th>
              <th>来源</th>
              <th>路径</th>
              <th>状态</th>
              <th>进度</th>
              <th>方式</th>
              <th>错误</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {jobs.map((job) => (
              <tr key={job.id}>
                <td data-th="创建时间">
                  <Link href={`/downloads/${job.id}`} className="entry-link mono">
                    {formatDateTime(job.createdAt)}
                  </Link>
                </td>
                <td data-th="来源">{SOURCE_LABELS[job.source]}</td>
                <td data-th="路径" className="mono path-cell">
                  <Link href={`/downloads/${job.id}`} className="entry-link">
                    {job.path}
                  </Link>
                </td>
                <td data-th="状态">
                  <JobStateChip state={job.state} />
                  {job.cancelRequested && job.state !== "cancelled" && job.state !== "failed" && (
                    <span className="chip cancelling">取消中</span>
                  )}
                </td>
                <td data-th="进度">
                  {job.state === "completed" ? (
                    `${formatBytes(job.bytesTotal)} / ${formatBytes(job.bytesTotal)}`
                  ) : (
                    <>
                      <ProgressBar done={job.bytesDone} total={job.bytesTotal} />
                      <span className="dim">
                        {formatBytes(job.bytesDone)} / {job.bytesTotal > 0 ? formatBytes(job.bytesTotal) : "未知"}
                      </span>
                    </>
                  )}
                </td>
                <td data-th="方式">{ORIGIN_LABELS[job.origin]}</td>
                <td data-th="错误" className="mono error-code">
                  {job.errorCode ?? "—"}
                </td>
                <td data-th="操作">
                  <JobActions
                    job={job}
                    busy={busyId === job.id}
                    onCancel={(j) => void handleCancel(j)}
                    onRetry={(j) => void handleRetry(j)}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {nextCursor !== null && (
        <div className="pagination">
          <button type="button" className="btn" onClick={() => void load(filter, nextCursor, true, false)}>
            加载更多
          </button>
        </div>
      )}
    </section>
  );
}
