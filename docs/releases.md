# 发布与更新

两个服务从 GitHub Releases 下载各自的程序。管理端仍只提供管理 API、SQLite 和嵌入前端；节点更新只更新 Agent，不会下载、升级或重新配置 Hysteria2 / Xray。

## 发布渠道与资产

默认仓库为 `ByteLan/proxy-server-mono`，默认更新渠道为 `main`。

| 来源 | 发布方式 | 使用场景 |
|---|---|---|
| `main` 分支 push | CI 检查通过后发布 `main-<12位commit SHA>` prerelease | 跟进主分支通过检查的构建 |
| `v*` tag | CI 检查通过后发布同名正式 release | 使用指定稳定版本 |

每个发布 tag 对应固定 commit 和完整资产集，不移动 tag、不覆盖已发布资产。`main` 是更新渠道，不是反复覆盖的 release tag。主分支 CI 尚未通过或尚未发布时，更新命令只能找到此前已发布的构建；代码已 push 不代表二进制已发布。GitHub 的 `latest` 正式 release 不包含 main 预发布，不能用它代替 main 渠道。

每次发布包含六个原始 Linux 可执行文件、独立 `install.sh`、`install-bundle.tar.gz` 和 `SHA256SUMS`。manifest 覆盖六个程序、bundle 与 bootstrap：

| Linux 目标 | 节点 Agent | 管理端 |
|---|---|---|
| amd64 / x86_64 | `proxy-server_linux_amd64` | `proxy-manage-server_linux_amd64` |
| arm64 / aarch64 | `proxy-server_linux_arm64` | `proxy-manage-server_linux_arm64` |
| armv7 / 32位 ARMv7 | `proxy-server_linux_armv7` | `proxy-manage-server_linux_armv7` |

ARMv7 的 Go 构建参数是 `GOARCH=arm GOARM=7`，不是 `GOARCH=armv7`。资产按目标架构命名；交叉编译通过不等于已在对应硬件实测。节点两个协议核心也必须单独提供正确架构的版本。

## 查看、检查与更新

两种程序有相同的更新参数：

```text
version
update [--repo owner/repo] [--channel main|stable] [--version TAG]
       [--check] [--token-file PATH] [--target PATH]
```

`--version TAG` 精确选择一个已发布 tag，覆盖渠道选择；既可指定 `main-...`，也可指定 `v...`。`--target` 只在明确要替换另一个安装路径时使用，通常不需要设置。仅支持发布的 Linux 架构；本机没有对应资产时会失败，不能把别的架构强行写入目标程序。

以下命令从已有安装位置执行。公共仓库可不提供 GitHub 凭据：

```bash
/opt/proxy-server/bin/proxy-server version
/opt/proxy-server/bin/proxy-server update --check
sudo /opt/proxy-server/bin/proxy-server update
sudo systemctl restart proxy-server
```

管理端同理：

```bash
/opt/proxy-manage-server/bin/proxy-manage-server version
/opt/proxy-manage-server/bin/proxy-manage-server update --channel stable --check
sudo /opt/proxy-manage-server/bin/proxy-manage-server update --channel stable
sudo systemctl restart proxy-manage-server
```

指定 tag 时，把示例 tag 换成仓库中实际存在的版本：

```bash
sudo /opt/proxy-server/bin/proxy-server update --version v0.1.0 --check
sudo /opt/proxy-server/bin/proxy-server update --version v0.1.0
```

`--check` 只检查候选发布，不替换文件。更新先下载同一 release 的程序与 `SHA256SUMS`，校验 SHA256 后在目标目录准备新文件，再进行原子替换，并将旧程序保留为 `<target>.previous`。下载、权限或校验失败不会用不完整程序覆盖当前程序。SHA256 校验防止损坏或资产不匹配；它不代替对发布仓库及其 CI 权限的信任。

**更新不自动重启服务。** 正在运行的进程会继续使用旧程序，直到管理员执行相应 `systemctl restart`。节点 Agent 重启也会停止并重新启动它管理的协议核心，现有代理连接可能中断；选择合适的维护时段。配置、节点身份、数据库和协议核心不会被更新命令修改。

## 私有仓库的凭据

为 `ByteLan/proxy-server-mono` 单独创建 fine-grained PAT：只选择这个仓库，授予 **Contents: Read-only**。不需要 Actions、管理权限或写权限。组织仓库若要求审批或 SSO，先完成相应授权；无访问权限有时表现为 404，不能据此认定仓库或 release 不存在。

