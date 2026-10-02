<script setup lang="ts">
import { ref, onMounted, reactive, computed } from 'vue'
import api from '@/lib/api'
import PageHeader from '@/components/ui/PageHeader.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Badge from '@/components/ui/Badge.vue'
import { Plus, Pencil, Trash2, Copy, Check } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'

interface APIKey {
  id: number
  name: string
  key: string
  key_masked: string
  owner_user_id: number
  allowed_vendors: string
  allowed_models: string
  allow_ips: string
  credits_cap: number
  rate_limit_rpm: number
  status: number
  expires_at: string | null
  last_used_at: string | null
  created_at: string
}

const auth = useAuthStore()
const isAdmin = computed(() => auth.role === 'admin')

const keys = ref<APIKey[]>([])
const users = ref<{ id: number; username: string }[]>([])
const vendors = ref<{ id: number; name: string }[]>([])
const models = ref<{ name: string }[]>([])
const loading = ref(true)
const showForm = ref(false)
const editingId = ref<number | null>(null)
const copied = ref<number | null>(null)
const newKey = ref('')

const form = reactive({
  name: '',
  key: '',
  owner_user_id: 0,
  status: 1,
  expires_at: '',
  credits_cap: 0,
  rate_limit_rpm: 0,
  selVendors: [] as number[],
  selModels: [] as string[],
  allow_ips: '',
})

onMounted(async () => {
  await Promise.all([loadKeys(), loadRefs()])
})

async function loadKeys() {
  loading.value = true
  try {
    const { data } = await api.get('/admin/api-keys')
    keys.value = data.data
  } finally {
    loading.value = false
  }
}

async function loadRefs() {
  if (!isAdmin.value) return
  const [u, v, m] = await Promise.all([
    api.get('/admin/users', { params: { page_size: 100 } }),
    api.get('/admin/vendors'),
    api.get('/admin/models'),
  ])
  users.value = u.data.data.map((x: any) => ({ id: x.id, username: x.username }))
  vendors.value = v.data.data.map((x: any) => ({ id: x.id, name: x.name }))
  models.value = m.data.data.map((x: any) => ({ name: x.name }))
}

function parseArr(s: string): any[] {
  if (!s || s === '[]' || s === 'null') return []
  try { return JSON.parse(s) } catch { return [] }
}

function resetForm() {
  Object.assign(form, {
    name: '', key: '', owner_user_id: users.value[0]?.id || 0, status: 1, expires_at: '',
    credits_cap: 0, rate_limit_rpm: 0, selVendors: [], selModels: [], allow_ips: '',
  })
  newKey.value = ''
}

function openCreate() {
  editingId.value = null
  resetForm()
  showForm.value = true
}

function openEdit(k: APIKey) {
  editingId.value = k.id
  Object.assign(form, {
    name: k.name, key: '', owner_user_id: k.owner_user_id, status: k.status,
    expires_at: k.expires_at ? k.expires_at.slice(0, 16) : '',
    credits_cap: k.credits_cap, rate_limit_rpm: k.rate_limit_rpm,
    selVendors: parseArr(k.allowed_vendors).map(Number),
    selModels: parseArr(k.allowed_models).map(String),
    allow_ips: parseArr(k.allow_ips).join('\n'),
  })
  newKey.value = ''
  showForm.value = true
}

function toggleVendor(id: number) {
  const i = form.selVendors.indexOf(id)
  if (i >= 0) form.selVendors.splice(i, 1); else form.selVendors.push(id)
}
function toggleModel(name: string) {
  const i = form.selModels.indexOf(name)
  if (i >= 0) form.selModels.splice(i, 1); else form.selModels.push(name)
}

async function save() {
  const ips = form.allow_ips.split(/[\n,]/).map(s => s.trim()).filter(Boolean)
  const payload: any = {
    name: form.name,
    owner_user_id: form.owner_user_id,
    status: form.status,
    credits_cap: Number(form.credits_cap) || 0,
    rate_limit_rpm: Number(form.rate_limit_rpm) || 0,
    allowed_vendors: JSON.stringify(form.selVendors),
    allowed_models: JSON.stringify(form.selModels),
    allow_ips: JSON.stringify(ips),
  }
  if (form.expires_at) payload.expires_at = new Date(form.expires_at).toISOString()
  try {
    if (editingId.value) {
      await api.put(`/admin/api-keys/${editingId.value}`, payload)
      showForm.value = false
      await loadKeys()
    } else {
      if (form.key) payload.key = form.key
      const { data } = await api.post('/admin/api-keys', payload)
      newKey.value = data.data.key
      await loadKeys()
    }
  } catch (e: any) {
    alert(e.response?.data?.error || '保存失败')
  }
}

