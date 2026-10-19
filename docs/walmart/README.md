# Higress 企业私有化源码镜像构建

**① 准备基础镜像及离线文件 → ② 准备源码与 Dockerfile → ③ 执行构建、检查和推送。**

从 `/home/teaho/IdeaProjects/teaho-infra/higress` 执行，目标为 **Linux/amd64**。企业构建过程只需要访问企业 Harbor 和公司 Go 模块代理；访问阿里、GitHub、Go 下载站和 apt 源的准备工作全部放在有外网的中转机。

| 部署角色 | 源码编译产物 | 新 Dockerfile / target | 目标镜像 |
| --- | --- | --- | --- |
| higress-core | `cmd/higress` → `higress` | [Dockerfile.walmart.core](../../Dockerfile.walmart.core) / `higress-core` | `higress-core:2.2.5-walmart` |
| discovery | Istio `pilot/cmd/pilot-discovery` | 同上 / `discovery` | `discovery:2.2.5-walmart` |
| higress-gateway | `pilot-agent`、`golang-filter.so`，加离线 Envoy | [Dockerfile.walmart.gateway](../../Dockerfile.walmart.gateway) / `higress-gateway` | `higress-gateway:2.2.5-walmart` |

`higress-core` 与 `discovery` 通常是同一个 Controller Pod 的两个容器，并非三个独立 Deployment。这里使用用户指定的镜像名；原 chart 默认镜像名为 `higress`、`pilot`、`gateway`，部署时须明确替换三个镜像引用。

**构建范围与验证边界：** core、discovery、pilot-agent、Go filter 从源码编译；Envoy 使用官方 v2.2.5 的预编译文件，不重新编译 Envoy C++。2026-10-19 已按本版文件在本机实际构建三个镜像，默认入口/版本检查通过；Envoy 成功加载 Go filter，HTTP 冒烟请求返回 200。本版 Go filter 使用官方独立的 Go 1.23/bullseye 工具链，本次未复现旧方案的内部错误。**验证使用本地基础镜像和 goproxy.cn，未验证企业 Harbor/公司代理、xDS 或集群部署**。详见[本地验证记录](verification.md)。

## ① 准备基础镜像及离线文件

### 1.1 准备哪些基础镜像

| 用途 | 来源 | 企业 Harbor 建议标签 |
| --- | --- | --- |
| 三个成品镜像的运行基础 | `higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/base:2023-07-20T20-50-43-amd64` | `base-image/higress-base:2023-07-20T20-50-43-amd64` |
| core/discovery/pilot-agent 编译环境 | `higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/build-tools:release-1.19-ef344298e65eeb2d9e2d07b87eb4e715c2def613` | `base-image/higress-build-tools:release-1.19-ef344298e65eeb2d9e2d07b87eb4e715c2def613` |
| Go filter 编译环境 | `golang:1.23-bullseye` 加官方要求的 GCC/binutils | `base-image/higress-filter-builder:go1.23-bullseye` |

base/build-tools 标签分别来自根 [Makefile.core.mk](../../Makefile.core.mk) 和 [build-istio-image.sh](../../tools/hack/build-istio-image.sh)。Go filter 的环境来自 [官方 Dockerfile](../../plugins/golang-filter/Dockerfile)。根模块与 Istio 模块当前要求 Go 1.26.0，下面会离线安装该工具链，覆盖旧 build-tools 内的 Go；Go filter 使用自己的独立阶段。

本方案选择导入 **base**，不需要导入预编译的 core 业务镜像。core 成品不是通用运行基础，不能将 `BASE_IMAGE` 随意换成 core 镜像；导入 core 也不会自动获得编译工具。请按企业要求审查基础镜像，保存实际 digest；更新系统基础版本后应重新准备 `.deb` 并验证兼容性。

### 1.2 中转机：获取镜像，准备 Go filter 编译基础

在能访问外网的 Linux/amd64 中转机执行。先把源码根目录的 `Dockerfile.walmart.filter-builder` 及同名 `.dockerignore` 复制到本节工作目录。