更新命令支持 `GITHUB_TOKEN`、`GH_TOKEN` 环境变量，或 `--token-file PATH`。token 文件只包含 token 文本，并设置为 `0600`。优先使用管理员可读的临时文件或 root 私有路径，升级结束后按自己的凭据保管方式清理；不要把 token 传作命令行参数、写进 shell history、公开日志、Git 或长期的 systemd `EnvironmentFile`。

例如，管理员已将 token 安全保存到 `/root/.config/proxy-server-mono/github-token` 后：

```bash
sudo chmod 0600 /root/.config/proxy-server-mono/github-token
sudo /opt/proxy-server/bin/proxy-server update --check \
  --token-file /root/.config/proxy-server-mono/github-token
sudo /opt/proxy-server/bin/proxy-server update \
  --token-file /root/.config/proxy-server-mono/github-token
sudo systemctl restart proxy-server
```

管理端替换为 `/opt/proxy-manage-server/bin/proxy-manage-server` 和对应 unit 名。`GITHUB_TOKEN` / `GH_TOKEN` 与 `ADMIN_TOKEN` / `ENROLL_TOKEN` 无关，不能混用。

如果 token 已经通过安全交互放入当前 shell 环境，`sudo` 默认可能清掉它。可仅保留所需变量，例如 `sudo --preserve-env=GITHUB_TOKEN,GH_TOKEN /opt/proxy-server/bin/proxy-server update --check`；仅对自己信任的程序和 GitHub 凭据使用该方式，不使用保留全部环境的 `sudo -E`。不要在命令前写入 token 字面量。执行实际更新后手动重启，再清除不再需要的临时环境变量。

安装目录和二进制由 root 持有。systemd 的 `proxy-node` / `proxy-manager` 服务用户没有 `/opt/.../bin` 写权限，不能由服务进程自行升级，也不应为此改变目录所有者或放宽权限。升级由管理员手动执行，不设置常驻更新进程。

## 首次下载并初始化

独立 `install.sh` 会下载选定服务的原始二进制、`install-bundle.tar.gz` 与 `SHA256SUMS`，校验 SHA256，再调用 bundle 内的安装脚本完成初始化和 systemd 安装。**无需克隆源码或预先复制 scripts/deploy 目录**。安装 bundle 只包含两个安装脚本及两个 unit；不会将协议核心、配置或数据库打入包。bootstrap 只允许这四个普通文件，拒绝多余成员、链接和路径穿越。

目标机需要 Linux、Python 3、OpenSSL、systemd、`useradd`、`install`，首次下载下面的脚本还需要 curl。Python 和 curl 只在安装/下载时使用，不是 Go 服务的运行依赖。下载和校验在私密临时目录完成，程序退出时自动清理。较旧程序若没有 `update` 子命令，也可通过这条路径升级；已有配置由底层安装脚本保留，但安装脚本会重启服务。

### 下载 bootstrap 脚本

私库使用 Contents 只读的 fine-grained PAT。下面在交互终端静默读取 token，经环境提供给安装器；curl 的 Authorization 配置经标准输入传入，不将 token 放到命令参数或 shell history 中：

```bash
read -r -s -p 'GitHub read-only token: ' GITHUB_TOKEN
printf '\n'
export GITHUB_TOKEN
INSTALL_SCRIPT="$(mktemp /tmp/proxy-install-bootstrap.XXXXXXXX)"
printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" | \
  curl --config - --fail --silent --show-error --proto '=https' \
    --header 'Accept: application/vnd.github.raw+json' \
    'https://api.github.com/repos/ByteLan/proxy-server-mono/contents/scripts/install.sh?ref=main' \
    --output "$INSTALL_SCRIPT"
chmod 0700 "$INSTALL_SCRIPT"
```

首次脚本来自经过 HTTPS 与 GitHub 鉴权的 Contents API。若要固定脚本来源，将 `ref=main` 改为审核过的完整 commit SHA；下载的 release 资产仍单独校验。脚本不是带有独立签名的安装信任根。仓库变为公开时可省略鉴权配置，用同一 Contents API 获取脚本。

`install.sh` 的参数在 `--` 前选择仓库/渠道/版本，`--` 后传入既有安装器参数；不要传 `--binary`，它由 bootstrap 自动提供。默认仓库是 `ByteLan/proxy-server-mono`，默认渠道是 `main`：

```text
bash install.sh manager|node [--repo owner/repo] [--channel main|stable]
                [--version TAG] [--token-file PATH] -- INSTALLER_ARGUMENTS
```

### 首次安装管理端

执行上面的下载步骤后，直接完成 manager 初始化：

