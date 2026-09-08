.PHONY: all dev backend frontend build clean

# RouterAPI Makefile

all: build

# 开发模式 - 同时启动前后端
dev:
	@echo "启动开发模式..."
	@echo "后端: http://localhost:3000"
	@echo "前端: http://localhost:5173"
	@cd backend && go run main.go &
	@cd frontend && pnpm dev

# 仅后端
backend:
	@cd backend && go run main.go

# 仅前端
frontend:
	@cd frontend && pnpm dev

# 构建前端
build-frontend:
	@cd frontend && pnpm build

# 构建后端
build-backend:
	@cd backend && go build -o ../bin/routerapi main.go

# 构建全部
build: build-frontend build-backend
	@echo "构建完成: bin/routerapi"

# 清理
clean:
	@rm -rf bin
	@rm -rf frontend/dist
	@rm -f backend/data/routerapi.db
	@echo "清理完成"

# 安装依赖
install:
	@cd backend && go mod download
	@cd frontend && pnpm install
	@echo "依赖安装完成"
