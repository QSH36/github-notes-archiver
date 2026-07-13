# GitHub Notes Archiver

轻量、多仓库、可审计的真实笔记归档服务。程序将你在 GUI 中录入或导入的真实 Markdown/纯文本版本提交到选定的 GitHub 私有仓库默认分支；不会生成虚假内容、随机提交或自动填满贡献日历。

## 主要能力

- GitHub fine-grained PAT 临时发现仓库，支持单选或多选启用。
- 每个仓库独立 Ed25519 Deploy Key、工作树、队列和错误状态。
- 高级模式支持无口令 OpenSSH 私钥连接一个已知仓库。
- CSV/ZIP 历史版本预览，校验 SHA-256、路径、时间、远端 HEAD 后导入。
- 严格快进同步，不强推、不自动 rebase、不修改 `activity-notes/` 外文件。
- 回环地址 Web GUI、管理令牌、短会话、CSRF/Origin/Host 安全检查。
- Go 标准库单二进制，无 Docker、数据库、Redis、Node.js 或 Nginx。

## 五分钟开始

## 远程服务器预检

部署写入前先确认目标主机，不安装任何软件：

```bash
ssh -i 临时私钥 -p 22 root@服务器公网IP \
  'set -eu; whoami; hostname; uname -a; cat /etc/os-release; uname -m; command -v systemctl; df -h /; free -h || true; ss -lnt | grep 17891 || true'
```

确认输出中的主机名、系统、架构、磁盘和 `17891` 端口状态，再上传发布包：

```bash
scp -i 临时私钥 -P 22 \
  github-notes-archiver_1.0.0_linux_amd64.tar.gz install.sh \
  root@服务器公网IP:/root/
```

本项目不会修改云安全组、防火墙、DNS 或公网端口。

### 1. 安装

将对应架构的发布包、`install.sh` 上传到服务器同一目录：

```bash
chmod +x install.sh
VERSION=1.0.0 ARCHIVE="$PWD/github-notes-archiver_1.0.0_linux_amd64.tar.gz" \
  SHA256="$(sha256sum github-notes-archiver_1.0.0_linux_amd64.tar.gz | awk '{print $1}')" \
  ./install.sh
```

脚本支持 Ubuntu 20.04/22.04/24.04、CentOS Stream 9、Rocky/AlmaLinux 8/9，并兼容 CentOS 7 的 Git 1.8 与 systemd 219。CentOS 7 已停止维护，仅建议用于迁移过渡。脚本会自动识别 `apt-get`、`dnf` 或 `yum`。

### 2. 建立 SSH 隧道

在你的电脑执行：

```bash
ssh -L 17891:127.0.0.1:17891 root@服务器公网IP
```

保持窗口打开，浏览器访问 `http://127.0.0.1:17891`。

如需通过 HTTPS 反向代理发布，在 `/etc/github-notes-archiver/environment` 中设置精确域名白名单，例如 `GNA_TRUSTED_HOSTS=notes.example.com`。服务仍必须只监听回环地址，并由反向代理保留原始 `Host`。

### 3. 读取首次管理令牌

```bash
ssh root@服务器公网IP 'cat /var/lib/github-notes-archiver/initial-admin-token'
```

登录后先在高级连接表单保存已关联 GitHub 账号的作者姓名与邮箱，或通过后续配置接口设置。建议使用 GitHub 提供的 `noreply` 邮箱。

## GitHub 仓库接入

1. 打开“连接 GitHub”。
2. 填写 GitHub 用户名、resource owner 和 fine-grained PAT。
3. PAT 为目标仓库授予 `Metadata: read` 和 `Administration: write`。
4. 获取仓库列表，公开、fork、归档或无管理权限仓库会显示但不可选择。
5. 单选或多选私有仓库，确认 PAT 生命周期提示后启用。
6. 服务为每个仓库创建独立可写 Deploy Key，并立即清除内存中的 PAT。

重要：GitHub 官方规则规定，删除用于 API 创建 Deploy Key 的 PAT 时，相关 Deploy Key 也会被删除。服务器不保存 PAT，但该 PAT 必须继续存在于 GitHub；若撤销，请重新输入 PAT 安装密钥。

## 手工 SSH 私钥模式

高级入口只接受无口令 OpenSSH 私钥和 `git@github.com:owner/repository.git` 地址。私钥用于验证一个已知仓库，不能枚举账户仓库。账户级私钥权限可能覆盖多个仓库，优先使用每仓库 Deploy Key。

## 历史导入

CSV 必填表头：

```text
logical_path,version_at,content
```

可选 `title`。`version_at` 必须是带时区 RFC 3339，例如 `2026-07-01T09:30:00+08:00`。

ZIP 根目录必须包含 `manifest.csv`：

```text
logical_path,version_at,content_file,sha256,title
```

限制：上传包 50 MiB、解压后 100 MiB、单文件 1 MiB、单批 5000 个版本。系统拒绝路径穿越、符号链接、二进制、重复时间版本和哈希不匹配。

导入先生成预览并锁定远端 HEAD；确认前仓库发生变化时必须重新预览。真实来源时间同时写入 Git `AuthorDate` 与 `CommitDate`，但提交只会追加在现有 HEAD 后，不改写已有祖先。

## 运维命令

```bash
systemctl status github-notes-archiver --no-pager
journalctl -u github-notes-archiver -n 100 --no-pager
/opt/github-notes-archiver/current/github-notes-archiver status
/opt/github-notes-archiver/current/github-notes-archiver doctor
tail -n 100 /var/log/github-notes-archiver/app.jsonl
```

重启与停止：

```bash
systemctl restart github-notes-archiver
systemctl stop github-notes-archiver
systemctl start github-notes-archiver
```

轮换管理令牌：

```bash
systemctl stop github-notes-archiver
sudo -u github-notes-archiver /opt/github-notes-archiver/current/github-notes-archiver rotate-token
systemctl start github-notes-archiver
```

## 升级与回滚

用新 `VERSION` 和发布包再次执行 `install.sh`。脚本安装到 `/opt/github-notes-archiver/releases/<version>` 后原子切换 `current`。

手工回滚：

```bash
rm -f /opt/github-notes-archiver/current.new
ln -s /opt/github-notes-archiver/releases/上一版本 /opt/github-notes-archiver/current.new
rm -f /opt/github-notes-archiver/current
mv /opt/github-notes-archiver/current.new /opt/github-notes-archiver/current
systemctl restart github-notes-archiver
```

## 卸载

默认保留数据和凭据：

```bash
./uninstall.sh
```

彻底删除本地数据：

```bash
PURGE_DATA=1 ./uninstall.sh
```

卸载脚本不会删除 GitHub 远端 Deploy Key。请根据 GUI/配置记录的 key ID，在各仓库 `Settings → Deploy keys` 手工删除。

## 贡献显示说明

Commit 要计入 GitHub 贡献图，作者邮箱必须关联账户，仓库必须是非 fork，提交必须位于默认分支，并满足 GitHub 的仓库关系条件。私有贡献还需在个人资料开启 `Contribution settings → Private contributions`。符合条件的贡献可能延迟最多 24 小时显示。本程序不承诺固定绿色深浅、满绿或账号零风险。
