import { ApiError, toError } from "../api";

export function ErrorBox({ error, onRetry }: { error: Error; onRetry?: () => void }) {
  const message = error instanceof ApiError ? error.message : toError(error).message;
  return (
    <div className="error-box" role="alert">
      <div className="error-message">{message}</div>
      {onRetry !== undefined && (
        <button type="button" className="btn" onClick={onRetry}>
          重试
        </button>
      )}
    </div>
  );
}
