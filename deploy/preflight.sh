#!/bin/sh
set -eu

echo "== 身份与主机 =="
whoami
hostname
echo "== 系统与架构 =="
uname -a
cat /etc/os-release
echo "== systemd =="
command -v systemctl
echo "== 资源 =="
df -h /
free -h 2>/dev/null || true
echo "== 端口 17891 =="
ss -lnt 2>/dev/null | grep 17891 || echo "未占用"
echo "== 已有安装 =="
systemctl status github-notes-archiver --no-pager 2>/dev/null || echo "未安装"
