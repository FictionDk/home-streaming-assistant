export type Source = "aliyun" | "gdrive";

export interface Entry {
  source: Source;
  path: string;
  name: string;
  isDir: boolean;
  size: number | null;
  modified: string | null;
  mimeType: string | null;
  version: string | null;
  localState: string | null;
}

export interface EntryPage {
  entries: Entry[];
  nextCursor: string | null;
}

export type JobState =
  | "queued"
  | "downloading"
  | "verifying"
  | "publishing"
  | "completed"
  | "cancelled"
  | "failed";

export interface Job {
  id: string;
  source: Source;
  path: string;
  version: string;
  origin: "manual" | "schedule";
  state: JobState;
  attempt: number;
  bytesDone: number;
  bytesTotal: number;
  cancelRequested: boolean;
  errorCode: string | null;
  createdAt: string;
  updatedAt: string;
  libraryId?: string;
}

export type FileState =
  | "pending_index"
  | "playable"
  | "deleting"
  | "deleted"
  | "missing";

export interface LibraryFile {
  id: string;
  source: Source;
  path: string;
  version: string;
  name: string;
  size: number;
  state: FileState;
  playbackURL: string | null;
  errorCode: string | null;
  createdAt: string;
}

export type SourceStatus = "ready" | "not_configured" | "unavailable" | "forbidden";

export interface SourceInfo {
  id: Source;
  status: SourceStatus;
  lastCheckedAt: string | null;
  errorCode: string | null;
}

export interface Session {
  user: string;
  csrfToken: string;
}

export interface ScheduleLastRun {
  startedAt: string | null;
  finishedAt: string | null;
  status: string;
  enqueued: number | null;
  errorCode: string | null;
}

export interface Schedule {
  path: string;
  enabled: boolean;
  time: string;
  timezone: string;
  lastRun: ScheduleLastRun | null;
}
