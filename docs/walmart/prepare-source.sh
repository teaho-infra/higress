#!/usr/bin/env bash
# 目录复制改编自 tools/hack/prebuild.sh；不改依赖、不生成 .git。
# 只准备离线源码目录和版本元数据；实际编译交给官方 Makefile。
set -euo pipefail
cd /src
: "${SOURCE_REVISION:?缺少源码提交号}"
: "${BUILD_VERSION:?缺少版本号}"
test ! -e external
mkdir -p external /out
for repo in api client-go pkg istio; do
  test -s "istio/$repo/go.mod"
  cp -a "istio/$repo" "external/$repo"
done
test -s envoy/go-control-plane/go.mod
cp -a envoy/go-control-plane external/go-control-plane
# 构建上下文不带 .git，使用外部传入的真实提交号，不伪造 Git 仓库。
printf '%s\n' "source_revision=$SOURCE_REVISION" "version=$BUILD_VERSION" > /out/build-info.txt
go version >> /out/build-info.txt
for module in istio.io/pkg istio.io/istio/pkg; do
  name=controller
  if [[ "$module" == istio.io/istio/pkg ]]; then name=istio; fi
  printf '%s\n' \
    "$module/version.buildVersion=$BUILD_VERSION" \
    "$module/version.buildGitRevision=$SOURCE_REVISION" \
    "$module/version.buildStatus=source-archive" > "/out/$name.buildinfo"
done
