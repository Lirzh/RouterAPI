<script setup lang="ts">
import { ref, onMounted, reactive, computed } from 'vue'
import api from '@/lib/api'
import PageHeader from '@/components/ui/PageHeader.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Badge from '@/components/ui/Badge.vue'
import { Wallet as WalletIcon, Plus, ChevronLeft, ChevronRight } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'

interface Txn {
  id: number
  user_id: number
  amount: number
  type: string
  balance_after: number
  request_id: string
  model: string
  note: string
  operator_id: number
  created_at: string
}

interface UserOption { id: number; username: string }

const auth = useAuthStore()
const isAdmin = computed(() => auth.role === 'admin')

const balance = ref(0)
const txns = ref<Txn[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const users = ref<UserOption[]>([])
const filterUserId = ref(0)
const loading = ref(false)

const showAdjust = ref(false)
const adjustForm = reactive({ user_id: 0, amount: 0, type: 'topup', note: '' })

onMounted(async () => {
  if (isAdmin.value) {
    const { data } = await api.get('/admin/users', { params: { page_size: 100 } })
    users.value = data.data.map((u: any) => ({ id: u.id, username: u.username }))
  }
  await loadWallet()
})

async function loadWallet() {
  loading.value = true
  try {
    const { data } = await api.get('/admin/wallet')
    balance.value = data.credits
    if (!isAdmin.value) {
      txns.value = data.data
      total.value = data.total
    }
  } finally {
    loading.value = false
  }
  if (isAdmin.value) await loadTransactions()
}

async function loadTransactions() {
  loading.value = true
  try {
    const params: any = { page: page.value, page_size: pageSize.value }
    if (filterUserId.value) params.user_id = filterUserId.value
    const { data } = await api.get('/admin/credit-transactions', { params })
    txns.value = data.data
    total.value = data.total
  } finally {
    loading.value = false
  }
}

function applyFilter() { page.value = 1; loadTransactions() }
function prevPage() { if (page.value > 1) { page.value--; loadTransactions() } }
function nextPage() { if (page.value * pageSize.value < total.value) { page.value++; loadTransactions() } }
const totalPages = () => Math.max(1, Math.ceil(total.value / pageSize.value))

function usernameOf(id: number) { return users.value.find(u => u.id === id)?.username || `#${id}` }

function openAdjust() {
  Object.assign(adjustForm, { user_id: users.value[0]?.id || 0, amount: 0, type: 'topup', note: '' })
  showAdjust.value = true
}

async function submitAdjust() {
  if (!adjustForm.user_id) { alert('请选择用户'); return }
  if (!adjustForm.amount) { alert('额度不能为 0'); return }
  const amount = adjustForm.type === 'deduct' ? -Math.abs(adjustForm.amount) : Math.abs(adjustForm.amount)
  try {
    await api.post(`/admin/users/${adjustForm.user_id}/credits`, {
      amount, type: amount > 0 ? 'topup' : 'adjust', note: adjustForm.note,
    })
    showAdjust.value = false
    await loadWallet()
  } catch (e: any) {
    alert(e.response?.data?.error || '调整失败')
  }
}

function typeBadge(t: string) {
  switch (t) {
    case 'topup': return { variant: 'success', text: '充值' }
    case 'consume': return { variant: 'default', text: '消费' }
    case 'refund': return { variant: 'warning', text: '退款' }
    case 'adjust': return { variant: 'secondary', text: '调整' }
    default: return { variant: 'secondary', text: t }
  }
}

function fmt(n: number) { return new Intl.NumberFormat().format(n) }
</script>

<template>
  <div>
    <PageHeader>
      <template #title>积分钱包</template>
      <template #description>积分余额与消费流水（积分不挂钩现实货币，由管理员分配）</template>
      <template #actions>
        <Button v-if="isAdmin" class="bg-primary text-primary-foreground px-4 h-9" @click="openAdjust">
          <Plus class="w-4 h-4" />充值 / 调整
        </Button>
      </template>
    </PageHeader>

    <!-- 余额卡片 -->
    <Card class="p-6 mb-4">
      <div class="flex items-center justify-between">
        <div>
          <p class="text-sm text-muted-foreground">{{ isAdmin ? '我的积分余额' : '积分余额' }}</p>
          <p class="text-3xl font-bold mt-1">{{ loading ? '...' : fmt(balance) }}</p>
        </div>
        <div class="w-12 h-12 rounded-full bg-primary/10 flex items-center justify-center">
          <WalletIcon class="w-6 h-6 text-primary" />
        </div>
      </div>
    </Card>

    <!-- 管理员：用户筛选 -->
    <Card v-if="isAdmin" class="p-4 mb-4">
      <div class="flex items-center gap-3">
        <Label class="whitespace-nowrap">按用户筛选</Label>
        <select v-model.number="filterUserId" @change="applyFilter" class="flex h-9 w-56 rounded-md border border-input bg-transparent px-3 py-1 text-sm">
          <option :value="0">全部用户</option>
          <option v-for="u in users" :key="u.id" :value="u.id">{{ u.username }}</option>
        </select>
      </div>
    </Card>

    <!-- 流水表 -->
    <Card class="p-0">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b">
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">时间</th>
            <th v-if="isAdmin" class="h-10 px-4 text-left font-medium text-muted-foreground">用户</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">类型</th>
            <th class="h-10 px-4 text-right font-medium text-muted-foreground">变动</th>
            <th class="h-10 px-4 text-right font-medium text-muted-foreground">变动后余额</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">模型</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">备注 / 请求</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="t in txns" :key="t.id" class="border-b hover:bg-gray-50 dark:hover:bg-gray-800/50">
            <td class="px-4 py-2.5 text-xs text-muted-foreground whitespace-nowrap">{{ new Date(t.created_at).toLocaleString() }}</td>
            <td v-if="isAdmin" class="px-4 py-2.5 text-xs">{{ usernameOf(t.user_id) }}</td>
            <td class="px-4 py-2.5"><Badge :variant="typeBadge(t.type).variant as any" class="text-xs">{{ typeBadge(t.type).text }}</Badge></td>
            <td class="px-4 py-2.5 text-right font-mono" :class="t.amount >= 0 ? 'text-green-600' : 'text-red-600'">
              {{ t.amount >= 0 ? '+' : '' }}{{ fmt(t.amount) }}
            </td>
            <td class="px-4 py-2.5 text-right font-mono text-muted-foreground">{{ fmt(t.balance_after) }}</td>
            <td class="px-4 py-2.5 font-mono text-xs">{{ t.model || '—' }}</td>
            <td class="px-4 py-2.5 text-xs text-muted-foreground">
              <span v-if="t.note">{{ t.note }}</span>
              <span v-else-if="t.request_id" class="font-mono">{{ t.request_id.slice(0, 8) }}</span>
              <span v-else>—</span>
            </td>
          </tr>
          <tr v-if="txns.length === 0 && !loading">
            <td :colspan="isAdmin ? 7 : 6" class="text-center py-12 text-muted-foreground">暂无流水</td>
          </tr>
        </tbody>
      </table>
      <div v-if="isAdmin" class="flex items-center justify-between p-4 border-t">
        <span class="text-sm text-muted-foreground">共 {{ total }} 条，第 {{ page }}/{{ totalPages() }} 页</span>
        <div class="flex gap-2">
          <Button class="h-8 px-3 border" :disabled="page === 1" @click="prevPage"><ChevronLeft class="w-4 h-4" /></Button>
          <Button class="h-8 px-3 border" :disabled="page * pageSize >= total" @click="nextPage"><ChevronRight class="w-4 h-4" /></Button>
        </div>
      </div>
    </Card>

    <!-- 充值/调整弹窗 -->
    <div v-if="showAdjust" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" @click.self="showAdjust = false">
      <Card class="w-full max-w-md p-6">
        <h2 class="text-lg font-semibold mb-4">充值 / 调整积分</h2>
        <div class="space-y-4">
          <div class="space-y-2">
            <Label>目标用户</Label>
            <select v-model.number="adjustForm.user_id" class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm">
              <option v-for="u in users" :key="u.id" :value="u.id">{{ u.username }}</option>
            </select>
          </div>
          <div class="space-y-2">
            <Label>操作类型</Label>
            <select v-model="adjustForm.type" class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm">
              <option value="topup">充值（增加）</option>
              <option value="deduct">扣减（减少）</option>
            </select>
          </div>
          <div class="space-y-2"><Label>积分数量</Label><Input v-model.number="adjustForm.amount" type="number" placeholder="正整数" /></div>
          <div class="space-y-2"><Label>备注（可选）</Label><Input v-model="adjustForm.note" placeholder="例如: 本月额度分配" /></div>
        </div>
        <div class="flex justify-end gap-2 mt-6">
          <Button class="px-4 h-9 border" @click="showAdjust = false">取消</Button>
          <Button class="px-4 h-9 bg-primary text-primary-foreground" @click="submitAdjust">确认</Button>
        </div>
      </Card>
    </div>
  </div>
</template>
