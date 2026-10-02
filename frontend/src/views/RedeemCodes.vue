<script setup lang="ts">
import { ref, reactive, onMounted, computed } from 'vue'
import api from '@/lib/api'
import PageHeader from '@/components/ui/PageHeader.vue'
import Card from '@/components/ui/Card.vue'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Badge from '@/components/ui/Badge.vue'
import { Plus, Ticket, Copy, Ban, Trash2, ChevronLeft, ChevronRight } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'

interface RedeemCode {
  id: number
  code: string
  code_masked: string
  credits: number
  batch: string
  note: string
  status: string
  expires_at: string | null
  redeemed_by: number
  redeemed_at: string | null
  created_at: string
}

const rows = ref<RedeemCode[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const loading = ref(false)
const filterStatus = ref('')
const filterBatch = ref('')

const showForm = ref(false)
const form = reactive({ credits: 1000, count: 1, batch: '', note: '', expires_at: '' })
const saving = ref(false)

// 生成成功后一次性展示的完整码。关掉就再也拿不到了（列表只有脱敏形态）。
const issued = ref<string[]>([])
const showIssued = ref(false)

const auth = useAuthStore()
const isAdmin = computed(() => auth.role === 'admin')

onMounted(load)

async function load() {
  loading.value = true
  try {
    const params: any = { page: page.value, page_size: pageSize.value }
    if (filterStatus.value) params.status = filterStatus.value
    if (filterBatch.value) params.batch = filterBatch.value
    const { data } = await api.get('/admin/redeem-codes', { params })
    rows.value = data.data || []
    total.value = data.total || 0
  } catch (e: any) {
    alert(e.response?.data?.error || '加载失败')
  } finally {
    loading.value = false
  }
}

function applyFilter() { page.value = 1; load() }
function prevPage() { if (page.value > 1) { page.value--; load() } }
function nextPage() { if (page.value * pageSize.value < total.value) { page.value++; load() } }
const totalPages = () => Math.max(1, Math.ceil(total.value / pageSize.value))

function openForm() {
  Object.assign(form, { credits: 1000, count: 1, batch: '', note: '', expires_at: '' })
  showForm.value = true
}

async function submitForm() {
  if (!form.credits || form.credits <= 0) { alert('面额必须大于 0'); return }
  if (!form.count || form.count <= 0) { alert('数量必须大于 0'); return }
  saving.value = true
  try {
    const body: any = { credits: form.credits, count: form.count, batch: form.batch, note: form.note }
    if (form.expires_at) body.expires_at = new Date(form.expires_at).toISOString()
    const { data } = await api.post('/admin/redeem-codes', body)
    issued.value = (data.data || []).map((r: RedeemCode) => r.code)
    showForm.value = false
    showIssued.value = true
    await load()
  } catch (e: any) {
    alert(e.response?.data?.error || '生成失败')
  } finally {
    saving.value = false
  }
}

async function disableCode(r: RedeemCode) {
  if (!confirm(`确定停用 ${r.code_masked}？停用后该码立即失效。`)) return
  try {
    await api.post(`/admin/redeem-codes/${r.id}/disable`)
    await load()
  } catch (e: any) {
    alert(e.response?.data?.error || '操作失败')
  }
}

async function deleteCode(r: RedeemCode) {
  if (!confirm(`确定删除 ${r.code_masked}？`)) return
  try {
    await api.delete(`/admin/redeem-codes/${r.id}`)
    await load()
  } catch (e: any) {
    alert(e.response?.data?.error || '删除失败')
  }
}

async function copyAll() {
  await navigator.clipboard.writeText(issued.value.join('\n'))
  alert(`已复制 ${issued.value.length} 个兑换码`)
}

function copyOne(code: string) { navigator.clipboard.writeText(code) }

function statusBadge(s: string) {
  switch (s) {
    case 'unused': return { variant: 'success', text: '未兑换' }
    case 'redeemed': return { variant: 'secondary', text: '已兑换' }
    case 'disabled': return { variant: 'destructive', text: '已停用' }
    default: return { variant: 'secondary', text: s }
  }
}

function fmt(n: number) { return new Intl.NumberFormat().format(n) }
function expired(r: RedeemCode) {
  return !!r.expires_at && new Date(r.expires_at).getTime() < Date.now()
}
</script>

<template>
  <div>
    <PageHeader>
      <template #title>兑换码</template>
      <template #description>批量发放积分额度。完整码值只在生成时展示一次，列表与审计日志中均为脱敏形态。</template>
      <template #actions>
        <Button v-if="isAdmin" class="bg-primary text-primary-foreground px-4 h-9" @click="openForm">
          <Plus class="w-4 h-4" />生成兑换码
        </Button>
      </template>
    </PageHeader>

    <!-- 筛选 -->
    <Card class="p-4 mb-4">
      <div class="flex items-center gap-3 flex-wrap">
        <div class="flex items-center gap-2">
          <Label class="whitespace-nowrap">状态</Label>
          <select v-model="filterStatus" @change="applyFilter" class="flex h-9 w-36 rounded-md border border-input bg-transparent px-3 py-1 text-sm">
            <option value="">全部</option>
            <option value="unused">未兑换</option>
            <option value="redeemed">已兑换</option>
            <option value="disabled">已停用</option>
          </select>
        </div>
        <div class="flex items-center gap-2">
          <Label class="whitespace-nowrap">批次</Label>
          <Input v-model="filterBatch" class="w-44" placeholder="批次标签" @keyup.enter="applyFilter" />
        </div>
        <Button class="h-9 px-4 border" @click="applyFilter">筛选</Button>
      </div>
    </Card>

    <!-- 列表 -->
    <Card class="p-0">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b">
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">兑换码</th>
            <th class="h-10 px-4 text-right font-medium text-muted-foreground">面额</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">批次</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">状态</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">兑换人</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">有效期</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">创建时间</th>
            <th class="h-10 px-4 text-right font-medium text-muted-foreground">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in rows" :key="r.id" class="border-b hover:bg-gray-50 dark:hover:bg-gray-800/50">
            <td class="px-4 py-2.5 font-mono text-xs">
              {{ r.code_masked }}
              <button v-if="r.code" class="ml-1 inline-flex" title="复制" @click="copyOne(r.code)"><Copy class="w-3.5 h-3.5 text-muted-foreground" /></button>
            </td>
            <td class="px-4 py-2.5 text-right font-mono">{{ fmt(r.credits) }}</td>
            <td class="px-4 py-2.5 text-xs text-muted-foreground">{{ r.batch || '—' }}</td>
            <td class="px-4 py-2.5">
              <Badge :variant="statusBadge(r.status).variant as any" class="text-xs">{{ statusBadge(r.status).text }}</Badge>
              <Badge v-if="r.status === 'unused' && expired(r)" variant="destructive" class="text-xs ml-1">已过期</Badge>
            </td>
            <td class="px-4 py-2.5 text-xs text-muted-foreground">
              <span v-if="r.redeemed_by">#{{ r.redeemed_by }}</span>
              <span v-else>—</span>
            </td>
            <td class="px-4 py-2.5 text-xs text-muted-foreground whitespace-nowrap">
              {{ r.expires_at ? new Date(r.expires_at).toLocaleString() : '长期有效' }}
            </td>
            <td class="px-4 py-2.5 text-xs text-muted-foreground whitespace-nowrap">{{ new Date(r.created_at).toLocaleString() }}</td>
            <td class="px-4 py-2.5 text-right">
              <template v-if="isAdmin && r.status === 'unused'">
                <Button class="h-7 px-2 text-xs border mr-1" @click="disableCode(r)"><Ban class="w-3.5 h-3.5" />停用</Button>
                <Button class="h-7 px-2 text-xs border" @click="deleteCode(r)"><Trash2 class="w-3.5 h-3.5" />删除</Button>
              </template>
              <span v-else class="text-xs text-muted-foreground">—</span>
            </td>
          </tr>
          <tr v-if="rows.length === 0 && !loading">
            <td colspan="8" class="text-center py-12 text-muted-foreground">暂无兑换码</td>
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

    <!-- 生成弹窗 -->
    <div v-if="showForm" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" @click.self="showForm = false">
      <Card class="w-full max-w-md p-6">
        <h2 class="text-lg font-semibold mb-4">生成兑换码</h2>
        <div class="space-y-4">
          <div class="grid grid-cols-2 gap-4">
            <div class="space-y-2"><Label>单张面额（积分）</Label><Input v-model.number="form.credits" type="number" /></div>
            <div class="space-y-2"><Label>生成数量</Label><Input v-model.number="form.count" type="number" min="1" max="500" /></div>
          </div>
          <div class="space-y-2"><Label>批次标签（可选）</Label><Input v-model="form.batch" placeholder="例如: 2026-09-team-a" /></div>
          <div class="space-y-2"><Label>备注（可选）</Label><Input v-model="form.note" placeholder="发放缘由，便于对账" /></div>
          <div class="space-y-2">
            <Label>过期时间（可选）</Label>
            <Input v-model="form.expires_at" type="datetime-local" />
            <p class="text-xs text-muted-foreground">留空表示长期有效。</p>
          </div>
        </div>
        <div class="flex justify-end gap-2 mt-6">
          <Button class="px-4 h-9 border" @click="showForm = false">取消</Button>
          <Button class="px-4 h-9 bg-primary text-primary-foreground" :disabled="saving" @click="submitForm">
            {{ saving ? '生成中…' : '生成' }}
          </Button>
        </div>
      </Card>
    </div>

    <!-- 一次性展示完整码 -->
    <div v-if="showIssued" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" @click.self="showIssued = false">
      <Card class="w-full max-w-lg p-6">
        <div class="flex items-center gap-2 mb-2">
          <Ticket class="w-5 h-5 text-primary" />
          <h2 class="text-lg font-semibold">{{ issued.length }} 个兑换码已生成</h2>
        </div>
        <p class="text-sm text-destructive mb-4">完整码值只显示这一次，关闭后列表里只能看到脱敏形态。请立即复制并妥善分发。</p>
        <div class="max-h-72 overflow-auto rounded-md border bg-muted/30 p-3">
          <div v-for="c in issued" :key="c" class="flex items-center justify-between py-1">
            <code class="font-mono text-sm">{{ c }}</code>
            <button class="p-1 rounded hover:bg-accent text-muted-foreground" title="复制" @click="copyOne(c)"><Copy class="w-3.5 h-3.5" /></button>
          </div>
        </div>
        <div class="flex justify-end gap-2 mt-6">
          <Button class="px-4 h-9 border" @click="showIssued = false">关闭</Button>
          <Button class="px-4 h-9 bg-primary text-primary-foreground" @click="copyAll"><Copy class="w-4 h-4" />复制全部</Button>
        </div>
      </Card>
    </div>
  </div>
</template>