```bash
mkdir -p "$HOME/higress-walmart-inputs"
cd "$HOME/higress-walmart-inputs"
export ALI_BASE=higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/base:2023-07-20T20-50-43-amd64
export ALI_TOOLS=higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/build-tools:release-1.19-ef344298e65eeb2d9e2d07b87eb4e715c2def613
docker pull --platform linux/amd64 "$ALI_BASE"
docker pull --platform linux/amd64 "$ALI_TOOLS"
docker pull --platform linux/amd64 golang:1.23-bullseye

# 此 Dockerfile 在中转机安装 GCC/binutils/make；企业构建时不再运行 apt。
docker build --platform linux/amd64 -f Dockerfile.walmart.filter-builder \
  --build-arg GO_IMAGE=golang:1.23-bullseye \
  -t higress-filter-builder:go1.23-bullseye .

docker image inspect "$ALI_BASE" "$ALI_TOOLS" golang:1.23-bullseye \
  higress-filter-builder:go1.23-bullseye > base-images.json
docker save -o higress-build-inputs.tar \
  "$ALI_BASE" "$ALI_TOOLS" higress-filter-builder:go1.23-bullseye
sha256sum higress-build-inputs.tar > higress-build-inputs.tar.sha256
```

Go 镜像标签可变化，须保留 `base-images.json` 中实际 ID/digest 和中转机构建日志；发布流水线使用已审查的固定 digest。这里的 filter-builder 是本步骤明确创建的工具镜像，不是要求你寻找一个不存在的官方镜像。

### 1.3 中转机：准备离线 Go、Envoy、supercronic 和系统包

继续在上述目录执行：

```bash
mkdir -p offline/runtime-debs
curl -fL --retry 3 https://go.dev/dl/go1.26.0.linux-amd64.tar.gz \
  -o offline/go1.26.0.linux-amd64.tar.gz
curl -fL --retry 3 https://github.com/higress-group/proxy/releases/download/v2.2.5/envoy-symbol-amd64.tar.gz \
  -o offline/envoy-symbol-amd64.tar.gz
curl -fL --retry 3 https://github.com/higress-group/proxy/releases/download/v2.2.0/supercronic-linux-amd64 \
  -o offline/supercronic-linux-amd64

# 使用同一个运行 base 解析依赖；仅导出 .deb，不导出 apt 的 partial/lock。
docker run --rm --platform linux/amd64 --user 0 --entrypoint /bin/bash \
  -v "$PWD/offline/runtime-debs:/offline" "$ALI_BASE" -ec '
    apt-get -o Acquire::Retries=3 update
    apt-get -o Acquire::Retries=3 install --download-only --no-install-recommends -y logrotate cron
    cp /var/cache/apt/archives/*.deb /offline/
    chmod a+r /offline/*.deb
  '

# Go 官方发布摘要；本地此前已核对。其余文件另保存传输摘要。
printf '%s\n' 'aac1b08a0fb0c4e0a7c1555beb7b59180b05dfc5a3d62e40e9de90cd42f88235  go1.26.0.linux-amd64.tar.gz' \
  > offline/go.SHA256SUMS
(cd offline && sha256sum -c go.SHA256SUMS)
(cd offline && sha256sum envoy-symbol-amd64.tar.gz supercronic-linux-amd64 \
  runtime-debs/*.deb > gateway.SHA256SUMS)
```

Envoy URL 复制自当前 `Makefile.core.mk` 的 `ENVOY_PACKAGE_URL_PATTERN`；supercronic URL 复制自 `istio/istio/pilot/docker/Dockerfile.proxyv2`，两者版本号不同是原文件的选择。`.deb` 必须来自同一 base/架构，不从宿主机随意复制。

以上容器也必须能访问对应 apt 源；若中转机需要代理，应显式给容器配置可达代理。镜像内的 `127.0.0.1` 不等于宿主机。自算摘要只检查传输完整性，不代替下载来源审查。

### 1.4 企业环境：导入并推送 Harbor

把 tar、摘要、`base-images.json` 和整个 `offline/` 目录传入企业。目标 Harbor 项目 `base-image`、`nonprod-ai-gateway` 需已创建并有权限。在收到 tar 的目录执行：

```bash
sha256sum -c higress-build-inputs.tar.sha256
docker load -i higress-build-inputs.tar
docker login harbor.qa.wal-mart.cn

export BASE_IMAGE=harbor.qa.wal-mart.cn/base-image/higress-base:2023-07-20T20-50-43-amd64
export BUILD_TOOLS_IMAGE=harbor.qa.wal-mart.cn/base-image/higress-build-tools:release-1.19-ef344298e65eeb2d9e2d07b87eb4e715c2def613
export FILTER_BUILD_IMAGE=harbor.qa.wal-mart.cn/base-image/higress-filter-builder:go1.23-bullseye

docker tag higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/base:2023-07-20T20-50-43-amd64 "$BASE_IMAGE"
docker tag higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/build-tools:release-1.19-ef344298e65eeb2d9e2d07b87eb4e715c2def613 "$BUILD_TOOLS_IMAGE"
docker tag higress-filter-builder:go1.23-bullseye "$FILTER_BUILD_IMAGE"
docker push "$BASE_IMAGE"
docker push "$BUILD_TOOLS_IMAGE"
docker push "$FILTER_BUILD_IMAGE"
```

