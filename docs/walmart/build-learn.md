# Higress 零基础构建学习指南

本指南同时存放于 `higress` 与 `higress-console` 两个仓库的 `docs/walmart/`，内容一致。
命名对照：**higress = higress-core，pilot = discovery，gateway = higress-gateway**（等号左侧为 chart 默认镜像名，企业实际产物用右侧名称）。

## 一、阅读建议

1. 先读本文建立全景，再按 ①准备 → ②源码 → ③构建 顺序动手；不要一上来啃根目录官方 Makefile。
2. 前置知识四件套：Docker 多阶段构建、Make 目标、Go 交叉编译与 CGO、Git submodule。
3. 分辨两类文件：带 `walmart` 前缀的是企业定制（离线化改造），其余是官方文件复用；对照表见 higress 仓库 `docs/walmart/README.md` 的「官方逻辑复用与复制出处」。
4. 动手顺序：core（最短路径）→ discovery（复用编译缓存）→ gateway（最复杂，含 Go filter 与离线 Envoy）→ console（前后端常规构建）。
5. 出错先查 higress 仓库 `docs/walmart/README.md` 的「本地经验与失败定位」；本机实测记录见 `docs/walmart/verification.md`，原始日志在 `/home/teaho/.cache/higress-walmart-verify-0vsw2evj/logs/`。
6. 企业方案通过中转机准备外网依赖，构建时只访问 Harbor 和公司 Go 代理；本机实测使用本地基础镜像及 `https://goproxy.cn`，未验证企业 Harbor/公司代理，未推送镜像或更新集群。

## 二、构建文件结构（按镜像分组）

### higress 仓库 —— 三个控制面/网关镜像

```text
higress/
├─ docs/walmart/
│  ├─ Makefile                     构建调度入口，只调 docker build
│  ├─ prepare-source.sh            组装 external/ 源码与版本元数据
│  ├─ golang-filter.mk             Go filter 容器内编译附加目标
│  ├─ README.md                    三步主流程与官方文件出处对照
│  ├─ verification.md              本地构建与冒烟验证记录
│  ├─ envoy-smoke.yaml             Envoy 加载 filter 冒烟配置
│  └─ offline/                     离线件（不入 Git）
├─ Dockerfile.walmart.core        core+discovery 共用 Dockerfile
├─ Dockerfile.walmart.gateway     gateway 专用，四阶段组装
├─ Dockerfile.walmart.filter-builder  中转机做 filter 编译基础镜像
└─ 官方复用（只读不改）
   ├─ Makefile.core.mk            提供 /out/higress 官方目标
   ├─ tools/hack/gobuild.sh       官方 Go 编译参数来源
   ├─ plugins/golang-filter/      filter 源码与原 Dockerfile
   └─ external/istio/             pilot-discovery/agent 目标与脚本
```

offline/ 内：Go 1.26 工具链、Envoy 预编译包、supercronic、logrotate/cron 的 deb、各 sha256 清单。本机实测文件位于 `/home/teaho/.cache/higress-walmart-verify-0vsw2evj/context/docs/walmart/offline/`；当前源码目录尚未准备该目录。

### higress-console 仓库 —— console 镜像

```text
higress-console/
├─ backend/
│  ├─ Dockerfile                  temurin 21 基础，装 jar 与 mcp 工具
│  ├─ build.sh                    mvnw 打包 + docker build 一条龙
│  ├─ pom.xml                     Maven 聚合配置，声明前端插件
│  ├─ console/pom.xml             实际触发前端构建并产出 jar
│  └─ start.sh                    容器与本地启动脚本
├─ frontend/
│  └─ package.json                React(ice) 前端，build 出 build/
├─ helm/                          higress-console Chart 部署配置
└─ tools/                         chart 发布与镜像恢复校验脚本
```

## 三、构建全流程

### 3.1 企业方案总体路线（higress 三镜像）

```text
中转机（有外网）                        企业内（仅 Harbor + Go 代理）
──────────────                        ────────────────────────────
① pull base / build-tools / golang    ④ tar 导入：load + retag + push 基础镜像
        ↓                             ⑤ 完整源码(含 submodule) + offline/ 放入
② build filter-builder 镜像               docs/walmart/，sha256 校验
        ↓                                     ↓
③ 下载 Go1.26/Envoy/supercronic/deb   ⑥ export HUB/TAG/GOPROXY/SOURCE_REVISION
   打包 tar + sha256 ──带入企业──>             ↓
                                      ⑦ make images → make check → make push
```

### 3.2 higress-core 与 discovery（同一 Dockerfile 两个产物）

