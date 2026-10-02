<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import api from '@/lib/api'
import Card from '@/components/ui/Card.vue'
import { Activity, Coins, Zap, Users } from 'lucide-vue-next'

interface Entry {
  rank: number
  user_id: number
  username: string
  calls: number
  success: number
  tokens: number
  credits: number
}
interface Totals {
  calls: number
  tokens: number
  credits: number
  users: number
}

const periods = [
  { value: 'today', label: '今日' },
  { value: 'week', label: '近 7 天' },
  { value: 'month', label: '近 30 天' },
  { value: 'all', label: '全部' },
]
const metrics = [
  { value: 'credits', label: '消耗积分' },
  { value: 'tokens', label: 'Token 用量' },
  { value: 'calls', label: '调用次数' },
]
const limits = [
  { value: 10, label: 'Top 10' },
  { value: 20, label: 'Top 20' },
  { value: 50, label: 'Top 50' },
]

const period = ref('week')
const metric = ref('credits')
const limit = ref(20)
const entries = ref<Entry[]>([])
const totals = ref<Totals>({ calls: 0, tokens: 0, credits: 0, users: 0 })
const enabled = ref(true)
const anon = ref(false)
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const { data } = await api.get('/leaderboard', {
      params: { period: period.value, metric: metric.value, limit: limit.value },
    })
    enabled.value = data.enabled !== false
    anon.value = !!data.anon
    entries.value = data.data || []
    totals.value = data.totals || { calls: 0, tokens: 0, credits: 0, users: 0 }
  } catch (e: any) {
    error.value = e?.response?.data?.error || '排行榜加载失败，请稍后重试'
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch([period, metric, limit], load)

const podium = computed(() => entries.value.slice(0, 3))
const rest = computed(() => entries.value.slice(3))

function fmt(n: number) {
  return new Intl.NumberFormat().format(n)
}
function fmtCompact(n: number) {
  if (n >= 1_000_000_000) return (n / 1_000_000_000).toFixed(2) + 'B'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k'
  return String(n)
}
// 当前排名维度的取值（供列表/领奖台高亮显示）
function metricValue(e: Entry) {
  if (metric.value === 'tokens') return fmtCompact(e.tokens)
  if (metric.value === 'calls') return fmt(e.calls)
  return fmt(e.credits)
}
const medalClass = ['bg-yellow-100 text-yellow-700', 'bg-gray-200 text-gray-700', 'bg-orange-100 text-orange-700']
</script>

<template>
  <div class="space-y-6">
    <!-- 排行榜未开放 -->
    <div v-if="!enabled" class="text-center py-12 text-muted-foreground border rounded-lg bg-card">
      排行榜当前未开放，请联系管理员或稍后再试。
    </div>

    <template v-else>
      <!-- 工具条：周期 + 维度 + Top N -->
      <div class="flex flex-col gap-3 lg:flex-row lg:flex-wrap lg:items-center lg:justify-between">
        <div class="inline-flex rounded-lg border bg-card p-1">
          <button
            v-for="p in periods"
            :key="p.value"
            @click="period = p.value"
            class="px-3 py-1.5 text-sm rounded-md transition-colors"
            :class="period === p.value ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-accent'"
          >{{ p.label }}</button>
        </div>
        <div class="flex flex-wrap items-center gap-3">
          <div class="inline-flex rounded-lg border bg-card p-1">
            <button
              v-for="m in metrics"
              :key="m.value"
              @click="metric = m.value"
              class="px-3 py-1.5 text-sm rounded-md transition-colors"
              :class="metric === m.value ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-accent'"
            >{{ m.label }}</button>
          </div>
          <div class="inline-flex rounded-lg border bg-card p-1">
            <button
              v-for="l in limits"
              :key="l.value"
              @click="limit = l.value"
              class="px-3 py-1.5 text-sm rounded-md transition-colors"
              :class="limit === l.value ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-accent'"
            >{{ l.label }}</button>
          </div>
        </div>
      </div>

      <!-- 匿名化提示 -->
      <p v-if="anon" class="text-xs text-muted-foreground -mt-2">为保护隐私，用户名已匿名化显示。</p>

      <!-- 全站汇总数据条 -->
      <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
        <Card class="p-4">
          <div class="flex items-center gap-2 text-muted-foreground text-sm"><Users class="w-4 h-4" />参与用户</div>
          <p class="text-2xl font-bold mt-1">{{ loading ? '...' : fmt(totals.users) }}</p>
        </Card>
        <Card class="p-4">
          <div class="flex items-center gap-2 text-muted-foreground text-sm"><Activity class="w-4 h-4" />总调用</div>
          <p class="text-2xl font-bold mt-1">{{ loading ? '...' : fmtCompact(totals.calls) }}</p>
        </Card>
        <Card class="p-4">
          <div class="flex items-center gap-2 text-muted-foreground text-sm"><Zap class="w-4 h-4" />总 Token</div>
          <p class="text-2xl font-bold mt-1">{{ loading ? '...' : fmtCompact(totals.tokens) }}</p>
        </Card>
        <Card class="p-4">
          <div class="flex items-center gap-2 text-muted-foreground text-sm"><Coins class="w-4 h-4" />总积分</div>
          <p class="text-2xl font-bold mt-1">{{ loading ? '...' : fmtCompact(totals.credits) }}</p>
        </Card>
      </div>

      <!-- 错误态 -->
      <div v-if="error" class="bg-red-50 dark:bg-red-950 text-red-600 dark:text-red-400 text-sm p-3 rounded-md">
        {{ error }}
      </div>

      <template v-else>
        <!-- 空态 -->
        <div v-if="!loading && entries.length === 0" class="text-center py-12 text-muted-foreground border rounded-lg bg-card">
          暂无数据 —— 还没有用户产生调用记录。
        </div>

        <template v-else>
          <!-- 领奖台：前三名 -->
          <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
            <Card
              v-for="(e, i) in podium"
              :key="e.user_id"
              class="p-5 relative overflow-hidden"
              :class="i === 0 ? 'sm:order-2 border-yellow-300 dark:border-yellow-700' : i === 1 ? 'sm:order-1' : 'sm:order-3'"
            >
              <div class="flex items-center justify-between">
                <span class="inline-flex items-center justify-center w-8 h-8 rounded-full text-sm font-bold" :class="medalClass[i]">{{ e.rank }}</span>
                <span class="text-xs text-muted-foreground">{{ periods.find(p => p.value === period)?.label }}</span>
              </div>
              <p class="mt-3 font-semibold truncate">{{ e.username || `用户#${e.user_id}` }}</p>
              <p class="text-2xl font-bold mt-1">{{ metricValue(e) }}</p>
              <p class="text-xs text-muted-foreground">{{ metrics.find(m => m.value === metric)?.label }}</p>
            </Card>
          </div>

          <!-- 其余名次 -->
          <Card class="overflow-hidden">
            <div class="grid grid-cols-[3rem_1fr_auto] sm:grid-cols-[3rem_1fr_6rem_6rem_7rem] items-center gap-2 px-5 py-3 border-b text-xs font-medium text-muted-foreground">
              <span>名次</span><span>用户</span>
              <span class="hidden sm:block text-right">调用</span>
              <span class="hidden sm:block text-right">Token</span>
              <span class="text-right">{{ metrics.find(m => m.value === metric)?.label }}</span>
            </div>
            <div
              v-for="e in rest"
              :key="e.user_id"
              class="grid grid-cols-[3rem_1fr_auto] sm:grid-cols-[3rem_1fr_6rem_6rem_7rem] items-center gap-2 px-5 py-3 border-b last:border-0 text-sm hover:bg-accent/50 transition-colors"
            >
              <span class="text-muted-foreground font-medium">{{ e.rank }}</span>
              <span class="font-medium truncate">{{ e.username || `用户#${e.user_id}` }}</span>
              <span class="hidden sm:block text-right text-muted-foreground">{{ fmtCompact(e.calls) }}</span>
              <span class="hidden sm:block text-right text-muted-foreground">{{ fmtCompact(e.tokens) }}</span>
              <span class="text-right font-semibold">{{ metricValue(e) }}</span>
            </div>
          </Card>
        </template>
      </template>
    </template>
  </div>
</template>
