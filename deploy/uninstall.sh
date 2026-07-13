#!/bin/sh
set -eu

PURGE_DATA=${PURGE_DATA:-0}
if [ "$(id -u)" -ne 0 ]; then
  echo "请使用 root 执行此脚本" >&2
  exit 1
fi

systemctl stop github-notes-archiver.service 2>/dev/null || true
systemctl disable github-notes-archiver.service 2>/dev/null || true
rm -f /etc/systemd/system/github-notes-archiver.service
systemctl daemon-reload
rm -rf /opt/github-notes-archiver

if [ "$PURGE_DATA" = "1" ]; then
  echo "正在彻底删除本地仓库、私钥、配置和日志；GitHub 远端 Deploy Key 不会被自动删除。"
  rm -rf /var/lib/github-notes-archiver /var/log/github-notes-archiver /etc/github-notes-archiver
  userdel github-notes-archiver 2>/dev/null || true
else
  echo "程序已卸载；数据、私钥、配置和日志已保留。"
fi