```text
[build 阶段] build-tools 镜像 + 离线 Go 1.26
  ├─ bash docs/walmart/prepare-source.sh        # 子模块复制成 external/，写 buildinfo
  ├─ make BUILD_WITH_CONTAINER=0 … /out/higress # 官方目标，编控制面二进制
  └─ make -C external/istio … /out/pilot-discovery  # 官方 Istio 目标
            │ COPY 二进制
            ▼
[运行阶段] higress-base 镜像
  ├─ --target higress-core → higress-core:2.2.5-walmart（ENTRYPOINT higress）
  └─ --target discovery    → discovery:2.2.5-walmart（ENTRYPOINT higress-pilot-start.sh）
```

### 3.3 gateway（重点精读：为什么分三步编译 + 逐行解读）

Dockerfile.walmart.gateway 一共 4 个阶段：**两个编译阶段 + 一个解包校验阶段 + 一个组装阶段**。

先认识最终镜像里的四个角色：

- envoy：真正转发流量的引擎（C++ 写的），官方预编译直接用，我们不重编。
- pilot-agent：envoy 的管理进程，负责拉起、监控、热更新 envoy。
- golang-filter.so：Go 写的 Envoy 插件，编译成动态库（.so），envoy 运行时加载。
- supercronic / logrotate / cron：容器内定时任务和日志轮转工具。

**为什么分阶段？（独立配置编译环境，并复用离线资产缓存）**

| 阶段 | 产物 | 工具链 | 为什么单独拆 |
| --- | --- | --- | --- |
| build | pilot-agent | Go 1.26（离线装进 build-tools） | Istio/higress 模块要求 Go 1.26；pilot-agent 是纯 Go 程序，无 C 依赖 |
| filter | golang-filter.so | Go 1.23 + amd64 GCC | .so 是 CGO 动态库，需要 C 编译器；此前共享 Go 1.26/GCC 11 环境构建失败，改用官方独立 Go 1.23/bullseye 环境后通过；旧错误根因未确定（见 verification.md） |
| assets | envoy、supercronic、deb | 无编译 | envoy 用官方预编译不重编；离线件与源码无关，独立成层后改源码重建时不必重新校验解包（Docker 层缓存提速） |
| higress-gateway | 最终镜像 | 无编译 | 只拼装二进制和系统依赖，不带任何编译器，镜像小而干净 |

一句话总结：**Go 1.26 编 pilot-agent，Go 1.23+GCC 编 Go filter，assets 只校验解包；分别管理已验证的编译环境和缓存，最后组装运行镜像。**

总览：

```text
[阶段1 build]  Go 1.26 ──────> pilot-agent ─────┐
[阶段2 filter] Go 1.23+GCC ──> golang-filter.so ─┼─> [阶段4 组装] higress-base + 自检 → 运行镜像
[阶段3 assets] 校验解包 ─────> envoy/supercronic/deb ─┘
```

**Dockerfile.walmart.gateway 逐行解读**（以下为解读示意，含省略内容，不能直接作为 Dockerfile 执行；完整以源文件为准）

阶段 1 build——编 pilot-agent：

```dockerfile
# 三个镜像变量，构建时由 make 传入
ARG BASE_IMAGE
ARG BUILD_TOOLS_IMAGE
ARG FILTER_BUILD_IMAGE
FROM ${BUILD_TOOLS_IMAGE} AS build       # 用企业 build-tools 当编译环境，本阶段命名 build
WORKDIR /tmp/toolchain
COPY docs/walmart/offline/go1.26.0.linux-amd64.tar.gz docs/walmart/offline/go.SHA256SUMS ./   # 拷入离线 Go 和校验清单
RUN sha256sum --strict -c go.SHA256SUMS  # 校验摘要，损坏/被改立即失败
  && test "$(uname -m)" = x86_64 && tar -xzf … -C /opt  # 确认 amd64 架构，解压到 /opt/go
ENV GOROOT=/opt/go PATH=/opt/go/bin:${PATH} GOTOOLCHAIN=local  # 用刚装的 Go；禁止 go 自动联网换版本
# 公司 Go 代理、源码提交号、版本号，均由 make 传入
ARG GOPROXY
ARG SOURCE_REVISION
ARG BUILD_VERSION
ENV GOFLAGS="-mod=readonly -buildvcs=false -p=4" GOOS=linux GOARCH=amd64
                    # -mod=readonly 禁止自动修改 go.mod，不保证 go.sum 不变；构建上下文无 .git 故禁读 VCS；限 4 并行；目标平台 amd64
WORKDIR /src
COPY . .                 # 整个源码进容器
RUN bash docs/walmart/prepare-source.sh  # 把子模块拼成 external/，写 build-info/buildinfo 版本元数据
RUN make -C external/istio BUILD_WITH_CONTAINER=0 BUILD_ALL=false \
      TARGET_ARCH=amd64 TARGET_OS=linux TARGET_OUT=/out TAG=… VERSION=… /out/pilot-agent
                    # 调官方 Istio Make 目标：BUILD_WITH_CONTAINER=0 在当前容器直接编（不再嵌套 Docker）；
                    # BUILD_ALL=false 只编 pilot-agent 一个；TARGET_OUT=/out 指定产物目录；TAG/VERSION 注入版本信息
```

