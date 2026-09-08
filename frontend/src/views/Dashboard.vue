<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import api from '@/lib/api'
import PageHeader from '@/components/ui/PageHeader.vue'
import Card from '@/components/ui/Card.vue'
import {
  Activity,
  CheckCircle2,
  Zap,
  Coins,
} from 'lucide-vue-next'

interface Stats {
  total_calls: number
  success_calls: number
  total_tokens: number
  total_credits: number
  today_calls: number
  vendor_stats: { vendor_id: number; vendor_name: string; calls: number; success: number; credits: number }[]
  model_stats: { model: string; calls: number; tokens: number; credits: number }[]
  daily_stats: { date: string; calls: number }[]
}

const stats = ref<Stats>({
  total_calls: 0,
  success_calls: 0,
  total_tokens: 0,
  total_credits: 0,
  today_calls: 0,
  vendor_stats: [],
  model_stats: [],
  daily_stats: [],
})

const loading = ref(true)

onMounted(async () => {
  try {
    const { data } = await api.get('/admin/stats')
    stats.value = data
  } catch (e) {
    console.error('加载统计数据失败', e)
  } finally {
    loading.value = false
  }
})

function formatNumber(n: number) {
  return new Intl.NumberFormat().format(n)
}

const successRate = computed(() => {
  if (stats.value.total_calls === 0) return 0
  return ((stats.value.success_calls / stats.value.total_calls) * 100).toFixed(1)
})

// 计算柱状图最大值
const maxDailyCalls = computed(() => {
  return Math.max(...stats.value.daily_stats.map(d => d.calls), 1)
})
</script>

<template>
  <div>
    <PageHeader>
      <template #title>仪表盘</template>
      <template #description>查看 API 调用统计和系统运行状态</template>
    </PageHeader>

    <!-- 统计卡片 -->
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
      <Card class="p-6">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-muted-foreground">总调用数</p>
            <p class="text-2xl font-bold mt-1">{{ loading ? '...' : formatNumber(stats.total_calls) }}</p>
          </div>
          <div class="w-10 h-10 rounded-full bg-blue-50 dark:bg-blue-950 flex items-center justify-center">
            <Activity class="w-5 h-5 text-blue-500 dark:text-blue-400" />
          </div>
        </div>
      </Card>

      <Card class="p-6">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-muted-foreground">成功调用</p>
            <p class="text-2xl font-bold mt-1">{{ loading ? '...' : formatNumber(stats.success_calls) }}</p>
            <p class="text-xs text-muted-foreground mt-1">成功率 {{ successRate }}%</p>
          </div>
          <div class="w-10 h-10 rounded-full bg-green-50 dark:bg-green-950 flex items-center justify-center">
            <CheckCircle2 class="w-5 h-5 text-green-500 dark:text-green-400" />
          </div>
        </div>
      </Card>

      <Card class="p-6">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-muted-foreground">总 Token</p>
            <p class="text-2xl font-bold mt-1">{{ loading ? '...' : formatNumber(stats.total_tokens) }}</p>
          </div>
          <div class="w-10 h-10 rounded-full bg-purple-50 dark:bg-purple-950 flex items-center justify-center">
            <Zap class="w-5 h-5 text-purple-500 dark:text-purple-400" />
          </div>
        </div>
      </Card>

      <Card class="p-6">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm text-muted-foreground">总消耗积分</p>
            <p class="text-2xl font-bold mt-1">{{ loading ? '...' : formatNumber(stats.total_credits) }}</p>
            <p class="text-xs text-muted-foreground mt-1">今日 {{ formatNumber(stats.today_calls) }} 次调用</p>
          </div>
          <div class="w-10 h-10 rounded-full bg-orange-50 dark:bg-orange-950 flex items-center justify-center">
            <Coins class="w-5 h-5 text-orange-500 dark:text-orange-400" />
          </div>
        </div>
      </Card>
    </div>

    <!-- 图表 -->
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <!-- 7日趋势 -->
      <Card class="p-6">
        <h3 class="font-semibold mb-4">近 7 日调用趋势</h3>
        <div class="flex items-end gap-2 h-40">
          <div
            v-for="item in stats.daily_stats"
            :key="item.date"
            class="flex-1 flex flex-col items-center gap-1"
          >
            <div
              class="w-full bg-primary rounded-t transition-all hover:bg-primary/80"
              :style="{ height: `${(item.calls / maxDailyCalls) * 100}%` }"
              :title="`${item.calls} 次`"
            ></div>
            <span class="text-xs text-muted-foreground">
              {{ item.date.slice(5) }}
            </span>
          </div>
          <div v-if="stats.daily_stats.length === 0" class="flex items-center justify-center w-full text-muted-foreground text-sm">
            暂无数据
          </div>
        </div>
      </Card>

      <!-- 供应商统计 -->
      <Card class="p-6">
        <h3 class="font-semibold mb-4">供应商调用统计</h3>
        <div class="space-y-3">
          <div
            v-for="item in stats.vendor_stats"
            :key="item.vendor_id"
            class="flex items-center justify-between text-sm"
          >
            <span class="font-medium truncate flex-1">{{ item.vendor_name || '未知' }}</span>
            <div class="flex items-center gap-3">
              <span class="text-muted-foreground">{{ item.success }}/{{ item.calls }}</span>
              <div class="w-24 h-2 bg-secondary rounded-full overflow-hidden">
                <div
                  class="h-full bg-green-500"
                  :style="{ width: `${item.calls > 0 ? (item.success / item.calls) * 100 : 0}%` }"
                ></div>
              </div>
            </div>
          </div>
          <div v-if="stats.vendor_stats.length === 0" class="text-muted-foreground text-sm text-center py-4">
            暂无数据
          </div>
        </div>
      </Card>
    </div>

    <!-- 模型统计 -->
    <Card class="p-6 mt-4">
      <h3 class="font-semibold mb-4">模型调用排行</h3>
      <div class="space-y-3">
        <div
          v-for="item in stats.model_stats"
          :key="item.model"
          class="flex items-center gap-4 text-sm"
        >
          <span class="font-mono w-48 truncate">{{ item.model }}</span>
          <div class="flex-1 h-6 bg-secondary rounded overflow-hidden">
            <div
              class="h-full bg-blue-500 flex items-center justify-end pr-2 text-white text-xs"
              :style="{ width: `${Math.min((item.calls / Math.max(...stats.model_stats.map(s => s.calls), 1)) * 100, 100)}%` }"
            >
              {{ item.calls }}
            </div>
          </div>
          <span class="text-muted-foreground w-32 text-right">{{ formatNumber(item.tokens) }} tok · {{ formatNumber(item.credits) }} 积分</span>
        </div>
        <div v-if="stats.model_stats.length === 0" class="text-muted-foreground text-sm text-center py-4">
          暂无数据
        </div>
      </div>
    </Card>
  </div>
</template>
