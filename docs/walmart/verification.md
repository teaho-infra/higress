# 本地镜像构建与运行验证（2026-10-19）

**结果：三个镜像均构建成功，默认入口检查通过，Envoy 加载新编译的 Go filter 并完成 HTTP 200 冒烟请求。** 未推送镜像、未更新 minikube，未验证企业 Harbor/公司 Go 代理或 xDS/MCP 完整业务。

## 输入和产物

- 源码：`e5e6ab188fba7e2e58bbf68e2704c4e94d8417fc` 及其固定子模块，Linux/amd64。
- 本地构建目录：`/home/teaho/.cache/higress-walmart-verify-0vsw2evj/context`。
- 使用源码快照及本仓库的 `Dockerfile.walmart.*`、`docs/walmart` 构建文件；未更改业务源码。快照未包含指向目录外的五个 protobuf 生成链接，编译使用仓库中已有的生成代码。
- core/discovery/pilot-agent：Go 1.26.0，官方 build-tools；Go filter：独立 Go 1.23.12、GCC 10.2.1、glibc 2.31。
- Envoy：官方 v2.2.5 离线归档，程序输出 `919836551144ad42c41bbe25597d74c1b9cb8421/1.36.10/Clean/RELEASE/BoringSSL`。
- 模块代理覆盖为 `https://goproxy.cn`；运行基础与 build-tools 使用本地已有的同版官方镜像，没有连接企业 Harbor。

| 本地镜像 | Image ID 前缀 | 未压缩大小 | 结果 |
| --- | --- | --- | --- |
| `registry.local/higress/higress-core:2.2.5-walmart` | `c44c85208308` | 206.1 MiB | 构建、默认入口通过 |
| `registry.local/higress/discovery:2.2.5-walmart` | `cb2cf2ebaae0` | 200.7 MiB | 构建、默认入口通过 |
| `registry.local/higress/higress-gateway:2.2.5-walmart` | `2080cf07d8d1` | 1059.4 MiB | 构建、默认入口、Envoy/Go filter/HTTP 检查通过 |

Image ID 是本地镜像配置摘要，不是推送 Harbor 后的 manifest digest。Gateway 包含官方 symbol Envoy 产物。

Go filter 基础输入 `golang:1.23-bullseye` 的 amd64 manifest 为 `sha256:2015125fe4a3d97f9ba4ad55d458c2d1f025fdf9cb73d8f423a29e448d59da5e`，导入后 Image ID 为 `sha256:65659f18f44bfad796f18c5aad69017609c36e837d68f49c80fee2097758de4b`；准备后的 `higress-filter-builder:go1.23-bullseye` Image ID 前缀为 `7a7ef03570c8`。

## 本次实际执行的构建

本机 Docker daemon 未配置代理，直接拉取 Docker Hub 镜像出现 EOF/停滞；宿主机代理可达。因此使用已安装 ORAS 下载同一 amd64 镜像，校验配置、压缩层摘要及解压后的 diff_id，再转换为 Docker save 格式导入。转换脚本、镜像布局及日志保存在下面的证据目录，没有修改 Docker daemon 配置。企业中转机正常使用主指引的 `docker pull/save/load` 即可。

在快照目录准备好离线输入后，实际命令为：

```bash
cd /home/teaho/.cache/higress-walmart-verify-0vsw2evj/context
export HUB=registry.local/higress TAG=2.2.5-walmart
export BASE_IMAGE=higress/base:2023-07-20T20-50-43-amd64
export BUILD_TOOLS_IMAGE=higress/build-tools:release-1.19-ef344298e65eeb2d9e2d07b87eb4e715c2def613
export FILTER_BUILD_IMAGE=higress-filter-builder:go1.23-bullseye
export GOPROXY=https://goproxy.cn
export SOURCE_REVISION=e5e6ab188fba7e2e58bbf68e2704c4e94d8417fc

# 中转准备：本机代理监听 127.0.0.1:7890，所以这里使用 host 网络。
docker build --platform linux/amd64 --network host \
  -f Dockerfile.walmart.filter-builder \
  --build-arg GO_IMAGE=golang:1.23-bullseye \
  --build-arg http_proxy=http://127.0.0.1:7890 \
  --build-arg https_proxy=http://127.0.0.1:7890 \
  -t "$FILTER_BUILD_IMAGE" .

make -f docs/walmart/Makefile core discovery
make -f docs/walmart/Makefile gateway
make -f docs/walmart/Makefile check
```