阶段 2 filter——编 golang-filter.so：

```dockerfile
FROM ${FILTER_BUILD_IMAGE} AS filter     # 关键：换基础镜像！Go 1.23/bullseye + amd64 GCC（中转机预制）
ENV GOPROXY=… GOFLAGS="-mod=readonly …"  # 与阶段 1 一样指定代理、禁止自动修改 go.mod
WORKDIR /src/plugins/golang-filter
COPY plugins/golang-filter/ ./           # 只拷 filter 源码，不动其他文件（层缓存更稳）
COPY docs/walmart/golang-filter.mk /tmp/golang-filter.mk  # 附加编译目标，不修改官方 Makefile
RUN make -f Makefile -f /tmp/golang-filter.mk walmart-local-build \
      GOARCH=amd64 GOPROXY="$GOPROXY" OUTPUT=/out/golang-filter.so
                    # 双 -f：官方规则 + 我们的附加目标，选 walmart-local-build 这个新入口
```

golang-filter.mk 里 walmart-local-build 的逐行解读（真正干活的 6 行）：

```makefile
test "$(GOARCH)" = amd64   # 只支持 amd64，其他架构直接报错
mkdir -p $(dir $(OUTPUT))  # 建输出目录 /out
CGO_ENABLED=1              # 打开 CGO：.so 要经过 C 编译器桥接
GOOS=linux GOARCH=amd64    # 目标平台 linux/amd64
CC=x86_64-linux-gnu-gcc AS=x86_64-linux-gnu-as  # 指定 amd64 交叉编译器和汇编器
go build -o $(OUTPUT) -buildmode=c-shared .     # -buildmode=c-shared：编译成可被 envoy 动态加载的 .so
```

阶段 3 assets——校验解包离线件（不编译）：

```dockerfile
FROM ${BUILD_TOOLS_IMAGE} AS assets      # 只借现成镜像当解包工具，不装任何东西
COPY docs/walmart/offline/envoy-symbol-amd64.tar.gz docs/walmart/offline/supercronic-linux-amd64 docs/walmart/offline/gateway.SHA256SUMS ./
COPY docs/walmart/offline/runtime-debs/ ./runtime-debs/
RUN for f in envoy… supercronic… runtime-debs/*.deb; do \
      awk 从 gateway.SHA256SUMS 取该文件对应行 > /tmp/asset.sha256; \
      sha256sum --strict -c /tmp/asset.sha256; done   # 逐文件校验摘要，缺/多/内容不符都报错
    tar -xzf envoy…tar.gz; find … -name envoy; test 恰好 1 个   # 解包并确认只有一个 envoy 二进制（防呆）
    cp 到 /out/envoy /out/supercronic; chmod 0755
```

阶段 4 higress-gateway——组装运行镜像：

```dockerfile
FROM ${BASE_IMAGE} AS higress-gateway    # 换回运行基础镜像 higress-base，此时已无编译器
ENV ISTIO_META_ISTIO_PROXY_SHA=${proxy_version}  # 沿用官方代理版本元数据入口
COPY --from=assets /assets/runtime-debs/ /tmp/runtime-debs/
RUN dpkg -i /tmp/runtime-debs/*.deb      # 离线安装 logrotate/cron，不联网 apt
  && mkdir -p /var/lib/istio/envoy /var/lib/logrotate /var/log/proxy  # 官方要求的运行目录
COPY --from=assets /out/envoy /usr/local/bin/envoy            # 数据面引擎（官方预编译）
COPY --from=assets /out/supercronic /usr/local/bin/supercronic  # 定时任务工具
COPY --from=assets /assets/gateway.SHA256SUMS /etc/higress-offline-assets.sha256  # 离线件清单留档镜像内
COPY --from=build /out/pilot-agent /usr/local/bin/pilot-agent   # 阶段 1 产物
COPY --from=filter /out/golang-filter.so /var/lib/istio/envoy/  # 阶段 2 产物，envoy 的加载路径
COPY --from=build /out/build-info.txt /etc/higress-source-build.txt  # 源码提交号/版本留档
COPY --from=build …envoy_bootstrap.json / …bootstrap_lite.json  # 官方 bootstrap 模板（生成 envoy 启动配置用）
COPY --from=build …higress-proxy-start.sh / …proxy-container-init.sh  # 官方启动/初始化脚本，直接 COPY 不重写
RUN chmod 0755 脚本 && bash container-init.sh \
    && envoy --version && pilot-agent version       # 构建期自检：脚本可执行、两个核心组件能跑
    && supercronic -test /var/lib/istio/cron.txt    # 校验 crontab 文件合法
    && ldd golang-filter.so 无 'not found'          # 检查 .so 动态库依赖齐全，否则构建失败
ENTRYPOINT ["/usr/local/bin/higress-proxy-start.sh"]  # 容器启动即运行网关
```