```bash
sudo --preserve-env=GITHUB_TOKEN bash "$INSTALL_SCRIPT" manager -- \
  --public-url https://manager.example.com
rm -f "$INSTALL_SCRIPT"
unset GITHUB_TOKEN
```

已有 TLS 文件时，追加底层安装参数：`--listen 0.0.0.0:8443 --public-url https://manager.example.com:8443 --tls-cert /path/fullchain.pem --tls-key /path/privkey.pem`，替换上面原有的 `--public-url`。否则默认仅监听 `127.0.0.1:8080`，通过已有 HTTPS 入口访问。

首次生成的 ADMIN_TOKEN / ENROLL_TOKEN 位于 `/etc/proxy-manage-server/manager.env`（0600），不会输出到终端。管理员私下读取 ADMIN_TOKEN 登录 UI，使用 ENROLL_TOKEN 接入节点。GitHub PAT 不写入该文件。管理端不会安装协议核心或处理代理流量。

### 首次安装代理节点

先按[部署指南](deployment.md#代理节点)从官方来源准备固定版本的 Hysteria2 / Xray 可执行文件、预期 SHA256 和版本字符串。将下列变量设为真实路径和已经选定的版本/hash；不要用 `latest`，不要把下载后自行计算的 hash 当成独立可信来源。两个核心必须适合该节点架构，并与目标 mihomo 客户端实际验证。

执行上面的脚本下载步骤后，静默读取管理端 enrollment key，再运行节点安装：

```bash
read -r -s -p 'Manager enrollment key: ' ENROLL_TOKEN
printf '\n'
export ENROLL_TOKEN
sudo --preserve-env=GITHUB_TOKEN,ENROLL_TOKEN bash "$INSTALL_SCRIPT" node -- \
  --manager https://manager.example.com --server node01.example.com --name node01 \
  --hy2-port 443 --vless-port 443 \
  --hysteria-binary "$HYSTERIA_BINARY" --hysteria-sha256 "$HYSTERIA_SHA256" \
  --core-version "hysteria=$HYSTERIA_VERSION" \
  --xray-binary "$XRAY_BINARY" --xray-sha256 "$XRAY_SHA256" \
  --core-version "xray=$XRAY_VERSION"
rm -f "$INSTALL_SCRIPT"
unset GITHUB_TOKEN ENROLL_TOKEN
```

这会下载 Agent 并安装本地已有的两个核心，初始化节点身份与协议配置，再启动服务。`--server` 是客户端连接的节点地址，`--manager` 是管理端 HTTPS 地址；两个 443 分别使用 UDP/TCP。私有 CA 可在 `--` 后添加 `--ca-file /path/to/ca.pem`。

若不使用环境变量，在 `--` 前添加 `--token-file /root/.config/proxy-server-mono/github-token`，文件须为普通文件且权限为 0600。使用稳定渠道把前半段改成 `manager --channel stable -- ...` 或 `node --channel stable -- ...`；固定发布用 `--version v0.1.0`（换成实际存在的 tag）。无对应 release 时安装失败，不会悄然改用其他渠道或本机编译。

`DESTDIR=/absolute/staging/path` 可以在不创建用户、不调用 systemctl、不初始化/注册节点的情况下检查文件布局；通过 sudo 运行时显式保留或传入 DESTDIR。staging 不能证明真实主机已经启动或代理协议可用。

### 手工下载资产

已经使用 GitHub CLI 的工作机也可下载指定资产，再自行安装；它不是 bootstrap 的依赖：

```bash
TAG=main-0123456789ab
ASSET=proxy-manage-server_linux_amd64
DOWNLOAD_DIR="$(mktemp -d)"
gh release download "$TAG" --repo ByteLan/proxy-server-mono \
  --pattern "$ASSET" --pattern SHA256SUMS --dir "$DOWNLOAD_DIR"
(
  cd "$DOWNLOAD_DIR"
  sha256sum --check --ignore-missing SHA256SUMS
)
chmod 0755 "$DOWNLOAD_DIR/$ASSET"
```

以上 tag 仅演示格式，要换成真实发布。确认校验输出包含所选资产且为 `OK`。同一发布包含 `install.sh`、`install-bundle.tar.gz`、六个原始二进制和 `SHA256SUMS`；manifest 覆盖前八项。私有资产下载使用 GitHub asset API，而非假定 `browser_download_url` 可匿名访问。bootstrap 对 API 请求鉴权，处理 200/302，跨域跳转不会携带 Authorization。[GitHub 资产 API](https://docs.github.com/en/rest/releases/assets#get-a-release-asset)、[GitHub CLI 下载命令](https://cli.github.com/manual/gh_release_download)。

## 升级前备份与二进制回退

管理端升级前停止服务，再备份整个实际 `DATA_DIR`，包括 SQLite 文件及 `master.key`；另存 `/etc/proxy-manage-server/manager.env`。如果用 `MASTER_KEY` 环境变量替代文件，必须一起保留对应密钥。备份路径应私密，并确认有空间容纳数据：

```bash
sudo systemctl stop proxy-manage-server
sudo cp -a /var/lib/proxy-manage-server /secure/backup/proxy-manager-before-upgrade
sudo cp -a /etc/proxy-manage-server /secure/backup/proxy-manager-config-before-upgrade
```

以上 `/secure/backup/` 是自行准备的备份位置，安装的实际 `DATA_DIR` 若不同要同步修改。随后执行 update，成功后启动服务；失败时可直接启动原程序。

每次成功替换只保留一份 `.previous`，再次升级会替换这份备份。需要回退二进制时，**先停止对应服务**，核对 `.previous` 的版本，再恢复。例如节点：

```bash
sudo systemctl stop proxy-server
sudo /opt/proxy-server/bin/proxy-server.previous version
sudo cp -p /opt/proxy-server/bin/proxy-server.previous /opt/proxy-server/bin/proxy-server
sudo systemctl start proxy-server
```

管理端使用 `/opt/proxy-manage-server/bin/proxy-manage-server.previous` 和 `proxy-manage-server` unit。这里的恢复只回退二进制，**不会回退 SQLite 数据或已经执行的数据迁移**；不要把 `.previous` 描述为数据库回滚。若新旧版本的数据格式不兼容，应在停服状态下按版本说明恢复升级前一致性备份及同一主密钥，不能只替换程序后继续尝试。

## 维护 CI

检查与构建在 GitHub runner 完成，1 GiB 目标机只下载和替换程序。CI 需要测试 Go、检查前端并构建嵌入 UI，然后交叉编译全部发布目标并生成 `SHA256SUMS`。发布 job 使用仓库 `contents: write`；清理 job 为临时产物使用 `actions: write`，启用 Release 清理时也使用 `contents: write`。下载方的 PAT 保持只读。PR 不需要发布凭据，也不发布 release。

主分支工作流应避免旧 commit 的慢构建晚于新 commit 发布后成为渠道候选；固定 commit tag 和发布前核对主分支 HEAD 用于保持来源清楚。重跑已发布版本时不覆盖资产。如果失败留下未发布 draft，先检查 draft 与资产是否完整再处理，不能上传一半就供更新命令选择。仓库应限制 main 与发布 tag 的写权限；删除或改写历史 release 会影响指定版本的可获取性。

## 临时 CI 产物与 Release 历史

Actions Artifacts 是工作流构建与发布 job 之间的临时传输，配置为 **1 天过期、仅保留本工作流最近 3 组 `proxy-linux-*` 产物**；清理只作用于这些临时 artifacts，不删除 Release。并发运行时可能短暂多于 3 组。GitHub Free 的 500 MB Actions artifact 存储额度属于这类临时存储，不是 Release 附件总量额度。

GitHub Release 附件独立保留历史，供首次安装、固定版本与回退下载；不会因为临时 artifact 过期而失效。GitHub 当前每个 Release 最多 1000 个附件、每个文件必须小于 2 GiB，未设 Release 总体大小或带宽的额外上限；实际可用性仍取决于仓库访问权限及平台政策。[GitHub Releases 官方说明](https://docs.github.com/en/repositories/releasing-projects-on-github/about-releases)。

Release 清理默认关闭。需要限制 main 构建历史时，在仓库 Actions Variables 配置以下一个或两个变量；它们是自己的保留策略，不是 GitHub 收费阈值：

| 仓库变量 | 默认值 | 用途 |
|---|---|---|
| `KEEP_MAIN_RELEASES` | `0` | 设置为至少 `2` 时限制保留的 main 预发布数量；不接受 `1` |
| `MAX_MAIN_RELEASE_MIB` | `0` | 大于零时为 main 预发布附件设置 MiB 预算，达到预算 90% 时开始清理旧版本 |

清理只针对 `main-<sha12>` 预发布，始终保护最近至少两个 main release 和本次发布 tag；正式 `v*` release 与 Git tags 不删除。因此预算不是保证可达到的硬上限，被保护版本自身较大时仍可能超出。历史 release 被清理后，即使相应 Git tag 还在，`--version` 也不能再下载它的附件；需要长期保留的版本应发布为正式 `v*` release 或自行归档。
