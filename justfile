# Ech0 任务入口（just，https://github.com/casey/just）——全仓唯一的 task runner。
#
# 布局：根 justfile 只放后端与全仓级 recipe，各子项目用 just 模块聚合：
#
#   just web    <recipe>   前端 web（Vue SPA，构建产物嵌进 Go 二进制）
#   just site   <recipe>   官网 / 文档站（React Router）
#   just hub    <recipe>   公共实例目录站（Vue）
#   just docker <recipe>   镜像构建与推送
#
# `just` 或 `just --list` 列根 recipe（模块显示为 `web ...` 这样的条目），
# `just --list web`（或直接 `just web`）列某个模块自己的 recipe。
# 模块 recipe 的工作目录就是模块目录，不需要手写 cd。
#
# 依赖：just + bash。Windows 装 Git for Windows（自带 Git Bash）即可。

set shell := ["bash", "-cu"]

mod web
mod site
mod hub
mod docker

# --- 构建元数据（解析期求值） ---
VERSION       := `git describe --tags --always 2>/dev/null || echo unknown`
BUILD_TIME    := `date -u +%Y-%m-%dT%H:%M:%SZ`
GIT_COMMIT    := `git rev-parse --short HEAD 2>/dev/null || echo unknown`

# 这两个变量必须是 var（不能是 const），见 internal/version/version.go。
VERSION_PKG   := "github.com/lin-snow/ech0/internal/version"
LDFLAGS       := "-X " + VERSION_PKG + ".Commit=" + GIT_COMMIT + " -X " + VERSION_PKG + ".BuildTime=" + BUILD_TIME

# mockery 仅作代码生成器，不进 go.mod（用 go run 固定版本调用），保持模块图精简。
# 版本 pin 死，保证任何机器/CI 生成结果一致，`just mocks-check` 才稳定。
MOCKERY_VERSION := env_var_or_default("MOCKERY_VERSION", "v3.7.4")

# 覆盖率过滤：mockery 生成的 mock 全 0%、Wire 生成的 wire_gen.go 几乎 0%，
# 只稀释分母，不反映人写代码的覆盖情况。
COVER_EXCLUDE := 'internal/test/mocks/|/wire_gen\.go:'

# 默认：列出所有 recipe
default:
    @just --list

# ---------------------------------------------------------------- 后端运行 ---

# 以 serve 模式运行后端（阻塞在 :6277）
run:
    ECH0_SERVER_MODE=debug go run -ldflags "{{LDFLAGS}}" ./cmd/ech0 serve

# 构建本地二进制（注入 version/commit/build-time）
build:
    go build -ldflags "{{LDFLAGS}}" -o ./bin/ech0 ./cmd/ech0

# Air 热重载运行后端（缺 Air 时自动安装）
dev:
    #!/usr/bin/env bash
    set -euo pipefail
    AIR_BIN="$(command -v air 2>/dev/null || echo "$(go env GOPATH)/bin/air")"
    if [ ! -x "$AIR_BIN" ]; then
        echo "air not found, installing..."
        just air-install
        AIR_BIN="$(go env GOPATH)/bin/air"
    fi
    ECH0_SERVER_MODE=debug "$AIR_BIN" -c .air.toml

# 安装 Air（Go 热重载工具）到 $GOPATH/bin
air-install:
    go install github.com/air-verse/air@latest

# ---------------------------------------------------------------- 后端质量 ---

# golangci-lint 检查
lint:
    golangci-lint run

# golangci-lint 格式化
fmt:
    golangci-lint fmt

# 跑 Go 测试
test:
    go test ./...

# 带竞态检测跑 Go 测试（CGO 必开：go-sqlite3 与 -race 都依赖）
test-race:
    CGO_ENABLED=1 go test -race ./...

# 覆盖率：原子计数，跑完打印 RAW（含生成代码）与 CALIBRATED（滤掉生成代码）两个口径
test-cover:
    CGO_ENABLED=1 go test -coverprofile=coverage.out -covermode=atomic ./...
    @grep -v -E '{{COVER_EXCLUDE}}' coverage.out > coverage.calibrated.out
    @printf 'RAW        (incl. generated): '; go tool cover -func=coverage.out            | tail -1 | awk '{print $NF}'
    @printf 'CALIBRATED (excl. generated): '; go tool cover -func=coverage.calibrated.out | tail -1 | awk '{print $NF}'