构建命令（docs/walmart/Makefile 的 gateway 目标）逐行解读：

```makefile
# BUILD_ARGS = --platform linux/amd64 + BASE/BUILD_TOOLS/GOPROXY/SOURCE_REVISION/BUILD_VERSION 五个 --build-arg
# FILTER_BUILD_IMAGE 为 gateway 额外使用的第三个基础镜像变量。
gateway: gateway-inputs  # 先检查变量和离线文件
	$(DOCKER) build $(BUILD_ARGS) \
	  --build-arg "FILTER_BUILD_IMAGE=$(FILTER_BUILD_IMAGE)" \
	  -f Dockerfile.walmart.gateway --target higress-gateway \
	  -t "$(HUB)/higress-gateway:$(TAG)" .
```

### 3.4 构建语句解读

```bash
# 三个镜像一次构建（不推送）
make -f docs/walmart/Makefile images \
  HUB=$HUB TAG=$TAG BASE_IMAGE=… BUILD_TOOLS_IMAGE=… \
  FILTER_BUILD_IMAGE=… GOPROXY=$GOPROXY SOURCE_REVISION=$(git rev-parse HEAD)

# 等价分步（须先 export 上述变量；core/discovery 共用编译缓存）
make -f docs/walmart/Makefile core
make -f docs/walmart/Makefile discovery
make -f docs/walmart/Makefile gateway

# Makefile 实际执行的核心命令（以 core 为例）
docker build --platform linux/amd64 \
  --build-arg "BASE_IMAGE=$BASE_IMAGE" \
  --build-arg "BUILD_TOOLS_IMAGE=$BUILD_TOOLS_IMAGE" \
  --build-arg "GOPROXY=$GOPROXY" \
  --build-arg "SOURCE_REVISION=$SOURCE_REVISION" \
  --build-arg "BUILD_VERSION=$TAG" \
  -f Dockerfile.walmart.core --target higress-core -t "$HUB/higress-core:$TAG" .
```

关键点：

- `--target higress-core / discovery`：同一 Dockerfile 里选不同运行阶段，产物不同镜像。
- `BUILD_WITH_CONTAINER=0`：在容器内直调官方 Make 目标，避免嵌套 Docker。
- `VERSION_PACKAGE=…/pkg/cmd/version`：覆盖官方 Makefile 指向不存在包的默认值。
- `-mod=readonly`：禁止自动修改 go.mod，不保证 go.sum 不变；依赖仍可从 GOPROXY 下载。不加 `,direct`：代理取不到模块时不回源。
- `make check`：`docker run --network none <镜像> version`，断网验证二进制与 Envoy 可用。
- `make push`：显式推送；images/check 永不推镜像。

### 3.5 console 镜像（higress-console 仓库）

```text
frontend npm run build（也可交给 Maven 自动执行）
        ↓
backend: sh build.sh
  ├─ ./mvnw clean package              # console 模块经 frontend-maven-plugin
  │                                    #   自动 npm install + build，前端打进 jar
  └─ docker build -f Dockerfile .      # temurin:21 基础，COPY jar/start.sh/mcp 工具
        ↓
higress-console:0.0.1 → helm/ Chart 部署
```

### 3.6 最终产物

```text
$HUB/higress-core:2.2.5-walmart       控制面（higress 二进制）
$HUB/discovery:2.2.5-walmart          控制面（pilot-discovery）
$HUB/higress-gateway:2.2.5-walmart    数据面（envoy + pilot-agent + Go filter）
higress-console:0.0.1                 控制台（jar 内嵌前端）
```

部署替换集群镜像前，仍需在测试集群验证 xDS、路由与 Go filter 业务功能（构建检查不覆盖）。
