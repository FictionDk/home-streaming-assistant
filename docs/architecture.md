# home-streaming-assistant 架构与模块契约

> 状态：应用已实现并通过本机集成测试与 deb rootless 部署。本文描述已落地的
> 架构、模块间调用契约（固定接口）与关键不变量。字段名与状态名是唯一事实来源，
> 任何模块不得自行改名。

## 1. 组件与数据流

```mermaid
flowchart LR
  B[浏览器 React SPA] -->|同源 HTTP + Cookie/CSRF| G[Go 单进程<br/>api + worker + 调度 + 索引]
  G --> S[(SQLite /data/state)]
  G -->|REST + token| A[Alist v3.63<br/>AliyundriveOpen 网关]
  G -->|子进程 rclone| AL[aliyun WebDAV remote]
  G -->|子进程 rclone| GD[gdrive-ro remote]
  A --> ALY[阿里云盘]
  GD --> GOO[Google Drive]
  G -->|copyto| I[/data/files/.incoming]
  I -->|fsync+rename| M[/data/files/media]
  M -->|只读 /media| J[Jellyfin 12.4]
  J --> P[播放器]
```

- 浏览器只访问 Go；不直连 Alist、rclone、SQLite，也拿不到任何上游令牌。
- 下载、调度、索引、删除全部在 Go 单进程内完成；rclone 是唯一子进程。
- `/data/files` 是**唯一**包含 `.incoming` 与 `media` 的挂载点（保证原子 rename，跨挂载会 EXDEV）。

## 2. 目录结构

```
cmd/server/main.go        装配、恢复、信号处理
internal/config           HSA_CONFIG_FILE 单 JSON 配置 + HSA_LISTEN_ADDR 覆盖
internal/model            Source/Entry/Job/File/版本指纹/错误码（唯一契约）
internal/storage          SQLite 迁移、进程锁、全部行级访问
internal/catalog          Backend 接口 + Alist 客户端 + rclone lsjson 客户端 + 游标
internal/downloads        单 Worker 队列、rclone 执行、校验、原子发布、恢复
internal/schedule         gdrive 每日计划（15s 检查、revision 取消）
internal/jellyfin         API 客户端 + 按精确路径的索引器
internal/api              认证/CSRF/Host 校验、业务路由、SPA 静态服务
web/                      React 19 + Vite 8（构建产物 go:embed 进二进制）
deploy/                   images.json、config.example.json、quadlet/
scripts/                  build-image.sh / deploy-deb.sh / install-deb.sh
```

## 3. 模块间调用契约

### 3.1 catalog（上游目录）

```go
// 固定接口（internal/catalog）
type Backend interface {
    List(ctx, path, cursor string, refresh bool, cs *CursorStore) (model.EntryPage, error)
    Stat(ctx, path string) (model.Entry, error)
    Probe(ctx) error
}

type Manager struct{ ... }
func (m *Manager) List(ctx, source model.Source, path, cursor string, refresh bool) (model.EntryPage, error) // 30s 超时
func (m *Manager) Stat(ctx, source model.Source, path string) (model.Entry, error)                            // 30s 超时
func (m *Manager) Probe(ctx, source model.Source)          // 只探根元数据，不扫盘
func (m *Manager) Statuses() []model.SourceStatus          // ready|not_configured|unavailable|forbidden
```

- **Aliyun**（`aliyun.go`）：Alist v3 `POST /api/auth/login` 取 token，`Authorization` 直接传 token（非 Bearer）；`fs/list`（`per_page=100`，按 total 翻页）、`fs/get?refresh=true` 做 Stat；**永不透传 raw_url/sign/thumb**。401 时互斥重新登录一次并重试原请求一次。
- **GDrive**（`gdrive.go`）：`rclone lsjson --config <专用配置> --drive-skip-shortcuts [--stat] --hash gdrive-ro:<root/path>`；受控环境变量（不继承宿主 remote）；单目录 100000 项 / 64 MiB 上限 → `DIRECTORY_TOO_LARGE`；缓存 60s、总量 128 MiB；目录优先 + UTF-8 字节序排序、100 项一页。
- **游标**（`CursorStore`）：服务端随机 token，绑定 source/path/page/代次，5 分钟 TTL；刷新或错目录 → 409 `CURSOR_EXPIRED`。云端并发修改不承诺快照一致性。
- 配置检查：`gdrive-ro` 必须存在、`type=drive`、`scope=drive.readonly`，不符则禁用来源（`NOT_CONFIGURED`）；这是部署自检，**不替代**对实授 scope 的核验。

