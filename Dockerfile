# 编译与运行时分层，镜像包含真实文档渲染及中文、数学字体。
FROM golang:1.26.10-alpine AS builder
RUN apk add --no-cache git
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOMAXPROCS=2 GOMEMLIMIT=768MiB go build -p 2 -trimpath \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /hexclaw ./cmd/hexclaw

FROM debian:bookworm-slim AS render-tools
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl xz-utils gcc libc6-dev libcap-dev meson ninja-build pkg-config \
    && rm -rf /var/lib/apt/lists/*
# 与桌面已验收渲染工具使用同一版本；此镜像当前发布 linux/amd64。
RUN curl -fsSL https://github.com/jgm/pandoc/releases/download/3.9.0.2/pandoc-3.9.0.2-linux-amd64.tar.gz -o /tmp/pandoc.tar.gz \
    && echo 'a69abfababda8a56969a254b09f9553a7be89ddec00d4e0fe9fd585d71a67508  /tmp/pandoc.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/pandoc.tar.gz -C /tmp \
    && cp /tmp/pandoc-3.9.0.2/bin/pandoc /usr/local/bin/pandoc \
    && curl -fsSL https://github.com/typst/typst/releases/download/v0.13.1/typst-x86_64-unknown-linux-musl.tar.xz -o /tmp/typst.tar.xz \
    && echo '7d214bfeffc2e585dc422d1a09d2b144969421281e8c7f5d784b65fc69b5673f  /tmp/typst.tar.xz' | sha256sum -c - \
    && tar -xJf /tmp/typst.tar.xz -C /tmp \
    && cp /tmp/typst-x86_64-unknown-linux-musl/typst /usr/local/bin/typst
# Python 核验使用既有 Linux 沙箱后端；构建工具留在此层，运行层只带非 setuid 程序与运行库。
RUN curl -fsSL https://github.com/containers/bubblewrap/releases/download/v0.11.2/bubblewrap-0.11.2.tar.xz -o /tmp/bubblewrap.tar.xz \
    && echo '69abc30005d2186baf7737feacd8da35633b93cf5af38838ecff17c5f8e924f6  /tmp/bubblewrap.tar.xz' | sha256sum -c - \
    && tar -xJf /tmp/bubblewrap.tar.xz -C /tmp \
    && meson setup /tmp/bubblewrap-build /tmp/bubblewrap-0.11.2 \
        --buildtype=debugoptimized --prefix=/usr -Db_ndebug=false \
        -Dman=disabled -Dselinux=disabled -Dsupport_setuid=false -Dtests=false \
    && meson compile -C /tmp/bubblewrap-build \
    && install -o root -g root -m 0755 /tmp/bubblewrap-build/bwrap /usr/bin/bwrap \
    && test ! -u /usr/bin/bwrap \
    && /usr/bin/bwrap --help > /tmp/bubblewrap-help.txt \
    && grep -Fq -- '--disable-userns' /tmp/bubblewrap-help.txt \
    && grep -Fq -- '--assert-userns-disabled' /tmp/bubblewrap-help.txt

# MCP运行器使用固定官方二进制，下载与解包工具不进入运行层。
RUN curl -fsSL https://nodejs.org/dist/v22.23.4/node-v22.23.4-linux-x64.tar.xz -o /tmp/node.tar.xz \
    && echo '1805bcd35e6815559bd2eb97db438d3fdb0eb3a5e6a977b7c1badca76337e9ce  /tmp/node.tar.xz' | sha256sum -c - \
    && tar -xJf /tmp/node.tar.xz -C /tmp \
    && cp /tmp/node-v22.23.4-linux-x64/bin/node /usr/local/bin/node \
    && mkdir -p /usr/local/lib/node_modules \
    && cp -R /tmp/node-v22.23.4-linux-x64/lib/node_modules/npm /usr/local/lib/node_modules/ \
    && curl -fsSL https://github.com/astral-sh/uv/releases/download/0.13.6/uv-x86_64-unknown-linux-gnu.tar.gz -o /tmp/uv.tar.gz \
    && echo 'f29c3e94ffabd7e6f3ba521dba169a9b7ec12100bf3257087f0f24f6123f3645  /tmp/uv.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/uv.tar.gz -C /tmp \
    && cp /tmp/uv-x86_64-unknown-linux-gnu/uv /tmp/uv-x86_64-unknown-linux-gnu/uvx /usr/local/bin/

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates tzdata fontconfig fonts-noto-cjk fonts-dejavu-core fonts-stix \
    python3 python3-yaml python3-requests python3-sympy curl poppler-utils libcap2 antiword libstdc++6 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /hexclaw /usr/local/bin/hexclaw
COPY --from=render-tools /usr/local/bin/pandoc /usr/local/bin/typst /usr/local/bin/
COPY --from=render-tools /usr/bin/bwrap /usr/bin/bwrap
COPY --from=render-tools /usr/local/bin/node /usr/local/bin/uv /usr/local/bin/uvx /usr/local/bin/
COPY --from=render-tools /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY render/assets/reference.docx /usr/local/share/hexclaw/assets/render/reference.docx
COPY docker/entrypoint.py /usr/local/bin/hexclaw-container-entrypoint
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && mkdir -p /data/.hexclaw && chmod 700 /data/.hexclaw && chmod 755 /usr/local/bin/hexclaw-container-entrypoint
ENV HOME=/data TZ=Asia/Shanghai HEXCLAW_RESOURCE_DIR=/usr/local/share/hexclaw
WORKDIR /data
EXPOSE 16060
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD curl -fsS http://127.0.0.1:16060/health >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/hexclaw-container-entrypoint"]
CMD ["serve", "--config", "/data/.hexclaw/hexclaw.yaml"]
