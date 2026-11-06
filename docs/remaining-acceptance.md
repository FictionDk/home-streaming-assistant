# 剩余联调步骤（验收 C/D）与当前阻塞点

截至本轮结束，代码、镜像、deb 部署（三容器 active、双入口 healthz 200、浏览器
登录、升级不覆盖凭证）均已实测通过。剩余工作是**真实云盘授权与端到端联调**，
全部需要账号持有人参与。

## 0. 当前外部状态（恢复时先核对）

- deb：`ssh deb`（密钥），三服务 active，应用镜像
  `localhost/home-streaming-assistant:acceptance-20261106T052017Z`。
- Alist（隧道 `ssh -N -L 15244:127.0.0.1:5244 deb`）：
  - admin 密码已重置为随机值：deb `~/cloud-media-staging/alist-admin-pw`（0600）。
  - 只读角色 `assistant-readonly`（scope `/aliyun-media`，权限 16640 = Webdav
    read bit8 + Check path limit bit14）与用户 `assistant`（base_path=/aliyun-media，
    密码在 `~/cloud-media-staging/alist-assistant-pw`）已创建。
  - 存储 `AliyundriveOpen`（挂载 `/aliyun-media`，web_proxy 开）**处于禁用状态，
    refresh_token/client_id/secret 是占位值，等待真实授权**。
- Jellyfin 12.4：向导未完成（`StartupWizardCompleted=false`），初始仅 loopback。
- 上游现实约束（本轮实测）：Alist 官方托管应用已失效（扫码工具报
  `AppNotExists`），阿里云盘开放平台已不受理个人开发者（仅企业），因此
  AliyundriveOpen 的可行路径是 **OpenList 团队托管的应用**：
  1. 打开 `https://api.oplist.org` → 网盘服务选 `阿里云盘 - 扫码登录 (OAuth App)`
     → 打开 `使用 OpenList 提供的参数` 开关 → `获取 Token` → 用阿里云盘 App 扫码。
- Alist admin 密码的暂存文件已随本轮清理删除；恢复时用
  `ssh deb 'podman exec alist ./alist admin set <新随机密码>'` 重设。
  `assistant` 用户密码同样需重设：admin 登录后
  `POST /api/admin/user/update` 改密码，再写入 `~/cloud-media-staging/alist-assistant-pw`。
  2. Alist 存储里：`refresh_token` 填工具返回值；**Client ID / Secret 留空**；
     `Oauth token url` 填 `https://api.oplist.org/alicloud/renewapi`；启用存储。
  3. 验证：admin 调 `/api/fs/list`（path=/aliyun-media）应返回目录。
     （Alist v3.63 驱动源码确认：`oauth_token_url` 非空且 client_id 为空时走在线刷新。）

## 1. Alist 侧收尾

- [ ] 按上节完成存储授权并启用；`/api/fs/list` 能列出挂载。
- [ ] base_path 映射核验（计划 §3）：分别以 `/` 和 `/aliyun-media` 请求同一文件，
      确定逻辑根；据此把应用配置 `aliyun.root` 设为 `/` 或 `/aliyun-media`。
- [ ] 用户指定含验收素材的目录；把 `assistant` 密码经 unshare 通道写入
      `app-config/alist-password`。
- [ ] 修改 admin 初始密码（UI 内操作）。

## 2. rclone aliyun remote（deb 上，进应用镜像容器执行）

```bash
ssh deb
PW=$(cat ~/cloud-media-staging/alist-assistant-pw)
OBSCURE=$(podman run --rm --entrypoint /usr/bin/rclone \
  localhost/home-streaming-assistant:<tag> obscure "$PW")
podman run --rm -v /srv/cloud-media/rclone:/cfg \
  --entrypoint /usr/bin/rclone \
  localhost/home-streaming-assistant:<tag> config create --config /cfg/rclone.conf \
  aliyun webdav url http://alist:5244/dav vendor other user assistant pass "$OBSCURE"
# 校验（应用网络名 cloud-media）
podman run --rm --network cloud-media -v /srv/cloud-media/rclone:/cfg \
  --entrypoint /usr/bin/rclone \
  localhost/home-streaming-assistant:<tag> lsd --config /cfg/rclone.conf aliyun:
```

## 3. Google Drive（gdrive-ro）

- [ ] 用户新建专用 Google OAuth Desktop client（OpenList/Alist 均不再内置）。
- [ ] 本机 rclone 1.75.0 新配置创建 `gdrive-ro`，scope 明确 `drive.readonly`；
      授权只在私密浏览器进行；**不覆盖/不撤销旧授权**。
- [ ] tokeninfo 核验实授 scope 仅 `https://www.googleapis.com/auth/drive.readonly`
      且 audience 匹配新 client（内存中构造请求，禁止记录 token）。
- [ ] consent 发布状态：External 需发布 Production（Testing 授权约 7 天失效，
      不算完整部署）；无法发布则记为持续运行阻塞。
- [ ] 用户选定已有非根媒体目录作为 `gdrive.root`；config 合入
      `/srv/cloud-media/rclone/rclone.conf` 时**必须保留 aliyun remote**。

## 4. Jellyfin

- [ ] 隧道 `http://127.0.0.1:18096/web/` 完成向导：管理员账号、媒体库
      Movies / `Cloud Media` / 目录 `/media`，关闭在线 metadata/图片下载器与 UPnP。
- [ ] 确认 `StartupWizardCompleted=true` 后，在 deb 运行
      `deploy/quadlet/jellyfin-publish.sh`（换双 IP 发布并重启）。
- [ ] 建独立非管理员播放用户（仅该库/播放/必要转码）。
- [ ] Dashboard → API Keys 创建 `home-streaming-assistant`，经 unshare 通道写入
      `app-config/jellyfin-api-key`；libraryID 写入 config。

## 5. 应用配置安装与重启

- [ ] `app-config/config.json` 填入：aliyun（baseURL/username/passwordFile/root）、
      gdrive.root、jellyfin（baseURL/apiKeyFile/libraryID/webURLs），经
      `podman unshare install -m 0600 -o 10001 -g 10001` 安装，restart
      `home-streaming-assistant.service`，`/api/sources` 应为 ready。

## 6. 验收清单（计划 C/D 节，全部需真实操作）

- C：两来源浏览/详情（浏览前后 `.incoming`/`media` 无新文件）；各下载小文件并
  验证状态机与字节/hash；大文件取消/重试；重启中断恢复；断网（`podman network
  disconnect cloud-media home-streaming-assistant`）失败不发布；NO_SPACE smoke；
  验收前后云端 metadata 清单比对（只读证明）。
- D：计划页设定 2 分钟后的 Asia/Shanghai 槽 → 真实触发、lastRun、逐文件 job；
  同版本不重复下载；Jellyfin 真实索引后播放按钮进正确详情页；非管理员用户播放/
  快进/暂停；播放中删除返回 `FILE_IN_USE`；停止播放后删除仅移除本地；重启后库与
  链接保持；日志/响应内存脱敏（无 token/密码/签名 URL）。

## 7. 素材要求（账号持有人提供）

每个来源：至少两层目录、一个小文件、一个可播放 MP4（H.264/AAC 优先）、一个
可取消的大文件；Google 另选一个小计划子目录。不由应用上传素材，不做云端写试探。