async function remove(id: number) {
  if (!confirm('确定删除此 API Key？')) return
  await api.delete(`/admin/api-keys/${id}`)
  await loadKeys()
}

function copyKey(key: string, id: number) {
  navigator.clipboard.writeText(key)
  copied.value = id
  setTimeout(() => { copied.value = null }, 2000)
}

function usernameOf(id: number) { return users.value.find(u => u.id === id)?.username || (id ? `#${id}` : '—') }
function vendorNameOf(id: number) { return vendors.value.find(v => v.id === id)?.name || `#${id}` }
function statusBadge(status: number) {
  return status === 1 ? { variant: 'success', text: '正常' } : { variant: 'secondary', text: '禁用' }
}
</script>

<template>
  <div>
    <PageHeader>
      <template #title>API Key</template>
      <template #description>对外调用密钥：归属、授权范围（供应商/模型）、IP 白名单与花费上限</template>
      <template #actions>
        <Button v-if="isAdmin" class="bg-primary text-primary-foreground px-4 h-9" @click="openCreate">
          <Plus class="w-4 h-4" />创建 API Key
        </Button>
      </template>
    </PageHeader>

    <Card class="p-0">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b">
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">名称</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">Key</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">归属</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">授权范围</th>
            <th class="h-10 px-4 text-center font-medium text-muted-foreground">上限/RPM</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">状态</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">最后使用</th>
            <th class="h-10 px-4 text-right font-medium text-muted-foreground">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="key in keys" :key="key.id" class="border-b hover:bg-gray-50 dark:hover:bg-gray-800/50">
            <td class="px-4 py-3 font-medium">{{ key.name }}</td>
            <td class="px-4 py-3">
              <div class="flex items-center gap-2">
                <code class="font-mono text-xs text-muted-foreground">{{ key.key_masked }}</code>
                <button class="p-1 rounded hover:bg-accent text-muted-foreground" @click="copyKey(key.key_masked, key.id)" title="复制">
                  <component :is="copied === key.id ? Check : Copy" class="w-3 h-3" />
                </button>
              </div>
            </td>
            <td class="px-4 py-3 text-xs">{{ usernameOf(key.owner_user_id) }}</td>
            <td class="px-4 py-3 text-xs">
              <div v-if="parseArr(key.allowed_models).length === 0 && parseArr(key.allowed_vendors).length === 0" class="text-muted-foreground">全部模型</div>
              <div v-else class="flex flex-wrap gap-1">
                <Badge v-for="m in parseArr(key.allowed_models)" :key="'m'+m" variant="secondary" class="text-xs font-mono">{{ m }}</Badge>
                <Badge v-for="vid in parseArr(key.allowed_vendors)" :key="'v'+vid" variant="warning" class="text-xs">{{ vendorNameOf(vid) }}</Badge>
              </div>
            </td>
            <td class="px-4 py-3 text-center text-xs">
              <div>{{ key.credits_cap ? key.credits_cap : '不限' }}</div>
              <div class="text-muted-foreground">{{ key.rate_limit_rpm ? key.rate_limit_rpm + ' rpm' : '不限 rpm' }}</div>
            </td>
            <td class="px-4 py-3"><Badge :variant="statusBadge(key.status).variant as any">{{ statusBadge(key.status).text }}</Badge></td>
            <td class="px-4 py-3 text-xs text-muted-foreground">{{ key.last_used_at ? new Date(key.last_used_at).toLocaleString() : '—' }}</td>
            <td class="px-4 py-3">
              <div class="flex items-center justify-end gap-1">
                <button v-if="isAdmin" class="p-1.5 rounded hover:bg-accent text-muted-foreground" title="编辑" @click="openEdit(key)"><Pencil class="w-4 h-4" /></button>
                <button v-if="isAdmin" class="p-1.5 rounded hover:bg-red-50 text-destructive" title="删除" @click="remove(key.id)"><Trash2 class="w-4 h-4" /></button>
              </div>
            </td>
          </tr>
          <tr v-if="keys.length === 0">
            <td colspan="8" class="text-center py-12 text-muted-foreground">暂无 API Key，点击右上角创建</td>
          </tr>
        </tbody>
      </table>
    </Card>

    <!-- 创建/编辑弹窗 -->
    <div v-if="showForm" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" @click.self="showForm = false">
      <Card class="w-full max-w-lg p-6 max-h-[90vh] overflow-auto">
        <h2 class="text-lg font-semibold mb-4">{{ editingId ? '编辑 API Key' : '创建 API Key' }}</h2>

        <!-- 创建成功后显示明文 Key -->
        <div v-if="newKey" class="space-y-4">
          <div class="bg-green-50 text-green-700 dark:bg-green-950 dark:text-green-400 p-3 rounded-md text-sm">
            <p class="font-medium mb-2">✓ API Key 已创建</p>
            <p class="text-xs mb-2">请妥善保存，此 Key 仅显示一次：</p>
            <div class="flex items-center gap-2">
              <code class="font-mono text-xs bg-white dark:bg-black p-2 rounded border flex-1 break-all">{{ newKey }}</code>
              <button class="p-2 rounded hover:bg-accent text-muted-foreground shrink-0" @click="copyKey(newKey, -1)">
                <component :is="copied === -1 ? Check : Copy" class="w-4 h-4" />
              </button>
            </div>
          </div>
          <Button class="w-full h-9 bg-primary text-primary-foreground" @click="showForm = false">完成</Button>
        </div>

        <div v-else class="space-y-4">
          <div class="space-y-2"><Label>名称</Label><Input v-model="form.name" placeholder="例如: 团队A-调用Key" /></div>
          <div v-if="!editingId" class="space-y-2"><Label>API Key（留空自动生成）</Label><Input v-model="form.key" placeholder="自动生成；自定义至少 24 字符" /></div>
          <div class="grid grid-cols-2 gap-4">
            <div class="space-y-2">
              <Label>归属用户</Label>
              <select v-model.number="form.owner_user_id" class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm">
                <option v-for="u in users" :key="u.id" :value="u.id">{{ u.username }}</option>
              </select>
            </div>
            <div class="space-y-2">
              <Label>状态</Label>
              <select v-model.number="form.status" class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm">
                <option :value="1">启用</option><option :value="0">禁用</option>
              </select>
            </div>
          </div>
          <div class="grid grid-cols-2 gap-4">
            <div class="space-y-2"><Label>花费上限（积分，0=不限）</Label><Input v-model.number="form.credits_cap" type="number" /></div>
            <div class="space-y-2"><Label>限流 RPM（0=不限）</Label><Input v-model.number="form.rate_limit_rpm" type="number" /></div>
          </div>
          <div class="space-y-2"><Label>过期时间（可选）</Label><Input v-model="form.expires_at" type="datetime-local" /></div>

          <div class="space-y-2">
            <Label>授权供应商（留空=不按供应商限制）</Label>
            <div class="flex flex-wrap gap-2 p-2 border rounded-md max-h-28 overflow-auto">
              <label v-for="v in vendors" :key="v.id" class="flex items-center gap-1 text-xs cursor-pointer">
                <input type="checkbox" :checked="form.selVendors.includes(v.id)" @change="toggleVendor(v.id)" class="rounded border-input" />{{ v.name }}
              </label>
              <span v-if="vendors.length === 0" class="text-xs text-muted-foreground">暂无供应商</span>
            </div>
          </div>
          <div class="space-y-2">
            <Label>授权模型（留空=全部模型）</Label>
            <div class="flex flex-wrap gap-2 p-2 border rounded-md max-h-28 overflow-auto">
              <label v-for="m in models" :key="m.name" class="flex items-center gap-1 text-xs font-mono cursor-pointer">
                <input type="checkbox" :checked="form.selModels.includes(m.name)" @change="toggleModel(m.name)" class="rounded border-input" />{{ m.name }}
              </label>
              <span v-if="models.length === 0" class="text-xs text-muted-foreground">暂无模型</span>
            </div>
          </div>
          <div class="space-y-2">
            <Label>IP 白名单（每行一个 IP 或 CIDR，留空=不限）</Label>
            <textarea v-model="form.allow_ips" rows="2" class="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm font-mono" placeholder="10.0.0.0/8&#10;192.168.1.5"></textarea>
          </div>

          <div class="flex justify-end gap-2 mt-6">
            <Button class="px-4 h-9 border" @click="showForm = false">取消</Button>
            <Button class="px-4 h-9 bg-primary text-primary-foreground" @click="save">{{ editingId ? '保存' : '创建' }}</Button>
          </div>
        </div>
      </Card>
    </div>
  </div>
</template>
