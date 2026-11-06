import type {
  Entry,
  EntryPage,
  Job,
  LibraryFile,
  Schedule,
  Session,
  Source,
  SourceInfo,
} from "./types";

let csrfToken: string | null = null;
let unauthorizedHandler: (() => void) | null = null;

export function setUnauthorizedHandler(handler: () => void): void {
  unauthorizedHandler = handler;
}

export function storeSession(session: Session): void {
  csrfToken = session.csrfToken;
}

export function clearSession(): void {
  csrfToken = null;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

export function toError(e: unknown): Error {
  if (e instanceof Error) return e;
  return new Error("发生未知错误");
}

export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET" && csrfToken !== null) headers["X-CSRF-Token"] = csrfToken;

  let res: Response;
  try {
    res = await fetch(path, {
      method,
      headers,
      credentials: "same-origin",
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    throw new ApiError(0, "NETWORK_ERROR", "网络错误，无法连接服务器");
  }

  if (res.status === 401 && unauthorizedHandler !== null) {
    unauthorizedHandler();
  }

  if (res.status === 204) return undefined as T;

  let data: unknown = null;
  const text = await res.text();
  if (text.length > 0) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }

  if (!res.ok) {
    let code = "UNKNOWN";
    let message = "请求失败";
    const err = (data as { error?: { code?: unknown; message?: unknown } } | null)?.error;
    if (err !== undefined && err !== null) {
      if (typeof err.code === "string" && err.code.length > 0) code = err.code;
      if (typeof err.message === "string" && err.message.length > 0) message = err.message;
    }
    throw new ApiError(res.status, code, message);
  }

  return data as T;
}

// ---- auth ----

export function fetchSession(): Promise<Session> {
  return request<Session>("GET", "/api/auth/session");
}

export function login(username: string, password: string): Promise<Session> {
  return request<Session>("POST", "/api/auth/login", { username, password });
}

export async function logout(): Promise<void> {
  await request<void>("POST", "/api/auth/logout");
}

// ---- sources ----

export function fetchSources(): Promise<{ sources: SourceInfo[] }> {
  return request<{ sources: SourceInfo[] }>("GET", "/api/sources");
}

// ---- entries ----

export function fetchEntries(
  source: Source,
  path: string,
  cursor: string | null,
  refresh: boolean,
): Promise<EntryPage> {
  const q = new URLSearchParams({ source, path });
  if (cursor !== null && cursor.length > 0) q.set("cursor", cursor);
  if (refresh) q.set("refresh", "true");
  return request<EntryPage>("GET", `/api/entries?${q.toString()}`);
}

export function fetchEntry(source: Source, path: string): Promise<Entry> {
  const q = new URLSearchParams({ source, path });
  return request<Entry>("GET", `/api/entry?${q.toString()}`);
}

// ---- downloads ----

export function createDownload(source: Source, path: string): Promise<Job> {
  return request<Job>("POST", "/api/downloads", { source, path });
}

export function fetchJobs(
  state: string | null,
  cursor: string | null,
): Promise<{ jobs: Job[]; nextCursor: string | null }> {
  const q = new URLSearchParams();
  if (state !== null && state.length > 0) q.set("state", state);
  if (cursor !== null && cursor.length > 0) q.set("cursor", cursor);
  const qs = q.toString();
  return request<{ jobs: Job[]; nextCursor: string | null }>(
    "GET",
    qs.length > 0 ? `/api/downloads?${qs}` : "/api/downloads",
  );
}

export function fetchJob(id: string): Promise<Job> {
  return request<Job>("GET", `/api/downloads/${encodeURIComponent(id)}`);
}

export function cancelJob(id: string): Promise<Job> {
  return request<Job>("POST", `/api/downloads/${encodeURIComponent(id)}/cancel`);
}

export function retryJob(id: string): Promise<Job> {
  return request<Job>("POST", `/api/downloads/${encodeURIComponent(id)}/retry`);
}

// ---- library ----

export function fetchLibrary(
  source: string | null,
  cursor: string | null,
): Promise<{ files: LibraryFile[]; nextCursor: string | null }> {
  const q = new URLSearchParams();
  if (source !== null && source.length > 0) q.set("source", source);
  if (cursor !== null && cursor.length > 0) q.set("cursor", cursor);
  const qs = q.toString();
  return request<{ files: LibraryFile[]; nextCursor: string | null }>(
    "GET",
    qs.length > 0 ? `/api/library?${qs}` : "/api/library",
  );
}

export async function deleteLibraryFile(id: string): Promise<void> {
  await request<void>("DELETE", `/api/library/${encodeURIComponent(id)}`);
}

// ---- schedule ----

export function fetchSchedule(): Promise<Schedule> {
  return request<Schedule>("GET", "/api/schedules/gdrive");
}

export interface ScheduleInput {
  path: string;
  enabled: boolean;
  time: string;
  timezone: string;
}

export function saveSchedule(input: ScheduleInput): Promise<Schedule> {
  return request<Schedule>("PUT", "/api/schedules/gdrive", input);
}
