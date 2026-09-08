<script setup lang="ts">
import { ref, onMounted, reactive } from 'vue'
import api from '@/lib/api'
import PageHeader from '@/components/ui/PageHeader.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Input from '@/components/ui/Input.vue'
import Badge from '@/components/ui/Badge.vue'
import { ChevronLeft, ChevronRight, Search } from 'lucide-vue-next'

interface Log {
  id: number
  request_id: string
  upstream_request_id: string
  api_key_name: string
  vendor_name: string
  protocol_name: string
  inbound_protocol: string
  model: string
  upstream_model: string
  token_prompt: number
  token_completion: number
  token_total: number
  credits_cost: number
  latency_ms: number
  status_code: number
  success: boolean
  is_stream: boolean
  error_message: string
  client_ip: string
  user_agent: string
  created_at: string
}

const logs = ref<Log[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const loading = ref(false)

const filters = reactive({
  model: '',
  vendor_id: '',
  keyword: '',
  success: '',
  date_from: '',
  date_to: '',
})

const selectedLog = ref<Log | null>(null)

onMounted(() => {
  loadLogs()
})

async function loadLogs() {
  loading.value = true
  try {
    const params: any = {
      page: page.value,
      page_size: pageSize.value,
    }
    if (filters.model) params.model = filters.model
    if (filters.vendor_id) params.vendor_id = filters.vendor_id
    if (filters.keyword) params.keyword = filters.keyword
    if (filters.success) params.success = filters.success
    if (filters.date_from) params.date_from = filters.date_from
    if (filters.date_to) params.date_to = filters.date_to

    const { data } = await api.get('/admin/logs', { params })
    logs.value = data.data
    total.value = data.total
  } finally {
    loading.value = false
  }
}

function applyFilters() {
  page.value = 1
  loadLogs()
}

function prevPage() {
  if (page.value > 1) {
    page.value--
    loadLogs()
  }
}

function nextPage() {
  if (page.value * pageSize.value < total.value) {
    page.value++
    loadLogs()
  }
}

const totalPages = () => Math.ceil(total.value / pageSize.value)

function viewDetail(log: Log) {
  selectedLog.value = log
}
</script>

<template>
  <div>
    <PageHeader>
      <template #title>调用日志</template>
      <template #description>查看所有 API 调用记录，用于团队审计</template>
    </PageHeader>

    <!-- 筛选 -->
    <Card class="p-4 mb-4">
      <div class="grid grid-cols-1 md:grid-cols-6 gap-3">
        <Input v-model="filters.model" placeholder="模型名" @keyup.enter="applyFilters" />
        <Input v-model="filters.vendor_id" placeholder="供应商ID" @keyup.enter="applyFilters" />
        <Input v-model="filters.keyword" placeholder="关键字/错误" @keyup.enter="applyFilters" />
        <select
          v-model="filters.success"
          class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm"
        >
          <option value="">全部状态</option>
          <option value="1">成功</option>
          <option value="0">失败</option>
        </select>
        <Input v-model="filters.date_from" type="date" />
        <div class="flex gap-2">
          <Input v-model="filters.date_to" type="date" class="flex-1" />
          <Button class="px-3 h-9 bg-primary text-primary-foreground" @click="applyFilters">
            <Search class="w-4 h-4" />
          </Button>
        </div>
      </div>
    </Card>

    <!-- 日志表格 -->
    <Card class="p-0">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b">
            <th class="h-10 px-3 text-left font-medium text-muted-foreground">时间</th>
            <th class="h-10 px-3 text-left font-medium text-muted-foreground">模型</th>
            <th class="h-10 px-3 text-left font-medium text-muted-foreground">供应商</th>
            <th class="h-10 px-3 text-left font-medium text-muted-foreground">Token</th>
            <th class="h-10 px-3 text-left font-medium text-muted-foreground">积分</th>
            <th class="h-10 px-3 text-left font-medium text-muted-foreground">延迟</th>
            <th class="h-10 px-3 text-left font-medium text-muted-foreground">状态</th>
            <th class="h-10 px-3 text-left font-medium text-muted-foreground">调用方</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="log in logs"
            :key="log.id"
            class="border-b hover:bg-gray-50 dark:hover:bg-gray-800/50 cursor-pointer"
            @click="viewDetail(log)"
          >
            <td class="px-3 py-2.5 text-xs text-muted-foreground whitespace-nowrap">
              {{ new Date(log.created_at).toLocaleString() }}
            </td>
            <td class="px-3 py-2.5">
              <div class="font-mono text-xs">{{ log.model }}</div>
              <div v-if="log.upstream_model !== log.model" class="text-xs text-muted-foreground">
                → {{ log.upstream_model }}
              </div>
            </td>
            <td class="px-3 py-2.5 text-xs">
              {{ log.vendor_name || '—' }}
              <div v-if="log.inbound_protocol" class="text-muted-foreground">{{ log.inbound_protocol }}{{ log.protocol_name ? ' → ' + log.protocol_name : '' }}</div>
            </td>
            <td class="px-3 py-2.5 text-xs">
              {{ log.token_total > 0 ? log.token_total : '—' }}
              <div v-if="log.token_total > 0" class="text-muted-foreground">
                {{ log.token_prompt }} + {{ log.token_completion }}
              </div>
            </td>
            <td class="px-3 py-2.5 text-xs font-mono">{{ log.credits_cost || '—' }}</td>
            <td class="px-3 py-2.5 text-xs">{{ log.latency_ms }}ms</td>
            <td class="px-3 py-2.5">
              <Badge :variant="log.success ? 'success' : 'destructive'" class="text-xs">
                {{ log.status_code }}
              </Badge>
            </td>
            <td class="px-3 py-2.5 text-xs">
              {{ log.api_key_name }}
              <div class="text-muted-foreground">{{ log.client_ip }}</div>
            </td>
          </tr>
          <tr v-if="logs.length === 0 && !loading">
            <td colspan="8" class="text-center py-12 text-muted-foreground">暂无日志</td>
          </tr>
        </tbody>
      </table>

      <!-- 分页 -->
      <div class="flex items-center justify-between p-4 border-t">
        <span class="text-sm text-muted-foreground">
          共 {{ total }} 条，第 {{ page }}/{{ totalPages() }} 页
        </span>
        <div class="flex gap-2">
          <Button class="h-8 px-3 border" :disabled="page === 1" @click="prevPage">
            <ChevronLeft class="w-4 h-4" />
          </Button>
          <Button class="h-8 px-3 border" :disabled="page * pageSize >= total" @click="nextPage">
            <ChevronRight class="w-4 h-4" />
          </Button>
        </div>
      </div>
    </Card>

    <!-- 详情弹窗 -->
    <div v-if="selectedLog" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" @click.self="selectedLog = null">
      <Card class="w-full max-w-2xl p-6 max-h-[90vh] overflow-auto">
        <div class="flex items-center justify-between mb-4">
          <h2 class="text-lg font-semibold">调用详情</h2>
          <Button class="h-8 px-3 border" @click="selectedLog = null">关闭</Button>
        </div>

        <div class="grid grid-cols-2 gap-4 text-sm">
          <div>
            <label class="text-muted-foreground text-xs">请求ID</label>
            <p class="font-mono text-xs break-all">{{ selectedLog.request_id }}</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">时间</label>
            <p>{{ new Date(selectedLog.created_at).toLocaleString() }}</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">请求模型</label>
            <p class="font-mono">{{ selectedLog.model }}</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">上游模型</label>
            <p class="font-mono">{{ selectedLog.upstream_model }}</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">供应商</label>
            <p>{{ selectedLog.vendor_name || '—' }}</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">协议（入站 → 上游）</label>
            <p>{{ selectedLog.inbound_protocol || '—' }} → {{ selectedLog.protocol_name || '—' }}</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">调用方</label>
            <p>{{ selectedLog.api_key_name }}</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">客户端IP</label>
            <p class="font-mono">{{ selectedLog.client_ip }}</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">HTTP 状态</label>
            <p>
              <Badge :variant="selectedLog.success ? 'success' : 'destructive'" class="text-xs">
                {{ selectedLog.status_code }}
              </Badge>
            </p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">延迟</label>
            <p>{{ selectedLog.latency_ms }} ms</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">Prompt Tokens</label>
            <p>{{ selectedLog.token_prompt }}</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">Completion Tokens</label>
            <p>{{ selectedLog.token_completion }}</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">总 Tokens</label>
            <p class="font-medium">{{ selectedLog.token_total }}</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">消耗积分</label>
            <p class="font-medium">{{ selectedLog.credits_cost }}</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">上游请求ID</label>
            <p class="font-mono text-xs break-all">{{ selectedLog.upstream_request_id || '—' }}</p>
          </div>
          <div class="col-span-2">
            <label class="text-muted-foreground text-xs">User-Agent</label>
            <p class="font-mono text-xs break-all">{{ selectedLog.user_agent || '—' }}</p>
          </div>
        </div>

        <div v-if="selectedLog.error_message" class="mt-4">
          <label class="text-muted-foreground text-xs">错误信息</label>
          <div class="bg-red-50 text-red-700 p-3 rounded-md text-xs font-mono mt-1 whitespace-pre-wrap break-all">
            {{ selectedLog.error_message }}
          </div>
        </div>
      </Card>
    </div>
  </div>
</template>