这些命令仅从本地加载、重新打标签并连接 Harbor，不连接阿里源。Harbor 使用企业 CA 时，Docker daemon/BuildKit 必须信任该 CA；Wasm 的 `WASM_INSECURE_REGISTRIES` 不控制 Docker 构建。

## ② 准备源码与 Dockerfile

### 2.1 固定源码，确认子模块

```bash
export HIGRESS_DIR=/home/teaho/IdeaProjects/teaho-infra/higress
cd "$HIGRESS_DIR"
git rev-parse HEAD
git status --short
git submodule status --recursive
git merge-base --is-ancestor 4deae1dd3b4cb2707273748985e55bef28e747d1 HEAD
```

写文档时本地 HEAD 为 `e5e6ab188fba7e2e58bbf68e2704c4e94d8417fc`，基于 v2.2.5 的 `4deae1dd`，带有本地依赖锁定提交。本文针对这份源码，**不等同于验证了干净上游 tag**。输出镜像名的 `2.2.5-walmart` 只是标签，须同时保存实际提交号和子模块版本。

子模块状态应以空格开头；出现 `-`、`+`、`U` 时先检查缺失、版本偏移或冲突。在有源码网络的环境使用 `git submodule update --init --recursive`，再把完整源码带入企业。不要只复制不含子模块的下载 ZIP；不要让企业构建过程临时访问 GitHub。

### 2.2 文件位置

新文件已经放在本源码仓库，无需从另一个 demo 仓库复制：

```text
higress/
├── Dockerfile.walmart.core / Dockerfile.walmart.core.dockerignore
├── Dockerfile.walmart.gateway / Dockerfile.walmart.gateway.dockerignore
├── Dockerfile.walmart.filter-builder / Dockerfile.walmart.filter-builder.dockerignore
└── docs/walmart/
    ├── README.md
    ├── Makefile                 # 调度三个成品镜像
    ├── prepare-source.sh        # 准备 external/ 和版本信息
    ├── golang-filter.mk         # 原 Go filter Makefile 的附加编译目标
    ├── offline/                # 将步骤①的目录放到这里，已忽略 Git 跟踪
    └── logs/                   # 构建日志，已忽略 Git 跟踪
```

把收到的 `offline/` 放到 `$HIGRESS_DIR/docs/walmart/offline/`，然后检查：

```bash
cd "$HIGRESS_DIR"
(cd docs/walmart/offline && sha256sum -c go.SHA256SUMS && sha256sum -c gateway.SHA256SUMS)
docker run --rm --network none --entrypoint /bin/sh "$FILTER_BUILD_IMAGE" -ec \
  'go version; command -v make; command -v x86_64-linux-gnu-gcc; command -v x86_64-linux-gnu-as'
```

业务 Dockerfile 必须连同专属 `.dockerignore` 使用，企业构建目录必须包含完整源码。旧 `external/`、历史 `.so`、Wasm 产物不会进入构建；`prepare-source.sh` 在容器内从子模块生成新的 external 目录。构建所需离线文件均校验摘要。

## ③ 执行构建镜像

### 3.1 设置企业参数

在同一个 Bash 终端继续执行；若重新打开终端，请先重新设置 1.4 的三个镜像变量。

```bash
cd "$HIGRESS_DIR"
export HUB=harbor.qa.wal-mart.cn/nonprod-ai-gateway
export TAG=2.2.5-walmart
export GOPROXY=http://cno-repository.cn.wal-mart.com/repository/GoproxCn/
export SOURCE_REVISION=$(git rev-parse HEAD)
mkdir -p docs/walmart/logs
set -o pipefail
```

GOPROXY 不加 `,direct`，避免缺少模块时回退 GitHub。公司代理必须提供锁定版本和 Higress fork 的 pseudo-version。企业 HTTPS 代理若需要 CA，先加入编译基础镜像信任，不使用关闭 TLS 校验的临时绕过。`GOSUMDB=off` 沿用公司环境示例，依赖仍受已有 go.sum 检查。