### 3.2 downloads（队列与发布）

```go
// 固定接口（internal/downloads）
func (m *Manager) Enqueue(ctx, source, path, origin string) (model.Job, created bool, err error)
func (m *Manager) Cancel(ctx, id string) (job model.Job, pending bool, err error)
func (m *Manager) Retry(ctx, id string) (model.Job, error)
func (m *Manager) GetJob(ctx, id string) (model.Job, error)
func (m *Manager) Recover(...)        // main 启动时调用，早于 Worker 启动
func FinalRel(source, origin, jobID string, attempt int64, basename string) string
```

调用顺序（`Enqueue`）：校验路径 → `catalog.Stat`（拒绝目录/无版本）→ 库内同版本副本复用 → 同 `(source,path,version)` 任务复用（终态任务 attempt+1 复活）→ 空间预留检查（`statfs 可用 − 队列预留 − minFreeBytes`）→ 插入 queued → 唤醒 Worker。

Worker（单并发槽）：`queued → downloading → verifying → publishing → completed`；
queued/downloading/verifying 可转 cancelled/failed；**publishing 不接受取消（409）**。

- 临时目录 `.incoming/<job-id>/<attempt>` 0700；命令固定：
  `rclone copyto --config <cfg> --use-json-log --stats 1s --stats-log-level NOTICE <remote:root/path> <绝对临时文件>`，仅参数数组、固定环境变量。
- 进度：解析 stderr JSONL 的 `Transferred: a / b`（人读单位换算），每秒至多持久化一次。
- 取消：持久化 `cancel_requested` → SIGTERM 进程组 → 等 5s → SIGKILL → wait → 清理；互斥锁串行化，取消完成后绝不发布。
- 校验：本地流式 SHA-256；远端提供可比对 checksum 时必须匹配（本地 SHA-256 只能对上远端 sha256），再刷新 Stat 比对版本指纹 → `REMOTE_CHANGED`。
- 发布：先持久化 `publishing + final_rel + local_sha256`，再 fsync 临时文件、`os.Root.Rename`、fsync 父目录，最后单事务 upsert `pending_index` + `completed`。目标 `media/<source>/<manual|schedule>/<job-id>/<attempt>/<basename>`，更新从不覆盖旧版。
- 恢复（`cmd/server/main.go recoverState`）：publishing 补完或隔离到 `.incoming/orphan-*`；downloading/verifying → `failed/INTERRUPTED`；queued 保留；孤儿 `.incoming` 清理；库文件缺失置 `missing`；`deleting` 重试删除。

### 3.3 schedule（gdrive 每日下行）

```go
// internal/schedule
func (r *Runner) Start() / Stop()
func (r *Runner) Update(ctx, path string, enabled bool, time, timezone string) error  // 校验 + revision++
func (r *Runner) check()   // 每 15s；slot 逻辑见下
```

- 单行 `gdrive_schedule`（id=1）；`last_slot` 为本地日历日（YYYY-MM-DD），**每日槽最多一次**；DST 折叠不重复（槽键是日历日不是钟点）；错过一天启动后补跑一次、不回补多日。
- 改配置/停用 → `revision+1` 并 cancel 在途扫描；已入队任务不撤销。
- 扫描逐层 List、逐文件 `Enqueue(origin=schedule)`；不读正文、不越界、不整目录 copy。
- 手动与计划共用同一下载校验发布链路；同版本去重由 Enqueue 复用完成。

### 3.4 jellyfin（索引/播放/删除保护）

```go
// internal/jellyfin
func (c *Client) SystemInfo(ctx) (*PublicInfo, error)              // 真实 Id/Version/StartupWizardCompleted
func (c *Client) StartRefresh(ctx) error                           // 204 只代表开始
func (c *Client) AllItems(ctx) ([]MediaItem, error)                // /Items?parentId&recursive&fields=Path,MediaSources 分页
func (c *Client) PlaybackConflict(ctx, absPath string) (bool, error) // /Sessions 全量，Path/MediaSources.Path 精确匹配
func (c *Client) PlaybackURL(appOrigin, itemID, serverID string) *string
```

