<script setup lang="ts">
import { ref, onMounted, reactive, computed } from 'vue'
import api from '@/lib/api'
import PageHeader from '@/components/ui/PageHeader.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Badge from '@/components/ui/Badge.vue'
import { Plus, Pencil, Trash2, Zap, Power, PowerOff } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'

interface ProtocolPreset {
  protocol: string
  display_name: string
  default_base_url: string
  auth_type: string
  auth_header: string
  auth_prefix: string
}

interface Vendor {
  id: number
  name: string
  protocol: string
  base_url: string
  api_key_masked: string
  auth_type: string
  auth_header: string
  auth_prefix: string
  weight: number
  priority: number
  status: number
  max_retry: number
  max_rpm: number
  consecutive_errors: number
  cooldown_until: string | null
  last_error: string
  last_used_at: string | null
  created_at: string
}

const auth = useAuthStore()
const isAdmin = computed(() => auth.role === 'admin')

const vendors = ref<Vendor[]>([])
const protocols = ref<ProtocolPreset[]>([])
const loading = ref(true)
const showForm = ref(false)
const editingId = ref<number | null>(null)
const testing = ref(false)
const testResult = ref<{ success: boolean; models?: string[]; error?: string; latency_ms?: number } | null>(null)

const form = reactive({
  name: '',
  protocol: 'openai',
  base_url: '',
  api_key: '',
  auth_type: '',
  auth_header: '',
  auth_prefix: '',
  weight: 1,
  priority: 0,
  max_retry: 3,
  max_rpm: 0,
  status: 1,
})

const currentPreset = computed(() => protocols.value.find(p => p.protocol === form.protocol))
const isCustomAuth = computed(() => form.protocol === 'custom')

onMounted(async () => {
  await Promise.all([loadVendors(), loadProtocols()])
})

async function loadVendors() {
  loading.value = true
  try {
    const { data } = await api.get('/admin/vendors')
    vendors.value = data.data
  } finally {
    loading.value = false
  }
}

async function loadProtocols() {
  const { data } = await api.get('/admin/protocols')
  protocols.value = data.data
}

function onProtocolChange() {
  const p = currentPreset.value
  if (p && !form.base_url) form.base_url = p.default_base_url
}

function resetForm() {
  form.name = ''
  form.protocol = 'openai'
  form.base_url = protocols.value.find(p => p.protocol === 'openai')?.default_base_url || ''
  form.api_key = ''
  form.auth_type = ''
  form.auth_header = ''
  form.auth_prefix = ''
  form.weight = 1
  form.priority = 0
  form.max_retry = 3
  form.max_rpm = 0
  form.status = 1
  testResult.value = null
}

function openCreate() {
  editingId.value = null
  resetForm()
  showForm.value = true
}

function openEdit(v: Vendor) {
  editingId.value = v.id
  form.name = v.name
  form.protocol = v.protocol
  form.base_url = v.base_url
  form.api_key = v.api_key_masked || ''
  form.auth_type = v.auth_type
  form.auth_header = v.auth_header
  form.auth_prefix = v.auth_prefix
  form.weight = v.weight
  form.priority = v.priority
  form.max_retry = v.max_retry
  form.max_rpm = v.max_rpm
  form.status = v.status
  testResult.value = null
  showForm.value = true
}

async function save() {
  const payload: any = { ...form }
  if (editingId.value && (payload.api_key.includes('****') || payload.api_key === '')) {
    delete payload.api_key
  }
  try {
    if (editingId.value) {
      await api.put(`/admin/vendors/${editingId.value}`, payload)
    } else {
      await api.post('/admin/vendors', payload)
    }
    showForm.value = false
    await loadVendors()
  } catch (e: any) {
    alert(e.response?.data?.error || '保存失败')
  }
}

async function remove(v: Vendor) {
  if (!confirm(`确定删除供应商「${v.name}」？其模型绑定也将删除。`)) return
  try {
    await api.delete(`/admin/vendors/${v.id}`)
    await loadVendors()
  } catch (e: any) {
    alert(e.response?.data?.error || '删除失败')
  }
}

async function test(id: number) {
  testing.value = true
  testResult.value = null
  try {
    const { data } = await api.post(`/admin/vendors/${id}/test`)
    testResult.value = data
  } catch (e: any) {
    testResult.value = { success: false, error: e.response?.data?.error || '测试失败' }
  } finally {
    testing.value = false
  }
}

