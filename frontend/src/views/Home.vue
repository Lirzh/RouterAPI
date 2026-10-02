<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { toggleTheme, getResolvedTheme } from '@/lib/theme'
import Button from '@/components/ui/Button.vue'
import Leaderboard from '@/components/Leaderboard.vue'
import {
  Activity,
  Coins,
  GitBranch,
  Key,
  Moon,
  ScrollText,
  Server,
  Sun,
} from 'lucide-vue-next'

const router = useRouter()
const auth = useAuthStore()
const isDark = computed(() => getResolvedTheme() === 'dark')
const isAuthenticated = computed(() => !!auth.token)

function goConsole() {
  router.push('/dashboard')
}
function goLogin() {
  router.push('/login')
}

function scrollToSection(id: string) {
  document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

const features = [
  { icon: GitBranch, title: '统一协议', desc: '将 OpenAI / Anthropic / Gemini 等异构协议统一为 OpenAI 兼容格式，客户端只需对接一套 API。' },
  { icon: Activity, title: '均衡分发', desc: '加权轮询 + 优先级路由，内置健康检查与故障自动转移，多渠道容灾。' },
  { icon: Server, title: '模型映射', desc: '把虚拟模型名映射到不同渠道的真实上游模型，切换供应商对调用方透明。' },
  { icon: Coins, title: '积分计费', desc: '按 token 定价的请求级额度预留与结算、兑换码与积分钱包，账单可审计。' },
  { icon: ScrollText, title: '调用日志', desc: '记录每次请求的模型、渠道、Token 用量、延迟与状态码，供团队复盘。' },
  { icon: Key, title: '密钥管理', desc: '为团队成员发放独立 API Key，支持模型权限、IP 白名单与过期控制。' },
]

const steps = [
  { no: '01', title: '添加供应商', desc: '选择协议、填入上游 AI 接口密钥与地址。' },
  { no: '02', title: '模型映射', desc: '把对外虚拟模型名绑定到渠道的真实上游模型。' },
  { no: '03', title: '创建 API Key', desc: '为团队成员或应用发放独立调用密钥。' },
  { no: '04', title: '开始调用', desc: '用 OpenAI 兼容接口发起请求，网关自动路由计费。' },
]

const demo = [
  "curl http://localhost:3000/v1/chat/completions \\",
  '  -H "Authorization: Bearer ra-your-api-key" \\',
  "  -d '{",
  '    "model": "gpt-4o",',
  '    "messages": [{ "role": "user", "content": "你好" }]',
  "  }'",
].join('\n')
</script>

<template>
  <div class="min-h-screen bg-background text-foreground">
    <!-- 顶部导航 -->
    <header class="sticky top-0 z-30 border-b bg-background/80 backdrop-blur">
      <div class="mx-auto max-w-6xl px-4 h-14 flex items-center justify-between">
        <a href="/" class="text-lg font-bold tracking-tight">RouterAPI</a>
        <nav class="hidden md:flex items-center gap-6 text-sm text-muted-foreground">
          <a href="#features" class="hover:text-foreground transition-colors">功能特性</a>
          <a href="#how" class="hover:text-foreground transition-colors">使用流程</a>
          <a href="#leaderboard" class="hover:text-foreground transition-colors">排行榜</a>
        </nav>
        <div class="flex items-center gap-2">
          <button
            @click="toggleTheme()"
            class="p-2 rounded-md text-muted-foreground hover:bg-accent transition-colors"
            :title="isDark ? '切换到浅色' : '切换到深色'"
          >
            <Sun v-if="isDark" class="w-4 h-4" />
            <Moon v-else class="w-4 h-4" />
          </button>
          <Button
            v-if="isAuthenticated"
            @click="goConsole"
            class="h-9 px-4 bg-primary text-primary-foreground hover:bg-primary/90"
          >进入控制台</Button>
          <Button
            v-else
            @click="goLogin"
            class="h-9 px-4 bg-primary text-primary-foreground hover:bg-primary/90"
          >登录</Button>
        </div>
      </div>
    </header>

    <!-- Hero -->
    <section class="relative overflow-hidden">
      <div class="mx-auto max-w-6xl px-4 pt-16 pb-14 md:pt-24 md:pb-20 grid grid-cols-1 lg:grid-cols-2 gap-10 items-center">
        <div>
          <span class="inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs text-muted-foreground mb-5">
            <span class="relative flex w-1.5 h-1.5"><span class="absolute inline-flex w-full h-full animate-ping rounded-full bg-green-400 opacity-75"></span><span class="relative inline-flex w-1.5 h-1.5 rounded-full bg-green-500"></span></span>
            AI 接口路由网关
          </span>
          <h1 class="text-4xl md:text-5xl font-bold leading-tight tracking-tight">
            统一接入<br />
            <span class="text-primary">海量 AI 模型</span>
          </h1>
          <p class="mt-5 text-base text-muted-foreground leading-relaxed max-w-xl">
            整合团队自有的各类 AI 接口密钥，统一协议、均衡分发、映射模型，
            用量与积分全链路可审计。一个网关，对接所有大模型。
          </p>
          <div class="mt-8 flex flex-wrap items-center gap-3">
            <Button @click="goLogin" class="h-11 px-6 bg-primary text-primary-foreground hover:bg-primary/90">立即登录</Button>
            <Button @click="scrollToSection('leaderboard')" class="h-11 px-6 border bg-card hover:bg-accent">查看排行榜</Button>
          </div>
        </div>

        <!-- 终端示例 -->
        <div class="rounded-xl border bg-card shadow-sm overflow-hidden">
          <div class="flex items-center gap-2 px-4 py-3 border-b bg-muted/50">
            <span class="w-3 h-3 rounded-full bg-red-400"></span>
            <span class="w-3 h-3 rounded-full bg-yellow-400"></span>
            <span class="w-3 h-3 rounded-full bg-green-400"></span>
            <span class="ml-2 text-xs text-muted-foreground">curl · /v1/chat/completions</span>
          </div>
          <pre class="p-5 text-[13px] leading-relaxed overflow-x-auto font-mono text-foreground"><code>{{ demo }}</code></pre>
        </div>
      </div>
    </section>

    <!-- 功能特性 -->
    <section id="features" class="mx-auto max-w-6xl px-4 py-16 border-t">
      <h2 class="text-2xl md:text-3xl font-bold tracking-tight text-center">核心能力</h2>
      <p class="mt-3 text-center text-muted-foreground">从协议统一到计费审计，开箱即用的团队级 AI 网关。</p>
      <div class="mt-12 grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-5">
        <div v-for="f in features" :key="f.title" class="rounded-xl border bg-card p-6 hover:shadow-md transition-shadow">
          <div class="w-10 h-10 rounded-lg bg-accent flex items-center justify-center">
            <component :is="f.icon" class="w-5 h-5 text-primary" />
          </div>
          <h3 class="mt-4 font-semibold">{{ f.title }}</h3>
          <p class="mt-2 text-sm text-muted-foreground leading-relaxed">{{ f.desc }}</p>
        </div>
      </div>
    </section>

    <!-- 使用流程 -->
    <section id="how" class="mx-auto max-w-6xl px-4 py-16 border-t">
      <h2 class="text-2xl md:text-3xl font-bold tracking-tight text-center">四步开始</h2>
      <div class="mt-12 grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-6">
        <div v-for="s in steps" :key="s.no" class="relative rounded-xl border bg-card p-6">
          <span class="text-3xl font-bold text-muted-foreground/40">{{ s.no }}</span>
          <h3 class="mt-2 font-semibold">{{ s.title }}</h3>
          <p class="mt-2 text-sm text-muted-foreground leading-relaxed">{{ s.desc }}</p>
        </div>
      </div>
    </section>

    <!-- 全站用户排行榜 -->
    <section id="leaderboard" class="mx-auto max-w-6xl px-4 py-16 border-t">
      <h2 class="text-2xl md:text-3xl font-bold tracking-tight text-center">全站用户排行榜</h2>
      <p class="mt-3 text-center text-muted-foreground">实时汇总全站的调用、用量与积分消耗排名。</p>
      <div class="mt-10">
        <Leaderboard />
      </div>
    </section>

    <!-- CTA -->
    <section class="mx-auto max-w-6xl px-4 py-16">
      <div class="rounded-2xl bg-primary text-primary-foreground px-8 py-12 text-center">
        <h2 class="text-2xl md:text-3xl font-bold">准备好接入你的模型网关了吗？</h2>
        <p class="mt-3 opacity-80">登录管理后台，几分钟内完成供应商、模型与密钥配置。</p>
        <div class="mt-8 flex flex-wrap items-center justify-center gap-3">
          <Button @click="goLogin" class="h-11 px-6 bg-card text-foreground hover:bg-card/90">前往登录</Button>
          <Button @click="scrollToSection('features')" class="h-11 px-6 border border-primary-foreground/30 bg-transparent text-primary-foreground hover:bg-primary-foreground/10">了解更多</Button>
        </div>
      </div>
    </section>

    <!-- 页脚 -->
    <footer class="border-t">
      <div class="mx-auto max-w-6xl px-4 py-8 flex flex-col sm:flex-row items-center justify-between gap-3 text-sm text-muted-foreground">
        <span>© 2026 RouterAPI · AI 接口路由网关</span>
        <span>基于 Go + Vue3 构建 · GPL-3.0</span>
      </div>
    </footer>
  </div>
</template>
