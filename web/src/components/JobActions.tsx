import type { Job } from "../types";

export function JobActions({
  job,
  busy,
  onCancel,
  onRetry,
}: {
  job: Job;
  busy: boolean;
  onCancel: (job: Job) => void;
  onRetry: (job: Job) => void;
}) {
  if (job.state === "queued" || job.state === "downloading" || job.state === "verifying" || job.state === "publishing") {
    return (
      <button type="button" className="btn btn-small" disabled={busy} onClick={() => onCancel(job)}>
        取消
      </button>
    );
  }
  if (job.state === "failed" || job.state === "cancelled") {
    return (
      <button type="button" className="btn btn-small" disabled={busy} onClick={() => onRetry(job)}>
        重试
      </button>
    );
  }
  return null;
}
