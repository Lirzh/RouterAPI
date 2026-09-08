# RouterAPI — AI 接口路由网关

> 基于 Go + Vue3 的 AI API 统一网关：整合用户自带的各类 AI 接口密钥，统一协议、均衡分发请求、映射模型，记录调用日志供团队审计。

## 核心功能

- **统一协议** — 将 OpenAI / Anthropic / Gemini 等不同协议统一为 OpenAI 兼容格式，客户端只需对接一套 API
- **均衡分发** — 加权轮询 + 优先级路由，支持健康检查和自动故障转移
- **模型映射** — 将虚拟模型名映射到不同渠道的实际模型名，实现多渠道容灾
- **调用日志** — 记录每次请求的模型、渠道、Token 用量、延迟、状态码，供团队审计
- **API Key 管理** — 为团队成员分配独立的调用密钥，支持模型权限和过期时间控制

## 技术栈

| 层      | 技术                                           |
|---------|------------------------------------------------|
| 后端    | Go 1.22, Gin, GORM, SQLite, JWT, AES-GCM 加密  |
| 前端    | Vue 3, TypeScript, Vite, TailwindCSS v4, shadcn-vue 风格 |
| 图标    | Lucide Icons                                   |
| HTTP    | axios                                          |

## 快速开始

### 前置条件

- [Go 1.22+](https://go.dev/dl/)
- [Node.js 18+](https://nodejs.org/)
- [pnpm](https://pnpm.io/)

### 1. 安装依赖

```bash
cd backend && go mod tidy
cd ../frontend && pnpm install
```

### 2. 启动开发环境

**方式一：分别启动（推荐开发模式）**

```bash
# 终端1 - 启动后端 (http://localhost:3000)
cd backend
go run main.go

# 终端2 - 启动前端 (http://localhost:5173)
cd frontend
pnpm dev
```

前端开发服务器会自动将 `/api` 和 `/v1` 请求代理到后端。

**方式二：使用 Makefile**

```bash
make dev
```

### 3. 首次登录

打开 http://localhost:5173，使用默认管理员账号登录：
- 用户名: `admin`
- 密码: `admin123`

### 4. 配置流程

1. **添加渠道** — 在「渠道管理」页面添加上游 AI 接口密钥（选择提供商类型）
2. **设置模型映射** — 在「模型映射」页面将虚拟模型名映射到渠道的上游模型
3. **创建 API Key** — 在「API Key」页面为团队成员创建调用密钥
4. **开始调用** — 使用 API Key 通过 OpenAI 兼容接口发起请求

## API 使用

RouterAPI 提供 OpenAI 兼容的 API 接口，调用方无需关心后端使用的是哪个提供商。

### 聊天补全

```bash
curl http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ra-your-api-key" \
  -d '{
    "model": "gpt-4o",
    "messages": [
      {"role": "user", "content": "你好"}
    ]
  }'
```

### 流式调用

```bash
curl http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ra-your-api-key" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "讲个故事"}],
    "stream": true
  }'
```

### 查询可用模型

```bash
curl http://localhost:3000/v1/models \
  -H "Authorization: Bearer ra-your-api-key"
```

## 配置说明

后端配置文件 `backend/config.yaml`：

```yaml
server:
  port: 3000
  mode: debug          # debug | release
  jwt_secret: "change-me-to-a-random-secret"
  jwt_expire_hours: 48

database:
  driver: sqlite
  path: ./data/routerapi.db

frontend_dist: ../frontend/dist   # 生产模式前端文件目录

admin:
  username: admin
  password: admin123

log_retention_days: 30
```

支持环境变量覆盖：`PORT`、`GIN_MODE`、`JWT_SECRET`、`DB_PATH`

## 生产部署

```bash
# 构建前端
cd frontend && pnpm build

# 构建后端
cd backend && go build -o ../bin/routerapi main.go

# 运行 (会自动加载前端静态文件)
./bin/routerapi
```

访问 http://localhost:3000 即可使用完整应用。

## 项目结构

```
RouterAPI/
├── backend/
│   ├── main.go                    # 入口
│   ├── config.yaml                # 配置
│   ├── internal/
│   │   ├── balancer/balancer.go   # 负载均衡器
│   │   ├── handler/
│   │   │   ├── admin.go           # 管理后台 API
│   │   │   └── proxy.go           # 代理转发 API
│   │   ├── middleware/middleware.go  # 中间件
│   │   ├── model/model.go         # 数据模型
│   │   ├── provider/
│   │   │   ├── types.go           # 统一接口定义
│   │   │   ├── openai.go          # OpenAI 适配器
│   │   │   ├── anthropic.go      # Anthropic 适配器
│   │   │   ├── gemini.go          # Gemini 适配器
│   │   │   └── factory.go        # Provider 工厂
│   │   └── router/router.go       # 路由注册
│   └── pkg/
│       ├── config/config.go       # 配置加载
│       ├── crypto/crypto.go       # AES-GCM 加密
│       ├── database/database.go   # 数据库初始化
│       ├── jwt/jwt.go             # JWT 令牌
│       └── logger/logger.go      # 日志
├── frontend/
│   ├── src/
│   │   ├── components/ui/         # shadcn-vue 风格组件
│   │   ├── lib/                   # 工具函数和 API 客户端
│   │   ├── router/                # Vue Router
│   │   ├── stores/                # Pinia 状态管理
│   │   ├── views/                 # 页面
│   │   │   ├── Login.vue          # 登录
│   │   │   ├── Layout.vue         # 侧边栏布局
│   │   │   ├── Dashboard.vue      # 仪表盘
│   │   │   ├── Channels.vue       # 渠道管理
│   │   │   ├── ModelMappings.vue  # 模型映射
│   │   │   ├── ApiKeys.vue        # API Key 管理
│   │   │   └── Logs.vue           # 调用日志
│   │   ├── App.vue
│   │   ├── main.ts
│   │   └── style.css
│   ├── vite.config.ts
│   └── package.json
├── Makefile
└── README.md
```

## License

GPL-3.0
