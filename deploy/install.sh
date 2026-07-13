#!/bin/sh
set -eu

VERSION=${VERSION:-1.0.0}
ARCHIVE=${ARCHIVE:-}
SHA256=${SHA256:-}
BASE=/opt/github-notes-archiver
RELEASE="$BASE/releases/$VERSION"
SERVICE_USER=github-notes-archiver

if [ "$(id -u)" -ne 0 ]; then
  echo "请使用 root 执行此脚本" >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "不支持的架构: $(uname -m)" >&2; exit 1 ;;
esac

if [ -z "$ARCHIVE" ]; then
  ARCHIVE="$(pwd)/github-notes-archiver_${VERSION}_linux_${ARCH}.tar.gz"
fi
if [ ! -f "$ARCHIVE" ]; then
  echo "找不到发布包: $ARCHIVE" >&2
  exit 1
fi

if command -v apt-get >/dev/null 2>&1; then
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y git openssh-client curl ca-certificates tzdata
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y git openssh-clients curl ca-certificates tzdata
elif command -v yum >/dev/null 2>&1; then
  yum install -y git openssh-clients curl ca-certificates tzdata
else
  echo "仅支持 apt-get、dnf 或 yum 系统" >&2
  exit 1
fi

if [ -n "$SHA256" ]; then
  ACTUAL=$(sha256sum "$ARCHIVE" | awk '{print $1}')
  [ "$ACTUAL" = "$SHA256" ] || { echo "SHA-256 校验失败" >&2; exit 1; }
fi

if ! getent group "$SERVICE_USER" >/dev/null 2>&1; then
  groupadd --system "$SERVICE_USER"
fi
if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  useradd --system --gid "$SERVICE_USER" --home-dir /var/lib/github-notes-archiver --shell /usr/sbin/nologin "$SERVICE_USER"
fi

install -d -m 0755 "$BASE/releases" /etc/github-notes-archiver
touch /etc/github-notes-archiver/environment
chmod 0600 /etc/github-notes-archiver/environment
install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0700 /var/lib/github-notes-archiver /var/log/github-notes-archiver
KNOWN_HOSTS_TMP=$(mktemp)
trap 'rm -f "$KNOWN_HOSTS_TMP"' EXIT
ssh-keyscan -t rsa,ecdsa,ed25519 github.com > "$KNOWN_HOSTS_TMP" 2>/dev/null
ALLOWED_FINGERPRINTS='SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s
SHA256:p2QAMXNIC1TJYWeIOttrVc98/R1BUFWu3/LiyKgUfQM
SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU'
FOUND_COUNT=0
while IFS= read -r fingerprint; do
  echo "$ALLOWED_FINGERPRINTS" | grep -Fx "$fingerprint" >/dev/null || {
    echo "github.com SSH 主机指纹不在官方允许列表: $fingerprint" >&2
    exit 1
  }
  FOUND_COUNT=$((FOUND_COUNT + 1))
done <<EOF
$(ssh-keygen -lf "$KNOWN_HOSTS_TMP" -E sha256 | awk '{print $2}')
EOF
[ "$FOUND_COUNT" -ge 2 ] || { echo "未能取得足够的 GitHub SSH 主机密钥" >&2; exit 1; }
install -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0600 "$KNOWN_HOSTS_TMP" /var/lib/github-notes-archiver/known_hosts
rm -rf "$RELEASE.tmp"
install -d -m 0755 "$RELEASE.tmp"
tar -xzf "$ARCHIVE" -C "$RELEASE.tmp"
test -f "$RELEASE.tmp/github-notes-archiver" || { echo "发布包缺少可执行文件" >&2; exit 1; }
test -f "$RELEASE.tmp/git-ssh-wrapper" || { echo "发布包缺少 Git SSH 包装器" >&2; exit 1; }
chown -R root:root "$RELEASE.tmp"
chmod 0755 "$RELEASE.tmp/github-notes-archiver" "$RELEASE.tmp/git-ssh-wrapper" "$RELEASE.tmp/uninstall.sh"
chmod 0644 "$RELEASE.tmp/github-notes-archiver.service" "$RELEASE.tmp/README.md"
if [ -d "$RELEASE" ]; then
  rm -rf "$RELEASE.tmp"
else
  mv "$RELEASE.tmp" "$RELEASE"
fi
rm -f "$BASE/current.new"
ln -s "$RELEASE" "$BASE/current.new"
rm -f "$BASE/current"
mv "$BASE/current.new" "$BASE/current"

install -m 0644 "$RELEASE/github-notes-archiver.service" /etc/systemd/system/github-notes-archiver.service
if command -v restorecon >/dev/null 2>&1; then
  restorecon -RF "$BASE" /etc/github-notes-archiver /var/lib/github-notes-archiver /var/log/github-notes-archiver
fi
systemctl daemon-reload
systemctl enable github-notes-archiver.service
if systemctl is-active --quiet github-notes-archiver.service; then
  systemctl restart github-notes-archiver.service
else
  systemctl start github-notes-archiver.service
fi
sleep 2
systemctl is-active --quiet github-notes-archiver.service

echo "安装完成: $VERSION ($ARCH)"
echo "管理令牌: /var/lib/github-notes-archiver/initial-admin-token"
echo "隧道示例: ssh -L 17891:127.0.0.1:17891 root@SERVER_IP"
