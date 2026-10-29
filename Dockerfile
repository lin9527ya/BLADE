# syntax=docker/dockerfile:1
#
# 运行镜像（不在镜像里编译）：只装常用工具，放入**预编译好的 Linux 单二进制**。
# 二进制由 CI 的 binaries job 交叉编译（纯 Go、无 QEMU），按目标架构放在
# 构建上下文的 dist/<TARGETARCH>/blade。这样多架构构建时 arm64 只需模拟 apt 层，
# 不再模拟 Next/Go 编译，速度快得多。
#
# 本地手动构建镜像时，先自行准备二进制：
#   cd web && npm run build:static && cd ..
#   cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/blade ./cmd/blade
#   docker build -t blade:local .
# Pin the multi-arch base image digest so a rebuild cannot silently pick a new
# OS layer. Update this digest deliberately when refreshing the toolchain.
FROM python:3.12-slim-bookworm@sha256:e697c81897302a4c4333bade000d98038fbb21eb064f8ee1fd42d02ebcd9d337
ARG TARGETARCH
# Pin the browser toolchain: :latest made image contents non-reproducible and a
# routine rebuild could silently change the agent's browser/MCP behavior.
ARG PLAYWRIGHT_VERSION=1.64.1
ARG PLAYWRIGHT_MCP_VERSION=0.0.83
ARG PLAYWRIGHT_CLI_VERSION=0.1.22
# 移动端工具链的版本钉在这里，与 Playwright 同一纪律：不写 :latest，
# 重建镜像不会悄悄换掉 agent 手里的 adb/jadx/apktool 行为。
ARG JADX_VERSION=1.5.6
ARG APKTOOL_VERSION=2.11.1
# 上游产物校验值（升级版本时一并更新）。jadx-1.5.6.zip 由发布方在本地下载后核对。
ARG JADX_SHA256=545ea2be9c242511bc145755cf4bda2485ade42966e096f8b4d3da2a230e8974
ARG APKTOOL_SHA256=56d59c524fc764263ba8d345754d8daf55b1887818b15cd3b594f555d249e2db
# 常用工具：ripgrep / curl / vim，加一批 recon 常备件（按需增删）。
# Node 从 NodeSource 装 20.x：bookworm 自带的 apt nodejs 是 18，Playwright 要求 >=20。
# 移动端三件套：
#   adb            —— Debian bookworm 的 android-sdk-platform-tools（连真机/模拟器）
#   jadx / apktool —— 都跑在 JRE 上，装 openjdk-17-jre-headless；apktool 用上游 jar
#                     + 一个三行 wrapper（上游没有 shell 入口，只有 .bat/.jar）
# 注意：**不装 Android SDK 本体**（几十 GB），adb 只需要 platform-tools 客户端；
# 模拟器与真机在容器外，adb 通过 TCP（adb connect）连过去。
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
      adb openjdk-17-jre-headless \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# jadx（反编译）与 apktool（解包/重打包）。
#
# 两者都从上游按**固定版本 URL** 取，并校验**写死的 sha256**：镜像内容必须可复现，
# 上游替换产物或传输截断都要在构建期失败，而不是悄悄装进一个坏的工具。
# 校验值由发布方维护：升级版本时同时改 ARG 与对应 SHA256。
#
# 为什么 jadx 用包装脚本而不是 symlink：bin/jadx 靠 `dirname $0/..` 定位 lib/，
# 软链会让它解析到 /usr/local 而不是 /opt/jadx。
RUN set -eux; \
    curl -fsSL -o /tmp/jadx.zip "https://github.com/skylot/jadx/releases/download/v${JADX_VERSION}/jadx-${JADX_VERSION}.zip"; \
    echo "${JADX_SHA256}  /tmp/jadx.zip" | sha256sum -c -; \
    mkdir -p /opt/jadx; unzip -q /tmp/jadx.zip -d /opt/jadx; rm -f /tmp/jadx.zip; \
    printf '#!/bin/sh\nexec /opt/jadx/bin/jadx "$@"\n' > /usr/local/bin/jadx; \
    chmod +x /usr/local/bin/jadx; \
    curl -fsSL -o /opt/apktool.jar "https://github.com/iBotPeaches/Apktool/releases/download/v${APKTOOL_VERSION}/apktool_${APKTOOL_VERSION}.jar"; \
    echo "${APKTOOL_SHA256}  /opt/apktool.jar" | sha256sum -c -; \
    printf '#!/bin/sh\nexec java -jar /opt/apktool.jar "$@"\n' > /usr/local/bin/apktool; \
    chmod +x /usr/local/bin/apktool; \
    jadx --version; apktool --version; adb version
# 预装 Playwright MCP 与 CLI（全局），运行时不再 npx 联网下载。
# @playwright/mcp：browser MCP 直接 `npx @playwright/mcp`（已全局装好，无需 -y/@latest）。
# @playwright/cli：提供 playwright-cli，装完顺带 --help 验证可执行。
# 再装 playwright（提供浏览器管理），装完用 --with-deps 预置 chromium 及其系统依赖，
# 这样容器内 MCP/CLI 首次启动即可用，不再联网下载浏览器。
RUN npm install -g @playwright/mcp@${PLAYWRIGHT_MCP_VERSION} @playwright/cli@${PLAYWRIGHT_CLI_VERSION} playwright@${PLAYWRIGHT_VERSION} \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# 预编译好的对应架构二进制（dist/amd64/blade 或 dist/arm64/blade）
COPY dist/${TARGETARCH}/blade /app/blade
# 守护启动脚本：进程退出后按退出码决定是否重新拉起，页面一键更新靠它完成换装。
# 它同时负责把 SIGTERM 转发给 blade —— docker stop 只把信号发给 PID 1，
# 不转发的话 blade 收不到、做不了优雅关闭，10 秒后被 SIGKILL 硬杀。
COPY start.sh /app/start.sh
RUN chmod +x /app/blade /app/start.sh
COPY skills/ /app/skills/
# data/（SQLite + jwt.key）持久化点
VOLUME ["/app/data"]
EXPOSE 9898 9899
ENTRYPOINT ["/app/start.sh"]
# 容器内必须监听容器网卡，否则 docker-compose 的 127.0.0.1:9898:9898 根本到不了
# （端口映射转发到容器 IP，不是容器 loopback）。公网暴露面由宿主侧映射控制：
# docker-compose.yml 仍只发布到 127.0.0.1。
CMD ["-addr", ":9898", "-proxy", "127.0.0.1:9899"]
