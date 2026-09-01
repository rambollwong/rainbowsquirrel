GO           ?= go
PG_CONTAINER ?= rs-pg-test
PG_IMAGE     ?= postgres:16-alpine
PG_PORT      ?= 5433
TEST_PG_DSN  ?= postgres://postgres:postgres@localhost:$(PG_PORT)/postgres?sslmode=disable

.PHONY: all build vet fmt test test-race test-integration bench check \
        pg-up pg-down pg-test pg-test-only clean

all: check

# build: Compile all packages. 编译全部包。
build:
	$(GO) build ./...

# vet: Run static analysis. 静态检查。
vet:
	$(GO) vet ./...

# fmt: Check gofmt formatting (list unformatted files and fail). 检查 gofmt 格式（列出未格式化文件并失败）。
fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

# test: Unit + sqlite integration tests (PostgreSQL auto-skips without env).
# 单元 + sqlite 集成测试（PostgreSQL 无环境自动 skip）。
test:
	$(GO) test ./...

# test-race: Full test suite with -race. 全量测试（-race）。
test-race:
	$(GO) test -race ./...

# test-integration: sqlite integration tests only. 仅 sqlite 集成测试。
test-integration:
	$(GO) test -run 'TestIntegration' -v .

# bench: Run benchmarks (pure functions + cache end-to-end).
# 运行基准测试（纯函数 + cache 端到端）。
bench:
	$(GO) test -run '^$$' -bench=. -benchmem ./...

# check: CI entry (build + vet + test). CI 入口（build + vet + test）。
check: build vet test

# pg-up: Start a temporary PostgreSQL 16 container and wait until ready.
# 启动临时 PostgreSQL 16 容器并等待就绪。
pg-up:
	docker run -d --name $(PG_CONTAINER) \
		-e POSTGRES_PASSWORD=postgres \
		-e POSTGRES_DB=postgres \
		-p $(PG_PORT):5432 $(PG_IMAGE)
	@for i in $$(seq 1 30); do \
		if docker exec $(PG_CONTAINER) pg_isready -U postgres >/dev/null 2>&1; then \
			echo "PostgreSQL ready"; exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "PostgreSQL not ready in 30s" >&2; exit 1

# pg-down: Remove the temporary PostgreSQL container. 删除临时 PostgreSQL 容器。
pg-down:
	-docker rm -f $(PG_CONTAINER)

# pg-test: Full loop (pg-up → test → pg-down). 完整闭环（pg-up → 测试 → pg-down）。
pg-test: pg-up
	TEST_PG_DSN='$(TEST_PG_DSN)' $(GO) test -run 'TestPostgres' -v .; \
	status=$$?; \
	$(MAKE) pg-down; \
	exit $$status

# pg-test-only: Run PG tests only (container must already be running).
# 仅跑 PG 测试（容器须已在运行）。
pg-test-only:
	TEST_PG_DSN='$(TEST_PG_DSN)' $(GO) test -run 'TestPostgres' -v .

# clean: Clean build artifacts and the temporary container. 清理构建产物与临时容器。
clean: pg-down
	$(GO) clean