async function toggleStatus(v: Vendor) {
  const action = v.status === 1 ? 'disable' : 'enable'
  try {
    await api.post(`/admin/vendors/${v.id}/${action}`)
    await loadVendors()
  } catch (e: any) {
    alert(e.response?.data?.error || '操作失败')
  }
}

function isCooling(v: Vendor) {
  return v.cooldown_until && new Date(v.cooldown_until) > new Date()
}

function statusBadge(v: Vendor) {
  if (v.status === 0) return { variant: 'secondary', text: '禁用' }
  if (isCooling(v)) return { variant: 'warning', text: '熔断中' }
  return { variant: 'success', text: '正常' }
}

function protocolName(p: string) {
  return protocols.value.find(x => x.protocol === p)?.display_name || p
}
</script>

<template>
  <div>
    <PageHeader>
      <template #title>供应商</template>
      <template #description>管理上游 AI 服务的协议、凭证与负载均衡设置</template>
      <template #actions>
        <Button v-if="isAdmin" class="bg-primary text-primary-foreground px-4 h-9" @click="openCreate">
          <Plus class="w-4 h-4" />
          添加供应商
        </Button>
      </template>
    </PageHeader>

    <Card class="p-0">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b">
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">名称</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">协议</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">Base URL</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">API Key</th>
            <th class="h-10 px-4 text-center font-medium text-muted-foreground">权重/优先级</th>
            <th class="h-10 px-4 text-center font-medium text-muted-foreground">RPM</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">状态</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">最后使用</th>
            <th class="h-10 px-4 text-right font-medium text-muted-foreground">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="v in vendors" :key="v.id" class="border-b hover:bg-gray-50 dark:hover:bg-gray-800/50">
            <td class="px-4 py-3 font-medium">{{ v.name }}</td>
            <td class="px-4 py-3"><Badge variant="secondary">{{ protocolName(v.protocol) }}</Badge></td>
            <td class="px-4 py-3 text-xs text-muted-foreground font-mono max-w-[200px] truncate" :title="v.base_url">
              {{ v.base_url || '—' }}
            </td>
            <td class="px-4 py-3 font-mono text-xs text-muted-foreground">{{ v.api_key_masked || '—' }}</td>
            <td class="px-4 py-3 text-center text-xs">{{ v.weight }} / {{ v.priority }}</td>
            <td class="px-4 py-3 text-center text-xs">{{ v.max_rpm || '不限' }}</td>
            <td class="px-4 py-3">
              <Badge :variant="statusBadge(v).variant as any">{{ statusBadge(v).text }}</Badge>
              <span v-if="v.consecutive_errors > 0" class="ml-1 text-xs text-muted-foreground">
                (连错 {{ v.consecutive_errors }})
              </span>
            </td>
            <td class="px-4 py-3 text-muted-foreground text-xs">
              {{ v.last_used_at ? new Date(v.last_used_at).toLocaleString() : '—' }}
            </td>
            <td class="px-4 py-3">
              <div class="flex items-center justify-end gap-1">
                <button v-if="isAdmin" class="p-1.5 rounded hover:bg-accent text-muted-foreground" title="测试连通性" @click="test(v.id)">
                  <Zap class="w-4 h-4" />
                </button>
                <button v-if="isAdmin" class="p-1.5 rounded hover:bg-accent text-muted-foreground" :title="v.status === 1 ? '禁用' : '启用'" @click="toggleStatus(v)">
                  <component :is="v.status === 1 ? PowerOff : Power" class="w-4 h-4" />
                </button>
                <button v-if="isAdmin" class="p-1.5 rounded hover:bg-accent text-muted-foreground" title="编辑" @click="openEdit(v)">
                  <Pencil class="w-4 h-4" />
                </button>
                <button v-if="isAdmin" class="p-1.5 rounded hover:bg-red-50 text-destructive" title="删除" @click="remove(v)">
                  <Trash2 class="w-4 h-4" />
                </button>
              </div>
            </td>
          </tr>
          <tr v-if="vendors.length === 0">
            <td colspan="9" class="text-center py-12 text-muted-foreground">暂无供应商，点击右上角添加</td>
          </tr>
        </tbody>
      </table>
    </Card>

    <!-- 测试结果浮层 -->
    <div v-if="testResult" class="mt-4">
      <Card class="p-4" :class="testResult.success ? 'border-green-300' : 'border-red-300'">
        <div class="flex items-start justify-between">
          <div>
            <p class="font-medium text-sm" :class="testResult.success ? 'text-green-700' : 'text-red-700'">
              {{ testResult.success ? '✓ 连接成功' : '✗ 连接失败' }}
              <span v-if="testResult.latency_ms" class="text-muted-foreground font-normal">({{ testResult.latency_ms }}ms)</span>
            </p>
            <p v-if="testResult.error" class="mt-1 text-xs text-red-600">{{ testResult.error }}</p>
            <div v-if="testResult.models && testResult.models.length" class="mt-2 flex flex-wrap gap-1">
              <Badge v-for="m in testResult.models.slice(0, 20)" :key="m" variant="secondary" class="text-xs font-mono">{{ m }}</Badge>
              <span v-if="testResult.models.length > 20" class="text-xs text-muted-foreground">等 {{ testResult.models.length }} 个模型</span>
            </div>
          </div>
          <Button class="h-7 px-2 border text-xs" @click="testResult = null">关闭</Button>
        </div>
      </Card>
    </div>

    <!-- 表单弹窗 -->
    <div v-if="showForm" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" @click.self="showForm = false">
      <Card class="w-full max-w-lg p-6 max-h-[90vh] overflow-auto">
        <h2 class="text-lg font-semibold mb-4">{{ editingId ? '编辑供应商' : '添加供应商' }}</h2>
        <div class="space-y-4">
          <div class="space-y-2">
            <Label>供应商名称</Label>
            <Input v-model="form.name" placeholder="例如: OpenAI-主账号" />
          </div>
          <div class="space-y-2">
            <Label>协议</Label>
            <select v-model="form.protocol" @change="onProtocolChange" class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm">
              <option v-for="p in protocols" :key="p.protocol" :value="p.protocol">{{ p.display_name }} ({{ p.protocol }})</option>
            </select>
          </div>
          <div class="space-y-2">
            <Label>Base URL</Label>
            <Input v-model="form.base_url" :placeholder="currentPreset?.default_base_url || '留空使用协议默认地址'" />
          </div>
          <div class="space-y-2">
            <Label>API Key</Label>
            <Input v-model="form.api_key" type="password" placeholder="输入上游 API Key" />
            <p v-if="editingId && form.api_key.includes('****')" class="text-xs text-muted-foreground">保持脱敏格式则不修改 Key</p>
          </div>
          <div v-if="isCustomAuth" class="space-y-2 p-3 rounded-md bg-secondary/40">
            <Label>自定义鉴权</Label>
            <div class="grid grid-cols-3 gap-2">
              <Input v-model="form.auth_type" placeholder="bearer" />
              <Input v-model="form.auth_header" placeholder="Authorization" />
              <Input v-model="form.auth_prefix" placeholder="Bearer " />
            </div>
            <p class="text-xs text-muted-foreground">依次为：鉴权方式 / 头部名 / 前缀</p>
          </div>
          <div class="grid grid-cols-2 gap-4">
            <div class="space-y-2"><Label>权重</Label><Input v-model.number="form.weight" type="number" /></div>
            <div class="space-y-2"><Label>优先级</Label><Input v-model.number="form.priority" type="number" /></div>
          </div>
          <div class="grid grid-cols-2 gap-4">
            <div class="space-y-2"><Label>最大重试</Label><Input v-model.number="form.max_retry" type="number" /></div>
            <div class="space-y-2"><Label>最大 RPM</Label><Input v-model.number="form.max_rpm" type="number" placeholder="0 = 不限" /></div>
          </div>
          <div class="space-y-2">
            <Label>状态</Label>
            <select v-model.number="form.status" class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm">
              <option :value="1">启用</option>
              <option :value="0">禁用</option>
            </select>
          </div>
        </div>
        <div class="flex justify-end gap-2 mt-6">
          <Button class="px-4 h-9 border" @click="showForm = false">取消</Button>
          <Button v-if="editingId" class="px-4 h-9 bg-blue-500 text-white" :disabled="testing" @click="test(editingId!)">测试</Button>
          <Button class="px-4 h-9 bg-primary text-primary-foreground" @click="save">保存</Button>
        </div>
      </Card>
    </div>
  </div>
</template>
