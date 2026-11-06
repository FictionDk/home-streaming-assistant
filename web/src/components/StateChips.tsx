import { FILE_STATE_LABELS, JOB_STATE_LABELS } from "../format";
import type { FileState, JobState } from "../types";

export function JobStateChip({ state }: { state: JobState }) {
  return <span className={`chip state-${state}`}>{JOB_STATE_LABELS[state]}</span>;
}

export function FileStateChip({ state }: { state: FileState }) {
  return <span className={`chip file-${state}`}>{FILE_STATE_LABELS[state]}</span>;
}
