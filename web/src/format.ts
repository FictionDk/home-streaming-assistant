import type { FileState, Job, JobState, Source, SourceStatus } from "./types";

export const SOURCE_LABELS: Record<Source, string> = {
  aliyun: "阿里云盘",
  gdrive: "Google Drive",
};

export const SOURCE_STATUS_LABELS: Record<SourceStatus, string> = {
  ready: "就绪",
  not_configured: "未配置",
  unavailable: "不可用",
  forbidden: "无权限",
};

export const JOB_STATE_LABELS: Record<JobState, string> = {
  queued: "排队中",
  downloading: "下载中",
  verifying: "校验中",
  publishing: "发布中",
  completed: "已完成",
  cancelled: "已取消",
  failed: "失败",
};

export const FILE_STATE_LABELS: Record<FileState, string> = {
  pending_index: "待索引",
  playable: "可播放",
  deleting: "删除中",
  deleted: "已删除",
  missing: "缺失",
};

export const ORIGIN_LABELS: Record<Job["origin"], string> = {
  manual: "手动",
  schedule: "定时",
};

export function formatBytes(bytes: number | null): string {
  if (bytes === null) return "—";
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KiB", "MiB", "GiB", "TiB", "PiB"];
  let value = bytes;
  let unitIndex = -1;
  do {
    value /= 1024;
    unitIndex += 1;
  } while (value >= 1024 && unitIndex < units.length - 1);
  return `${value.toFixed(1)} ${units[unitIndex]}`;
}

export function formatDateTime(iso: string | null): string {
  if (iso === null || iso.length === 0) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    dateStyle: "medium",
    timeStyle: "medium",
    hour12: false,
  }).format(d);
}

export function shortVersion(version: string | null): string {
  if (version === null || version.length === 0) return "—";
  return version.length > 12 ? `${version.slice(0, 12)}…` : version;
}
