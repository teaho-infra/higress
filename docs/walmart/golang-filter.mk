# 仅增加容器内编译入口，不修改 plugins/golang-filter/Makefile。
# 命令复制自 plugins/golang-filter/Dockerfile 的 amd64 分支。
GO ?= go
OUTPUT ?= /out/golang-filter.so
.PHONY: walmart-local-build
walmart-local-build:
	@test "$(GOARCH)" = amd64
	mkdir -p $(dir $(OUTPUT))
	CGO_ENABLED=1 GOOS=linux GOARCH=$(GOARCH) CC=x86_64-linux-gnu-gcc AS=x86_64-linux-gnu-as $(GO) build -o $(OUTPUT) -buildmode=c-shared .
