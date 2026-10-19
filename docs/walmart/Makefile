# 从 Higress 根目录执行 make -f docs/walmart/Makefile ...。
# 此文件只调度镜像，源码编译由容器内官方 Make 规则执行。
SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := images
.NOTPARALLEL:
DOCKER ?= docker
HUB ?= harbor.qa.wal-mart.cn/nonprod-ai-gateway
TAG ?= 2.2.5-walmart
BASE_IMAGE ?=
BUILD_TOOLS_IMAGE ?=
FILTER_BUILD_IMAGE ?=
GOPROXY ?= http://cno-repository.cn.wal-mart.com/repository/GoproxCn/
SOURCE_REVISION ?= $(shell git rev-parse HEAD)
export DOCKER_BUILDKIT := 1
BUILD_ARGS = --platform linux/amd64 \
	--build-arg "BASE_IMAGE=$(BASE_IMAGE)" \
	--build-arg "BUILD_TOOLS_IMAGE=$(BUILD_TOOLS_IMAGE)" \
	--build-arg "GOPROXY=$(GOPROXY)" \
	--build-arg "SOURCE_REVISION=$(SOURCE_REVISION)" \
	--build-arg "BUILD_VERSION=$(TAG)"
.PHONY: inputs gateway-inputs images core discovery gateway check push
inputs:
	@test -s go.mod && test -s docs/walmart/prepare-source.sh || { echo '请从 Higress 源码根目录执行。' >&2; exit 1; }
	@test -n "$(BASE_IMAGE)" && test -n "$(BUILD_TOOLS_IMAGE)" && test -n "$(SOURCE_REVISION)"
	@test -s docs/walmart/offline/go1.26.0.linux-amd64.tar.gz && test -s docs/walmart/offline/go.SHA256SUMS
gateway-inputs: inputs
	@test -n "$(FILTER_BUILD_IMAGE)"
	@test -s docs/walmart/offline/envoy-symbol-amd64.tar.gz && test -s docs/walmart/offline/supercronic-linux-amd64
	@test -s docs/walmart/offline/gateway.SHA256SUMS
	@compgen -G 'docs/walmart/offline/runtime-debs/*.deb' >/dev/null
images: core discovery gateway
core: inputs
	$(DOCKER) build $(BUILD_ARGS) -f Dockerfile.walmart.core --target higress-core -t "$(HUB)/higress-core:$(TAG)" .
discovery: inputs
	$(DOCKER) build $(BUILD_ARGS) -f Dockerfile.walmart.core --target discovery -t "$(HUB)/discovery:$(TAG)" .
gateway: gateway-inputs
	$(DOCKER) build $(BUILD_ARGS) --build-arg "FILTER_BUILD_IMAGE=$(FILTER_BUILD_IMAGE)" -f Dockerfile.walmart.gateway --target higress-gateway -t "$(HUB)/higress-gateway:$(TAG)" .
check:
	$(DOCKER) run --rm --network none "$(HUB)/higress-core:$(TAG)" version
	$(DOCKER) run --rm --network none "$(HUB)/discovery:$(TAG)" version
	$(DOCKER) run --rm --network none "$(HUB)/higress-gateway:$(TAG)" version
	$(DOCKER) run --rm --network none --entrypoint /usr/local/bin/envoy "$(HUB)/higress-gateway:$(TAG)" --version
# 推送为独立操作，images/check 不会推送镜像。
push:
	$(DOCKER) push "$(HUB)/higress-core:$(TAG)"
	$(DOCKER) push "$(HUB)/discovery:$(TAG)"
	$(DOCKER) push "$(HUB)/higress-gateway:$(TAG)"