上述构建和检查退出码均为 0。代理地址只适用于本机中转步骤；企业 Dockerfile 的编译过程没有访问外部 apt/GitHub/阿里仓库。

## Envoy 加载共享库及 HTTP 验证

配置文件为 [envoy-smoke.yaml](envoy-smoke.yaml)：加载最终镜像内的 `/var/lib/istio/envoy/golang-filter.so`，选择 `mcp-session`，然后通过静态 direct_response 路由返回固定内容。此配置用于验证动态库加载和过滤器回调，不验证 MCP 会话、Redis、上游模型或 xDS。

在源码根目录、已存在对应镜像的环境可按以下命令复现：

```bash
export IMAGE=registry.local/higress/higress-gateway:2.2.5-walmart
export SMOKE_CONFIG="$PWD/docs/walmart/envoy-smoke.yaml"
docker run --rm --network none \
  -v "$SMOKE_CONFIG:/etc/envoy-smoke.yaml:ro" \
  --entrypoint /usr/local/bin/envoy "$IMAGE" \
  --mode validate -c /etc/envoy-smoke.yaml

# 子 shell 退出时自动清理临时容器；端口由 Docker 动态分配。
(
  set -eu
  SMOKE_CONTAINER="higress-walmart-smoke-$$"
  trap 'docker rm -f "$SMOKE_CONTAINER" >/dev/null 2>&1 || true' EXIT
  docker run -d --name "$SMOKE_CONTAINER" \
    -p 127.0.0.1::18080 \
    -v "$SMOKE_CONFIG:/etc/envoy-smoke.yaml:ro" \
    --entrypoint /usr/local/bin/envoy "$IMAGE" \
    -c /etc/envoy-smoke.yaml --concurrency 1 --log-level debug
  SMOKE_ADDRESS=$(docker port "$SMOKE_CONTAINER" 18080/tcp)
  test "$(curl --noproxy '*' -fsS --retry 10 --retry-connrefused --retry-delay 1 \
    --connect-timeout 2 --max-time 5 "http://$SMOKE_ADDRESS/smoke")" = 'higress-private-build-ok'
  docker logs "$SMOKE_CONTAINER"
)
```

本次自动验证用同一 YAML、相同容器参数，通过 Python HTTP 客户端禁用代理发送请求。实际结果：

```text
configuration '/etc/envoy-smoke.yaml' OK
HTTP 200
Content-Type: text/plain
body: higress-private-build-ok
```

日志中确认以下路径均执行：

```text
loading symbols from so file: /var/lib/istio/envoy/golang-filter.so
Redis configuration not provided, running without Redis
golang filter decodeHeaders
RequestURL: method=GET, scheme=http, host=127.0.0.1:32773, path=/smoke
golang filter encodeData
```

验证完成后已确认临时容器 `higress-walmart-smoke-0vsw2evj` 不存在。Envoy 输出了 Go filter API 为 work-in-progress 的标准提示，未阻止配置加载和 HTTP 请求。

## 证据和结论边界

证据目录：`/home/teaho/.cache/higress-walmart-verify-0vsw2evj`。

- `inputs.json`：源码、子模块、构建文件摘要、基础镜像信息和验证结果。
- `logs/core-discovery-build.log`、`logs/gateway-build.log`：真实构建输出。
- `logs/filter-base-oras.log`、`logs/filter-base-conversion.log`、`logs/filter-builder-build.log`、`logs/filter-toolchain.txt`：基础镜像准备和工具链版本。
- `logs/runtime-verification.log`、`logs/docker-check.log`、`logs/*-entrypoint.log`：运行检查。
- `logs/envoy-config-validate.log`、`logs/envoy-smoke.log`、`logs/http-smoke.json`、`logs/cleanup.txt`：加载、请求、清理证据。
- `logs/images.json`、`evidence.SHA256SUMS`：完整本地镜像 ID 与证据摘要。
- `verify-images.py`、`envoy-smoke.yaml`：本次实际自动验证脚本和配置。

此前共享 Go 1.26/GCC 11 环境的 Go filter 构建失败；这次恢复官方 Go filter 的独立 Go 1.23/bullseye 环境后，构建与运行均通过。该结果验证了当前组合可用，不宣称已定位旧编译器内部错误的具体缺陷。测试未覆盖 Kubernetes 控制面交互、xDS、Wasm 远程拉取、完整 MCP 业务或企业网络权限，发布前仍需在对应测试环境验证。
