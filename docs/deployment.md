# 构建与部署手册（本机 → deb）

配套架构说明见 `docs/architecture.md`；deb 机器侧的快速启动与排障见
infra-view 仓库 `docs/detail/deb/home-streaming-assistant-deployment.md`。

## 1. 本机构建

```bash
# 前端 + 后端 + 全部测试
npm --prefix web ci
npm --prefix web run build        # tsc --noEmit && vite build（产物 web/dist，被 go:embed）
go test ./...
go build -o /tmp/hsa-acceptance-server ./cmd/server

# 镜像（linux/amd64；基础镜像 digest 已锁，rclone 1.75.0 SHA256 校验后打包）
scripts/build-image.sh <tag>      # 产出 localhost/home-streaming-assistant:<tag>
```

约束：tag 每次唯一（不用 latest，不在 deb 上构建）；`Dockerfile` 运行时层
`USER 10001:10001`、`EXPOSE 8080`、内嵌 `time/tzdata`。

## 2. deb 交付

```bash
scripts/deploy-deb.sh <tag>
```

脚本行为（全部已实测）：
1. 校验本机镜像存在且 `linux/amd64`；
2. `podman pull` `deploy/images.json` 锁定的 Alist/Jellyfin 依赖并打本地别名
   （`localhost/hsa-alist:3.63.0`、`localhost/hsa-jellyfin:12.4`）；
3. 三个镜像 `podman save --format docker-archive | ssh deb 'podman load'`（pipefail）；
4. 逐一核对本机/远端镜像 ID（忽略可选 `sha256:` 前缀）；
5. 传输 `install-deb.sh`、`deploy/config.example.json`、`deploy/quadlet/*` 到
   deb `~/cloud-media-staging`（0700）并执行安装；
6. 校验三个 user unit `active`。

SSH 认证：`ssh deb`（当前为密钥）；脚本不嵌入密码、不禁用 StrictHostKeyChecking。

## 3. install-deb.sh（在 deb 上执行）

- 前提：`/srv/cloud-media` 存在且属 hek（一次性 root 初始化已做：
  `chown hek:hek /srv/cloud-media && chmod 0700` + `loginctl enable-linger hek`）。
- 目录创建与模式：`app-config`、`rclone`、`state` 为命名空间 UID/GID 10001、0700；
  `files/.incoming` 0700、`files/media` 0755；Alist 数据/日志归 hek。
- 凭证安装唯一通道：暂存文件 → `podman unshare install -m 0600 -o 10001 -g 10001`。
- **已有安装绝不覆盖** config.json、admin-password、Jellyfin key、发布 drop-in、
  SQLite 与媒体（存在性检查经 `podman unshare test -f`，因为普通 hek 无法 stat
  命名空间属主目录——这是实测踩过的坑）。
- Quadlet：`~/.config/containers/systemd/{cloud-media.network,alist.container,
  jellyfin.container,home-streaming-assistant.container}`；应用 unit 由
  `home-streaming-assistant.container.in` 渲染实际镜像全名。
- 启动：quadlet 单元是 generated，不能 `systemctl enable`；`[Install]
  WantedBy=default.target` 已保证 linger 下开机自启，因此只 `start`。

## 4. 网络/端口契约

| 容器 | 端口 | 说明 |
|---|---|---|
| home-streaming-assistant | `192.168.200.175:8080`、`100.75.124.126:8080` | 应用双入口（具体 IP，不是 0.0.0.0） |
| jellyfin | 初始 `127.0.0.1:8096` | 向导完成后用 `deploy/quadlet/jellyfin-publish.sh` 换成双 IP |
| alist | `127.0.0.1:5244` | 管理走 SSH 隧道 |

管理隧道：`ssh -N -L 15244:127.0.0.1:5244 deb`、`ssh -N -L 18096:127.0.0.1:8096 deb`。

## 5. 配置与凭证

- 应用配置 `HSA_CONFIG_FILE`（容器内 `/config/app/config.json`），模板
  `deploy/config.example.json`。来源/Jellyfin 缺配时允许登录与设置页，操作返回
  `NOT_CONFIGURED` 503，不伪造空目录。
- 管理员密码查看（调用方在 unshare 下是命名空间 root）：
  `ssh deb 'podman unshare cat /srv/cloud-media/app-config/admin-password'`
- rclone 配置：`/srv/cloud-media/rclone/rclone.conf`（专用可写；`gdrive-ro` 必须为
  `type=drive` + `scope=drive.readonly`，否则来源禁用）。
- Alist/Jellyfin 凭证同样经 unshare 通道落 `app-config/`，0600。

## 6. 已知部署坑（实测）

1. **crun 权限**：`/run/user/1000/crun` 若被 root 建过，rootless 容器全部
   `Permission denied`；需 root 一次性 `rm -rf /run/user/1000/crun` 后重建。
2. **命名空间目录 chmod**：`app-config` 等目录属命名空间 10001 后，普通 hek 的
   `chmod/stat` 会失败；一律走 `podman unshare`。
3. **generated unit 不能 enable**：改用 `start`，开机自启由 `[Install]` 段负责。
4. **Quadlet 键版本差异**：Podman 5.4.2 的 `.network` 不支持 `IPMasquerade`；
   `.container` 依赖用显式 `After=/Wants=`。
5. **升级验证**：修复后部署同一镜像不出现 "first install"，config/admin-password
   md5 不变（已实测）。