### 3.2 构建三个镜像

可以一次顺序执行全部目标：

```bash
make -f docs/walmart/Makefile images \
  HUB="$HUB" TAG="$TAG" \
  BASE_IMAGE="$BASE_IMAGE" BUILD_TOOLS_IMAGE="$BUILD_TOOLS_IMAGE" \
  FILTER_BUILD_IMAGE="$FILTER_BUILD_IMAGE" GOPROXY="$GOPROXY" \
  SOURCE_REVISION="$SOURCE_REVISION" \
  2>&1 | tee docs/walmart/logs/build.log
```

也可以分步执行，变量来自上面的 `export`，三条命令分别生成对应镜像：

```bash
make -f docs/walmart/Makefile core      2>&1 | tee docs/walmart/logs/core.log
make -f docs/walmart/Makefile discovery 2>&1 | tee docs/walmart/logs/discovery.log
make -f docs/walmart/Makefile gateway   2>&1 | tee docs/walmart/logs/gateway.log
```

两种方式任选其一。core/discovery 共用编译阶段，第二个 target 通常复用缓存。`images` 不推送镜像，不改 Kubernetes。

这个入口与 `make docker-build HUB=... TAG=...` 的区别：原目标会进入官方 prebuild/子模块初始化等流程，其他网关脚本还会下载 Envoy、使用外部 Go/apt 镜像；仅设置 HUB/TAG 无法替换所有外部依赖。这里用小型 Makefile 调度 Docker，容器内仍调用官方的单二进制 Make 目标，避免嵌套 Docker 和不需要的联网初始化。

### 3.3 检查、保存记录、推送

构建成功后执行：

```bash
make -f docs/walmart/Makefile check 2>&1 | tee docs/walmart/logs/check.log
git rev-parse HEAD > docs/walmart/logs/source-revision.txt
git submodule status --recursive > docs/walmart/logs/submodules.txt
docker image inspect "$HUB/higress-core:$TAG" "$HUB/discovery:$TAG" \
  "$HUB/higress-gateway:$TAG" > docs/walmart/logs/images.json

# 检查全部通过后，再显式推送。
make -f docs/walmart/Makefile push
```

最终引用：

```text
harbor.qa.wal-mart.cn/nonprod-ai-gateway/higress-core:2.2.5-walmart
harbor.qa.wal-mart.cn/nonprod-ai-gateway/discovery:2.2.5-walmart
harbor.qa.wal-mart.cn/nonprod-ai-gateway/higress-gateway:2.2.5-walmart
```

`check` 只验证默认入口、版本和 Envoy 可执行性；Gateway 构建还验证系统包、supercronic crontab 和 `.so` 的动态库依赖。**这些检查不等于 xDS、HTTP 路由或 Go filter 业务验证**。正式替换集群镜像前，应在测试集群验证这些功能。原 chart 的仓库拼接规则可能额外插入 `/higress/`，不要直接把本例 HUB 填入 Helm values 后就假定引用一致，应检查最终渲染的三个 `image:`。

## 官方逻辑复用与复制出处

本次不修改任何原官方 Makefile/Dockerfile；新增文件的差异如下。复制的行为应随源文件版本一起审查。

