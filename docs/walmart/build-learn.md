# Higress 零基础构建学习指南

本指南同时存放于 `higress` 与 `higress-console` 两个仓库的 `docs/walmart/`，内容一致。
命名对照：**higress = higress-core，pilot = discovery，gateway = higress-gateway**（后三个为 chart 默认镜像名，企业实际产物用前者）。

## 一、阅读建议

1. 先读本文建立全景，再按 ①准备 → ②源码 → ③构建 顺序动手；不要一上来啃根目录官方 Makefile。
2. 前置知识四件套：Docker 多阶段构建、Make 目标、Go 交叉编译与 CGO、Git submodule。
3. 分辨两类文件：带 `walmart` 前缀的是企业定制（离线化改造），其余是官方文件复用；对照表见 higress 仓库 `docs/walmart/README.md` 的「官方逻辑复用与复制出处」。
4. 动手顺序：core（最短路径）→ discovery（复用编译缓存）→ gateway（最复杂，含 Go filter 与离线 Envoy）→ console（前后端常规构建）。
5. 出错先查 higress 仓库 `docs/walmart/verification.md` 的「本地经验与失败定位」，再看 `docs/walmart/logs/` 构建日志。
6. 企业构建只访问 Harbor 和公司 Go 代理；一切外网下载都在中转机完成。

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

offline/ 内：Go 1.26 工具链、Envoy 预编译包、supercronic、logrotate/cron 的 deb、各 sha256 清单。

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

### 3.1 总体路线（higress 三镜像）

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

### 3.3 gateway（三段编译 + 离线组装）

```text
[build]  build-tools + 离线 Go 1.26 → make -C external/istio /out/pilot-agent
[filter] filter-builder 镜像（Go 1.23/bullseye，中转机预制）
  └─ make -f Makefile -f golang-filter.mk walmart-local-build → golang-filter.so
[assets] build-tools：校验 sha256 → 解包 envoy、supercronic、runtime-debs
            │ 三阶段产物 COPY
            ▼
[运行阶段] higress-base
  ├─ dpkg -i runtime-debs/*.deb          # 离线装 logrotate/cron
  ├─ COPY envoy / pilot-agent / golang-filter.so / bootstrap 模板 / 启动脚本
  ├─ 自检：envoy --version、pilot-agent version、supercronic -test、ldd 无 not found
  └─ ENTRYPOINT higress-proxy-start.sh
```

### 3.4 构建语句解读

```bash
# 三个镜像一次构建（不推送）
make -f docs/walmart/Makefile images \
  HUB=$HUB TAG=$TAG BASE_IMAGE=… BUILD_TOOLS_IMAGE=… \
  FILTER_BUILD_IMAGE=… GOPROXY=$GOPROXY SOURCE_REVISION=$(git rev-parse HEAD)

# 等价分步（core/discovery 共用编译缓存，第二个通常秒级）
make -f docs/walmart/Makefile core
make -f docs/walmart/Makefile discovery
make -f docs/walmart/Makefile gateway

# Makefile 实际执行的核心命令（以 core 为例）
docker build --platform linux/amd64 \
  --build-arg BASE_IMAGE=… BUILD_TOOLS_IMAGE=… GOPROXY=… \
  --build-arg SOURCE_REVISION=… BUILD_VERSION=$TAG \
  -f Dockerfile.walmart.core --target higress-core -t "$HUB/higress-core:$TAG" .
```

关键点：

- `--target higress-core / discovery`：同一 Dockerfile 里选不同运行阶段，产物不同镜像。
- `BUILD_WITH_CONTAINER=0`：在容器内直调官方 Make 目标，避免嵌套 Docker。
- `VERSION_PACKAGE=…/pkg/cmd/version`：覆盖官方 Makefile 指向不存在包的默认值。
- `-mod=readonly` + `GOPROXY` 不加 `,direct`：锁定 go.sum，缺模块直接报错不回源。
- `walmart-local-build`：`CGO_ENABLED=1` + 指定交叉 GCC，`go build -buildmode=c-shared` 产出 `.so`。
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
