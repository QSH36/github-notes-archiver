# 故障排查手册

## GUI 无法打开

```bash
systemctl status github-notes-archiver --no-pager
ss -lntp | grep 17891
curl -fsS http://127.0.0.1:17891/healthz
```

服务只监听 `127.0.0.1`。确认本机 SSH 隧道仍保持连接：

```bash
ssh -N -L 17891:127.0.0.1:17891 root@服务器公网IP
```

## 忘记管理令牌

停止服务后轮换，避免现有会话继续使用：

```bash
systemctl stop github-notes-archiver
sudo -u github-notes-archiver /opt/github-notes-archiver/current/github-notes-archiver rotate-token
systemctl start github-notes-archiver
```

## GitHub 仓库列表为空

- 确认 resource owner 与 PAT 绑定的所有者一致。
- 确认 fine-grained PAT 已选择目标仓库。
- 确认组织已经批准该 PAT。
- PAT 至少需要 `Metadata: read`；自动安装 Deploy Key 需要 `Administration: write`。

## Deploy Key 创建失败

- `403`：用户没有仓库管理员权限，或组织策略禁止 Deploy Key。
- `422`：公钥已被其他仓库使用。每个仓库必须使用独立密钥。
- 删除创建密钥所用 PAT 会连带删除该 Deploy Key；重新粘贴有效 PAT 后重试激活。

## 推送失败

查看逐仓错误：

```bash
/opt/github-notes-archiver/current/github-notes-archiver status
journalctl -u github-notes-archiver -n 200 --no-pager
```

- `non-fast-forward` 或“已分叉”：程序会停止，绝不强推。先由开发者在正常工作副本处理合并，再点击“检测仓库”。
- `Permission denied (publickey)`：Deploy Key 被删除或私钥权限错误；检查仓库 Settings 和服务器 `/var/lib/github-notes-archiver/keys/` 权限。
- 分支保护拒绝：允许该 Deploy Key 写默认分支，或改用允许写入的专用私有仓库。
- `归档目录已存在且不受本程序管理`：为该仓库配置新的空归档目录，不要覆盖原目录。

## GitHub 贡献没有显示

```bash
git log -1 --format='author=%an <%ae>%nauthor_date=%aI%ncommit_date=%cI'
```

确认：

1. 作者邮箱已关联 GitHub 账户。
2. 仓库不是 fork。
3. Commit 位于默认分支。
4. 个人资料已开启私有贡献展示。
5. 等待最多 24 小时。

## 服务器重启后服务未启动

```bash
systemctl is-enabled github-notes-archiver
systemctl enable --now github-notes-archiver
journalctl -u github-notes-archiver -b --no-pager
```

## 本地数据备份

停止服务后备份，避免拷贝过程中状态变化：

```bash
systemctl stop github-notes-archiver
tar -czf github-notes-archiver-backup-$(date +%F).tar.gz \
  /var/lib/github-notes-archiver /var/log/github-notes-archiver
systemctl start github-notes-archiver
```

备份包含 SSH 私钥，必须加密保存并限制访问。