# ------------------------------------------------------------------ 生成物 ---

# 重新生成 testify mock（输出到 internal/test/mocks/<domain>mock）
mocks:
    go run github.com/vektra/mockery/v3@{{MOCKERY_VERSION}}

# 校验提交的 mock 与当前接口一致（CI 用）
mocks-check: mocks
    git diff --exit-code -- internal/test/mocks

# 用 Wire 生成 DI 代码
wire:
    go generate ./internal/di

# 校验 wire_gen.go 未漂移（CI 用）
wire-check: wire
    git diff --exit-code -- internal/di/wire_gen.go

# 重新生成 OpenAPI 规格（Huma type-first）到 internal/openapi/openapi.yaml
openapi:
    go run ./cmd/openapi-gen

# 校验入库的 OpenAPI 规格未漂移（CI 用）
openapi-check: openapi
    git diff --exit-code -- internal/openapi/openapi.yaml

# -------------------------------------------------------------------- 全仓 ---

# 给新增的 .go/.ts/.vue 补 SPDX/Copyright 头
spdx:
    node scripts/add-spdx-headers.mjs

# 缺 SPDX 头即失败（CI 用）
spdx-check:
    node scripts/add-spdx-headers.mjs --check

# 每一步都会跑完（不 fail-fast），最后打印汇总表。

# 提 PR 前必跑：SPDX + 后端 fmt/lint/openapi + web format/lint/style/i18n
check:
    bash scripts/check.sh

# 不提交、不打 tag、不推送；完整流程见 docs/dev/release-process.md。

# 抬 internal/version.Version 并做健全性检查（用法：just bump 4.7.5）
bump NEW_VERSION:
    #!/usr/bin/env bash
    set -euo pipefail
    SEMVER='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
    if ! echo "{{NEW_VERSION}}" | grep -Eq "$SEMVER"; then
        echo "✘ '{{NEW_VERSION}}' is not valid semver (expected X.Y.Z[-prerelease])"
        exit 1
    fi
    if [ -n "$(git status --porcelain)" ]; then
        echo "✘ Working tree dirty — commit or stash first so the bump commit is clean."
        git status --short
        exit 1
    fi
    OLD_VERSION="$(grep -E '^[[:space:]]*Version[[:space:]]*=[[:space:]]*"' internal/version/version.go \
                    | head -n1 \
                    | sed -E 's/.*"([^"]+)".*/\1/')"
    if [ -z "$OLD_VERSION" ]; then
        echo "✘ Could not extract current Version from internal/version/version.go"
        exit 1
    fi
    if [ "$OLD_VERSION" = "{{NEW_VERSION}}" ]; then
        echo "✘ Version is already $OLD_VERSION — nothing to bump."
        exit 1
    fi
    echo "→ bumping $OLD_VERSION → {{NEW_VERSION}}"
    sed -i.bak -E "s/^([[:space:]]*Version[[:space:]]*=[[:space:]]*\")[^\"]+(\")/\\1{{NEW_VERSION}}\\2/" internal/version/version.go
    rm -f internal/version/version.go.bak
    echo "→ verifying go build still succeeds..."
    go build ./... >/dev/null || { echo "✘ go build failed after bump — reverting"; git checkout -- internal/version/version.go; exit 1; }
    echo ""
    echo "✓ Version bumped. Diff:"
    git --no-pager diff -- internal/version/version.go
    echo ""
    echo "Next steps (review the diff above, then run):"
    echo ""
    echo "  # 1. Update CHANGELOG.md: rename [Unreleased] → [{{NEW_VERSION}}] - $(date -u +%Y-%m-%d), open a new empty [Unreleased]"
    echo "  # 2. Commit + tag:"
    echo "       git commit -am 'chore(release): v{{NEW_VERSION}}'"
    echo "       git tag -a v{{NEW_VERSION}} -m 'Release v{{NEW_VERSION}}'"
    echo "  # 3. Push to trigger release workflow:"
    echo "       git push origin main"
    echo "       git push origin v{{NEW_VERSION}}"
