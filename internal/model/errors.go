package model

import (
	"fmt"
	"net/http"
)

// Error is an error carrying a stable public code. Detail is for logs
// only and MUST never contain upstream URLs, tokens or response bodies.
type Error struct {
	Code   string
	Status int
	detail string
}

func (e *Error) Error() string {
	if e.detail == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.detail)
}

func (e *Error) Detail() string { return e.detail }

func NewError(code string, status int, detail string) *Error {
	return &Error{Code: code, Status: status, detail: detail}
}

func E(code string, status int, detail string) error {
	return NewError(code, status, detail)
}

// Public error codes. API responses render Code with a fixed Chinese
// message from Messages; upstream raw text is never included.
const (
	CodeUnauthorized        = "UNAUTHORIZED"
	CodeForbidden           = "FORBIDDEN"
	CodeNotFound            = "NOT_FOUND"
	CodeMethodNotAllowed    = "METHOD_NOT_ALLOWED"
	CodeInvalidRequest      = "INVALID_REQUEST"
	CodeInvalidPath         = "INVALID_PATH"
	CodeInternal            = "INTERNAL"
	CodeNotConfigured       = "NOT_CONFIGURED"
	CodeCursorExpired       = "CURSOR_EXPIRED"
	CodeDirectoryTooLarge   = "DIRECTORY_TOO_LARGE"
	CodeNoSpace             = "NO_SPACE"
	CodeVersionUnavailable  = "VERSION_UNAVAILABLE"
	CodeRemoteChanged       = "REMOTE_CHANGED"
	CodeFileInUse           = "FILE_IN_USE"
	CodeJellyfinUnavailable = "JELLYFIN_UNAVAILABLE"
	CodeUpstreamError       = "UPSTREAM_ERROR"
	CodeUpstreamForbidden   = "UPSTREAM_FORBIDDEN"
	CodeUpstreamUnavailable = "UPSTREAM_UNAVAILABLE"
	CodeIndexConflict       = "INDEX_CONFLICT"
	CodeConflict            = "CONFLICT"
)

// InterruptedCode marks jobs killed by shutdown/restart.
const InterruptedCode = "INTERRUPTED"

// Messages maps codes to fixed Chinese public messages.
var Messages = map[string]string{
	CodeUnauthorized:        "未登录或会话已过期",
	CodeForbidden:           "无权访问该资源",
	CodeNotFound:            "资源不存在",
	CodeMethodNotAllowed:    "请求方法不支持",
	CodeInvalidRequest:      "请求参数无效",
	CodeInvalidPath:         "路径不合法",
	CodeInternal:            "服务器内部错误",
	CodeNotConfigured:       "该来源或服务未配置",
	CodeCursorExpired:       "目录列表已过期，请刷新后重试",
	CodeDirectoryTooLarge:   "目录过大，请选择更小的子目录",
	CodeNoSpace:             "磁盘空间不足",
	CodeVersionUnavailable:  "无法确定该文件的可靠版本标识",
	CodeRemoteChanged:       "远端文件在校验期间发生变化",
	CodeFileInUse:           "文件正在播放，拒绝删除",
	CodeJellyfinUnavailable: "Jellyfin 服务不可用",
	CodeUpstreamError:       "上游服务返回错误",
	CodeUpstreamForbidden:   "上游拒绝访问，请检查授权",
	CodeUpstreamUnavailable: "上游服务暂时不可用",
	CodeIndexConflict:       "媒体库索引存在冲突，请稍后重试",
	CodeConflict:            "请求与当前状态冲突",
}

// HTTPStatus maps job/public error codes onto HTTP statuses.
func HTTPStatus(code string) int {
	switch code {
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbidden, CodeUpstreamForbidden:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case CodeInvalidRequest, CodeInvalidPath, CodeVersionUnavailable:
		return http.StatusBadRequest
	case CodeCursorExpired, CodeNoSpace, CodeRemoteChanged, CodeFileInUse, CodeConflict:
		return http.StatusConflict
	case CodeNotConfigured, CodeJellyfinUnavailable, CodeUpstreamUnavailable:
		return http.StatusServiceUnavailable
	case CodeUpstreamError:
		return http.StatusBadGateway
	case CodeDirectoryTooLarge:
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusInternalServerError
	}
}

// ErrCode extracts the public code from any error.
func ErrCode(err error) string {
	var e *Error
	if errorsAs(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// errorsAs avoids importing errors twice in this file.
func errorsAs(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