- 头 `Authorization: MediaBrowser Token="<key>"`，15s 超时。
- 索引器每 10s 合并处理 `pending_index`：Jellyfin `Path`/`MediaSources.Path` == `/media/<relative_path>` **精确匹配**；恰好一条 → 存 item/server id 置 `playable`；零条等待；多条 → `INDEX_CONFLICT` 不猜 ID。
- 删除（API 层同步完成）：per-file 互斥锁 → `PlaybackConflict`（播放或暂停即 409 `FILE_IN_USE`；查询失败 503 `JELLYFIN_UNAVAILABLE`）→ 持久化 `deleting` → `os.Root.Remove` → `deleted` → 触发刷新 → 204。`deleting` 只是崩溃恢复标记。
- 播放链接 = `webURLs[可信应用origin] + #/details?id=<itemID>&serverId=<serverID>`；进入 Jellyfin 由播放用户独立登录，应用不做播放器/代理。

### 3.5 storage（SQLite）

- 单连接、WAL、`foreign_keys`、`busy_timeout=5000`；DB/WAL/SHM 全在 `stateDir`；`PRAGMA user_version` 单事务迁移。
- `stateDir/.lock` 非阻塞 flock；第二实例启动即失败。
- 表：`download_jobs`（UNIQUE(source,path,version)）、`library_files`（UNIQUE(job_id,attempt)、UNIQUE(relative_path)）、`gdrive_schedule`（单行 id=1）、`sessions`（token 哈希 + csrf + 12h 绝对过期）。
- 关键方法：`ClaimNextQueued`（事务抢占）、`CompletePublish`（单事务：upsert pending_index + job completed）、`ReservedBytes`（空间预留）、`MarkInterruptedRuns`。

### 3.6 api（HTTP 面）

| 路由 | 说明 |
|---|---|
| `GET /healthz` | 公共；进程就绪 + DB ping |
| `POST /api/auth/login` | Origin 精确匹配 + 仅 JSON；成功返回 `{user,csrfToken}` + `hsa_session` cookie |
| `GET /api/auth/session` / `POST /api/auth/logout` | 会话查询/登出 |
| `GET /api/sources[?refresh=true]` | 来源健康 |
| `GET /api/entries?source&path&cursor&refresh` / `GET /api/entry` | 目录/详情（LocalState 富化） |
| `POST /api/downloads`（202 新建 / 200 已存在）、`GET /api/downloads`、`GET /api/downloads/{id}`、`POST .../cancel`（200/202）、`POST .../retry`（202） | 任务 |
| `GET /api/library?cursor&source`、`DELETE /api/library/{id}`（同步 204） | 本地库 |
| `GET/PUT /api/schedules/gdrive` | 计划（PUT 必填 time/timezone，非法 400） |
| `/api/*` 其他 | JSON 404，绝不落 SPA |
| `/assets/*` | 命中→immutable 缓存；缺失→404；其余 GET→index.html（no-store） |

- 中间件：64 KiB body 上限；Host 必须精确匹配配置 origin；会话 401；非 GET 需 `X-CSRF-Token`（会话独立随机值）。
- 错误统一 `{error:{code,message}}`，message 固定中文文案，不含上游原始文本；上游错误映射为 `UPSTREAM_ERROR/UPSTREAM_FORBIDDEN/UPSTREAM_UNAVAILABLE` 等类别（`internal/model/errors.go`）。

## 4. 关键模型与不变量

- `Source` 仅 `aliyun|gdrive`；remote 名固定 `aliyun`/`gdrive-ro`，请求不可指定。
- `Entry.version` = SHA-256(canonical JSON{source,path,size,modified,upstreamID,bestHash})；仅有 size 无 modified/hash → `VERSION_UNAVAILABLE`，不假定上游 ID 是内容版本。
- 客户端路径：拒绝绝对路径、NUL、反斜杠、`.`/`..`、重复分隔符（`model.ValidateClientPath`）；本地文件操作全部经 `os.OpenRoot(filesDir)`，拒绝 symlink/非普通文件。
- 空间：`可用 − (queued+downloading+verifying 的 bytes_total−bytes_done) − minFreeBytes`；不足 409 `NO_SPACE`；**从不自动删除媒体**。
- 会话：32B 随机 token，DB 只存 SHA-256；cookie HttpOnly/SameSite=Strict/Path=/；HTTPS origin 才置 Secure。管理员密码只在启动时读 0600 文件并保留 SHA-256。
- 启动顺序：验证配置 → 进程锁 → 迁移 → 恢复 → 装配上游/Worker/调度/索引器 → HTTP；SIGTERM/SIGINT 停接单 → 终止并等待 rclone → 关 HTTP/DB。