| 新文件/逻辑 | 原始出处 | 复用方式或不能直接使用的原因 |
| --- | --- | --- |
| core 的源码编译 | [Makefile.core.mk](../../Makefile.core.mk)、[tools/hack/gobuild.sh](../../tools/hack/gobuild.sh) | 直接执行 `/out/higress` 目标；覆盖 `BUILD_WITH_CONTAINER=0`、输出目录等参数 |
| discovery、pilot-agent 编译 | [Istio Makefile.core.mk](../../istio/istio/Makefile.core.mk)、[gobuild.sh](../../istio/istio/common/scripts/gobuild.sh) | 直接执行 `/out/pilot-discovery`、`/out/pilot-agent`，保留官方 tags 和 ldflags |
| `prepare-source.sh` | [tools/hack/prebuild.sh](../../tools/hack/prebuild.sh) | 复制必要子模块到 external 的逻辑；不运行原 prebuild 的联网更新、`go mod tidy`，不写伪造的 `.git` |
| core 最终镜像 | [docker/Dockerfile.higress](../../docker/Dockerfile.higress) | 复制二进制路径、USER 1337、ENTRYPOINT；FROM 改成完整 Harbor 引用，COPY 从编译阶段取产物 |
| discovery 最终镜像 | [Dockerfile.pilot](../../istio/istio/pilot/docker/Dockerfile.pilot) | 复制运行布局和 USER；启动脚本直接 COPY 官方文件，不重写 |
| gateway 最终镜像 | [Dockerfile.proxyv2](../../istio/istio/pilot/docker/Dockerfile.proxyv2) | 复制二进制/共享库位置与入口；bootstrap 模板、启动/初始化脚本直接 COPY 官方文件 |
| gateway 离线依赖 | 同上，以及根 Makefile 的 Envoy URL | 在线 apt/curl 改为校验后的离线包；`dpkg -i` 安装 logrotate/cron 依赖，不执行原文件的全系统 apt upgrade；本方案用源码/离线资产摘要文件记录来源 |
| filter-builder、Go filter 编译 | [plugins/golang-filter/Dockerfile](../../plugins/golang-filter/Dockerfile) | 中转机复制 Go 1.23/bullseye 和 amd64 GCC 安装逻辑；附加 Make 目标复制 `CGO_ENABLED=1`、CC/AS、`go build -buildmode=c-shared` 命令 |
| `golang-filter.mk` 的额外入口 | [plugins/golang-filter/Makefile](../../plugins/golang-filter/Makefile) | 原 `build` 目标只调用 Docker，不适合在 Docker 内再执行；用 `make -f Makefile -f ... walmart-local-build` 增加目标，不修改原文件 |
| 企业构建调度、离线校验和 BUILDINFO | `docs/walmart/Makefile`、两个业务 Dockerfile、`prepare-source.sh` | 新增企业适配；版本文件格式沿用 gobuild.sh 支持的 BUILDINFO，不是原有官方目标 |

当前根 Makefile 的 `VERSION_PACKAGE` 指向不存在的 `pkg/cmd/lversion`；本方案传入实际的 `github.com/alibaba/higress/v2/pkg/cmd/version`，无需修改官方 Makefile。higress 二进制版本取根 VERSION 的 `v2.2.5`，镜像标签可为 `2.2.5-walmart`。

不执行官方 Go filter Dockerfile 的 `go mod tidy`，统一使用 `-mod=readonly` 保留依赖锁定。若出现缺模块、工具链最低版本或需要更新 go.mod 的错误，应先审查对应模块和公司代理，不能简单去掉只读保护或换任意新版 Go。

## 本地经验与失败定位

- 本版已完成三个镜像构建、入口检查、Go filter 加载和静态 HTTP 路由冒烟验证，证据及复现命令见[本地验证记录](verification.md)。
- 旧方案共享 Go 1.26/GCC 11 时，Go filter 分别出现 Go 编译器 `values has no location`、GCC `var-tracking.c:2504`、试用 Go 1.26.9 后的 `invalid relocation`。具体编译器缺陷未定位；本版采用原 Go filter Dockerfile 的独立工具链，已实际构建及运行通过。这证明本次工具链组合可用，不代表已定位旧组合所有异常的根因。
- 离线 apt 目录只放 `.deb`，不要把权限受限的 `partial/` 带入上下文。旧 base 上 `apt-get --no-download` 安装本地包曾报内部路径错误，因此使用已验证的 `dpkg -i` 方式。
- 官方附带的 supercronic 不支持 `-version`，本版使用已验证的 `-test /var/lib/istio/cron.txt`。

需要定位 Go filter 时，可只构建该阶段，省去 pilot-agent 和最终镜像组装：

```bash
docker build --platform linux/amd64 -f Dockerfile.walmart.gateway --target filter \
  --build-arg BASE_IMAGE="$BASE_IMAGE" \
  --build-arg BUILD_TOOLS_IMAGE="$BUILD_TOOLS_IMAGE" \
  --build-arg FILTER_BUILD_IMAGE="$FILTER_BUILD_IMAGE" \
  --build-arg GOPROXY="$GOPROXY" \
  -t higress-walmart-filter-check:local .
```

只有该步骤成功且最终 Gateway 通过 Envoy 加载/请求验证，才能判定对应环境可用。本文提供构建材料与命令，不自动推送、部署或替换运行中的服务。

脚本语法、Dockerfile 阶段引用与源码路径、文档相对链接和 Make 调度检查已通过；此前替身 Docker 检查之外，本次也完成了真实镜像构建与运行验证，详见 verification.md。
