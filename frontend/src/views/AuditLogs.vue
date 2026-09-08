<script setup lang="ts">
import { ref, onMounted, reactive } from 'vue'
import api from '@/lib/api'
import PageHeader from '@/components/ui/PageHeader.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Input from '@/components/ui/Input.vue'
import Badge from '@/components/ui/Badge.vue'
import { Search, ChevronLeft, ChevronRight } from 'lucide-vue-next'

interface AuditLog {
  id: number
  actor_user_id: number
  actor_name: string
  actor_role: string
  action: string
  method: string
  path: string
  target_id: string
  request_id: string
  client_ip: string
  user_agent: string
  status_code: number
  success: boolean
  extra: string
  created_at: string
}

const logs = ref<AuditLog[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const loading = ref(false)
const selected = ref<AuditLog | null>(null)

const filters = reactive({ action: '', actor_user_id: '', success: '', date_from: '', date_to: '' })

onMounted(() => loadLogs())

async function loadLogs() {
  loading.value = true
  try {
    const params: any = { page: page.value, page_size: pageSize.value }
    if (filters.action) params.action = filters.action
    if (filters.actor_user_id) params.actor_user_id = filters.actor_user_id
    if (filters.success) params.success = filters.success
    if (filters.date_from) params.date_from = filters.date_from
    if (filters.date_to) params.date_to = filters.date_to
    const { data } = await api.get('/admin/audit-logs', { params })
    logs.value = data.data
    total.value = data.total
  } finally {
    loading.value = false
  }
}

function applyFilters() { page.value = 1; loadLogs() }
function prevPage() { if (page.value > 1) { page.value--; loadLogs() } }
function nextPage() { if (page.value * pageSize.value < total.value) { page.value++; loadLogs() } }
const totalPages = () => Math.max(1, Math.ceil(total.value / pageSize.value))

function methodVariant(m: string) {
  switch (m) {
    case 'POST': return 'success'
    case 'PUT': case 'PATCH': return 'warning'
    case 'DELETE': return 'destructive'
    default: return 'secondary'
  }
}

function prettyExtra(e: string) {
  if (!e) return ''
  try { return JSON.stringify(JSON.parse(e), null, 2) } catch { return e }
}
</script>

<template>
  <div>
    <PageHeader>
      <template #title>操作审计</template>
      <template #description>记录管理员对资源的所有写操作，长期保留，用于团队合规审计</template>
    </PageHeader>

    <Card class="p-4 mb-4">
      <div class="grid grid-cols-1 md:grid-cols-5 gap-3">
        <Input v-model="filters.action" placeholder="动作，如 vendor.create" @keyup.enter="applyFilters" />
        <Input v-model="filters.actor_user_id" placeholder="操作者用户ID" @keyup.enter="applyFilters" />
        <select v-model="filters.success" class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm">
          <option value="">全部结果</option>
          <option value="1">成功</option>
          <option value="0">失败</option>
        </select>
        <Input v-model="filters.date_from" type="date" />
        <div class="flex gap-2">
          <Input v-model="filters.date_to" type="date" class="flex-1" />
          <Button class="px-3 h-9 bg-primary text-primary-foreground" @click="applyFilters"><Search class="w-4 h-4" /></Button>
        </div>
      </div>
    </Card>

    <Card class="p-0">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b">
            <th class="h-10 px-3 text-left font-medium text-muted-foreground">时间</th>
            <th class="h-10 px-3 text-left font-medium text-muted-foreground">操作者</th>
            <th class="h-10 px-3 text-left font-medium text-muted-foreground">动作</th>
            <th class="h-10 px-3 text-left font-medium text-muted-foreground">请求</th>
            <th class="h-10 px-3 text-left font-medium text-muted-foreground">目标</th>
            <th class="h-10 px-3 text-left font-medium text-muted-foreground">来源 IP</th>
            <th class="h-10 px-3 text-center font-medium text-muted-foreground">结果</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="l in logs" :key="l.id" class="border-b hover:bg-gray-50 dark:hover:bg-gray-800/50 cursor-pointer" @click="selected = l">
            <td class="px-3 py-2.5 text-xs text-muted-foreground whitespace-nowrap">{{ new Date(l.created_at).toLocaleString() }}</td>
            <td class="px-3 py-2.5 text-xs">
              {{ l.actor_name || '—' }}
              <Badge v-if="l.actor_role" variant="secondary" class="text-xs ml-1">{{ l.actor_role }}</Badge>
            </td>
            <td class="px-3 py-2.5 font-mono text-xs">{{ l.action }}</td>
            <td class="px-3 py-2.5 text-xs">
              <Badge :variant="methodVariant(l.method) as any" class="text-xs">{{ l.method }}</Badge>
              <span class="ml-1 text-muted-foreground font-mono">{{ l.path }}</span>
            </td>
            <td class="px-3 py-2.5 text-xs font-mono">{{ l.target_id || '—' }}</td>
            <td class="px-3 py-2.5 text-xs font-mono text-muted-foreground">{{ l.client_ip }}</td>
            <td class="px-3 py-2.5 text-center">
              <Badge :variant="l.success ? 'success' : 'destructive'" class="text-xs">{{ l.status_code }}</Badge>
            </td>
          </tr>
          <tr v-if="logs.length === 0 && !loading">
            <td colspan="7" class="text-center py-12 text-muted-foreground">暂无审计记录</td>
          </tr>
        </tbody>
      </table>
      <div class="flex items-center justify-between p-4 border-t">
        <span class="text-sm text-muted-foreground">共 {{ total }} 条，第 {{ page }}/{{ totalPages() }} 页</span>
        <div class="flex gap-2">
          <Button class="h-8 px-3 border" :disabled="page === 1" @click="prevPage"><ChevronLeft class="w-4 h-4" /></Button>
          <Button class="h-8 px-3 border" :disabled="page * pageSize >= total" @click="nextPage"><ChevronRight class="w-4 h-4" /></Button>
        </div>
      </div>
    </Card>

    <div v-if="selected" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" @click.self="selected = null">
      <Card class="w-full max-w-xl p-6 max-h-[90vh] overflow-auto">
        <div class="flex items-center justify-between mb-4">
          <h2 class="text-lg font-semibold">审计详情</h2>
          <Button class="h-8 px-3 border" @click="selected = null">关闭</Button>
        </div>
        <div class="grid grid-cols-2 gap-4 text-sm">
          <div><label class="text-muted-foreground text-xs">动作</label><p class="font-mono">{{ selected.action }}</p></div>
          <div><label class="text-muted-foreground text-xs">结果</label><p><Badge :variant="selected.success ? 'success' : 'destructive'" class="text-xs">{{ selected.status_code }}</Badge></p></div>
          <div><label class="text-muted-foreground text-xs">操作者</label><p>{{ selected.actor_name }} ({{ selected.actor_role }}) #{{ selected.actor_user_id }}</p></div>
          <div><label class="text-muted-foreground text-xs">时间</label><p>{{ new Date(selected.created_at).toLocaleString() }}</p></div>
          <div><label class="text-muted-foreground text-xs">请求</label><p class="font-mono text-xs">{{ selected.method }} {{ selected.path }}</p></div>
          <div><label class="text-muted-foreground text-xs">目标 ID</label><p class="font-mono">{{ selected.target_id || '—' }}</p></div>
          <div><label class="text-muted-foreground text-xs">Request ID</label><p class="font-mono text-xs break-all">{{ selected.request_id }}</p></div>
          <div><label class="text-muted-foreground text-xs">来源 IP</label><p class="font-mono">{{ selected.client_ip }}</p></div>
          <div class="col-span-2"><label class="text-muted-foreground text-xs">User-Agent</label><p class="font-mono text-xs break-all">{{ selected.user_agent || '—' }}</p></div>
        </div>
        <div v-if="selected.extra" class="mt-4">
          <label class="text-muted-foreground text-xs">变更字段（白名单脱敏）</label>
          <pre class="bg-secondary/40 p-3 rounded-md text-xs font-mono mt-1 whitespace-pre-wrap break-all">{{ prettyExtra(selected.extra) }}</pre>
        </div>
      </Card>
    </div>
  </div>
</template>
