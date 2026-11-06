import { useCallback, useEffect, useState } from "react";
import * as api from "../api";
import { ErrorBox } from "../components/ErrorBox";
import { formatDateTime, SOURCE_STATUS_LABELS } from "../format";
import { toError } from "../api";
import type { Schedule, SourceInfo } from "../types";

interface ScheduleForm {
  path: string;
  enabled: boolean;
  time: string;
  timezone: string;
}

function LastRunCard({ schedule }: { schedule: Schedule }) {
  if (schedule.lastRun === null) {
    return (
      <div className="card">
        <h2>最近一次运行</h2>
        <p className="dim">计划尚未运行过。</p>
      </div>
    );
  }
  const run = schedule.lastRun;
  return (
    <div className="card">
      <h2>最近一次运行</h2>
      <dl className="detail-grid">
        <dt>状态</dt>
        <dd className="mono">{run.status}</dd>
        <dt>开始时间</dt>
        <dd>{formatDateTime(run.startedAt)}</dd>
        <dt>结束时间</dt>
        <dd>{formatDateTime(run.finishedAt)}</dd>
        <dt>入队任务数</dt>
        <dd>{run.enqueued ?? "—"}</dd>
        {run.errorCode !== null && (
          <>
            <dt>错误码</dt>
            <dd className="mono">{run.errorCode}</dd>
          </>
        )}
      </dl>
    </div>
  );
}

export function SettingsPage() {
  const [schedule, setSchedule] = useState<Schedule | null>(null);
  const [form, setForm] = useState<ScheduleForm>({ path: "", enabled: false, time: "03:00", timezone: "Asia/Shanghai" });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saveSuccess, setSaveSuccess] = useState(false);
  const [sources, setSources] = useState<SourceInfo[] | null>(null);
  const [sourcesError, setSourcesError] = useState<Error | null>(null);

  const loadSchedule = useCallback(async () => {
    setLoading(true);
    try {
      const fresh = await api.fetchSchedule();
      setSchedule(fresh);
      setForm({ path: fresh.path, enabled: fresh.enabled, time: fresh.time, timezone: fresh.timezone });
      setError(null);
    } catch (e) {
      setError(toError(e));
    } finally {
      setLoading(false);
    }
  }, []);

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
    void loadSchedule();
    void loadSources();
  }, [loadSchedule, loadSources]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setSaveError(null);
    setSaveSuccess(false);
    try {
      const saved = await api.saveSchedule({
        path: form.path,
        enabled: form.enabled,
        time: form.time,
        timezone: form.timezone,
      });
      setSchedule(saved);
      setForm({ path: saved.path, enabled: saved.enabled, time: saved.time, timezone: saved.timezone });
      setSaveSuccess(true);
    } catch (err) {
      setSaveError(toError(err).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="page">
      <div className="page-header">
        <h1>设置</h1>
      </div>

      <div className="card">
        <h2>Google Drive 定时下载</h2>
        {loading ? (
          <div className="loading">加载中…</div>
        ) : error !== null ? (
          <ErrorBox error={error} onRetry={() => void loadSchedule()} />
        ) : (
          <form onSubmit={(e) => void handleSave(e)} className="settings-form">
            <label className="field">
              <span className="field-label">下载目录（GDrive 允许根目录下的相对路径，不能为根目录本身）</span>
              <input
                type="text"
                value={form.path}
                onChange={(e) => setForm((f) => ({ ...f, path: e.target.value }))}
                placeholder="例如 movies/weekly"
                required
              />
            </label>
            <label className="field field-inline">
              <input
                type="checkbox"
                checked={form.enabled}
                onChange={(e) => setForm((f) => ({ ...f, enabled: e.target.checked }))}
              />
              <span>启用每日定时下载</span>
            </label>
            <label className="field">
              <span className="field-label">每天时间（HH:MM，本地时区计划）</span>
              <input
                type="time"
                value={form.time}
                onChange={(e) => setForm((f) => ({ ...f, time: e.target.value }))}
                required
              />
            </label>
            <label className="field">
              <span className="field-label">IANA 时区</span>
              <input
                type="text"
                value={form.timezone}
                onChange={(e) => setForm((f) => ({ ...f, timezone: e.target.value }))}
                placeholder="Asia/Shanghai"
                required
              />
            </label>
            <div className="detail-actions">
              <button type="submit" className="btn btn-primary" disabled={saving}>
                保存
              </button>
              {saveSuccess && <span className="inline-ok">已保存</span>}
              {saveError !== null && <span className="inline-error">{saveError}</span>}
            </div>
          </form>
        )}
      </div>

      {schedule !== null && <LastRunCard schedule={schedule} />}

      <div className="card">
        <div className="card-header">
          <h2>来源状态</h2>
          <button type="button" className="btn" onClick={() => void loadSources()}>
            刷新
          </button>
        </div>
        {sourcesError !== null && <ErrorBox error={sourcesError} onRetry={() => void loadSources()} />}
        {sources !== null && (
          <ul className="source-status-list">
            {sources.map((s) => (
              <li key={s.id}>
                <span>{s.id === "aliyun" ? "阿里云盘" : "Google Drive"}</span>
                <span className={`chip status-${s.status}`}>{SOURCE_STATUS_LABELS[s.status]}</span>
                <span className="dim">最近检查：{formatDateTime(s.lastCheckedAt)}</span>
                {s.errorCode !== null && <span className="mono error-code">{s.errorCode}</span>}
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}
