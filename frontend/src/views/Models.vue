<script setup lang="ts">
import { ref, onMounted, reactive, computed } from 'vue'
import api from '@/lib/api'
import PageHeader from '@/components/ui/PageHeader.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Badge from '@/components/ui/Badge.vue'
import { Plus, Pencil, Trash2, Link2, Download, Search } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'

interface Binding {
  id: number
  model_id: number
  vendor_id: number
  vendor_name: string
  vendor_protocol: string
  upstream_model: string
  params: string
  weight: number
  priority: number
  enabled: boolean
}

interface ModelItem {
  id: number
  name: string
  display_name: string
  description: string
  enabled: boolean
  prompt_credits_per_1m: number
  completion_credits_per_1m: number
  cache_read_credits_per_1m: number
  cache_write_credits_per_1m: number
  bindings: Binding[]
}

interface VendorOption {
  id: number
  name: string
  protocol: string
}

const auth = useAuthStore()
const isAdmin = computed(() => auth.role === 'admin')

const models = ref<ModelItem[]>([])
const vendors = ref<VendorOption[]>([])
const loading = ref(true)

// 模型表单
const showForm = ref(false)
const editingId = ref<number | null>(null)
const form = reactive({
  name: '',
  display_name: '',
  description: '',
  enabled: true,
  prompt_credits_per_1m: 0,
  completion_credits_per_1m: 0,
  cache_read_credits_per_1m: 0,
  cache_write_credits_per_1m: 0,
})

// 绑定管理
const showBindings = ref(false)
const bindingModel = ref<ModelItem | null>(null)
const bindings = ref<Binding[]>([])
const bindingForm = reactive({
  id: 0,
  vendor_id: 0,
  upstream_model: '',
  params: '',
  weight: 1,
  priority: 0,
  enabled: true,
})

// 发现导入
const showDiscover = ref(false)
const discoverVendorId = ref(0)
const discovering = ref(false)
const discovered = ref<string[]>([])
const selectedModels = ref<Set<string>>(new Set())
const importResult = ref<string>('')

onMounted(async () => {
  await Promise.all([loadModels(), loadVendors()])
})

async function loadModels() {
  loading.value = true
  try {
    const { data } = await api.get('/admin/models')
    models.value = data.data
  } finally {
    loading.value = false
  }
}

async function loadVendors() {
  const { data } = await api.get('/admin/vendors')
  vendors.value = data.data.map((v: any) => ({ id: v.id, name: v.name, protocol: v.protocol }))
}

// ---------- 模型 CRUD ----------
function openCreate() {
  editingId.value = null
  Object.assign(form, {
    name: '', display_name: '', description: '', enabled: true,
    prompt_credits_per_1m: 0, completion_credits_per_1m: 0,
    cache_read_credits_per_1m: 0, cache_write_credits_per_1m: 0,
  })
  showForm.value = true
}

function openEdit(m: ModelItem) {
  editingId.value = m.id
  Object.assign(form, {
    name: m.name, display_name: m.display_name, description: m.description, enabled: m.enabled,
    prompt_credits_per_1m: m.prompt_credits_per_1m, completion_credits_per_1m: m.completion_credits_per_1m,
    cache_read_credits_per_1m: m.cache_read_credits_per_1m ?? 0, cache_write_credits_per_1m: m.cache_write_credits_per_1m ?? 0,
  })
  showForm.value = true
}

async function saveModel() {
  try {
    if (editingId.value) {
      await api.put(`/admin/models/${editingId.value}`, { ...form })
    } else {
      await api.post('/admin/models', { ...form })
    }
    showForm.value = false
    await loadModels()
  } catch (e: any) {
    alert(e.response?.data?.error || '保存失败')
  }
}

async function removeModel(m: ModelItem) {
  if (!confirm(`确定删除模型「${m.name}」？其所有绑定也将删除。`)) return
  try {
    await api.delete(`/admin/models/${m.id}`)
    await loadModels()
  } catch (e: any) {
    alert(e.response?.data?.error || '删除失败')
  }
}

// ---------- 绑定管理 ----------
async function openBindings(m: ModelItem) {
  bindingModel.value = m
  const { data } = await api.get(`/admin/models/${m.id}/bindings`)
  bindings.value = data.data
  resetBindingForm()
  showBindings.value = true
}

function resetBindingForm() {
  Object.assign(bindingForm, { id: 0, vendor_id: vendors.value[0]?.id || 0, upstream_model: '', params: '', weight: 1, priority: 0, enabled: true })
}

function editBinding(b: Binding) {
  Object.assign(bindingForm, {
    id: b.id, vendor_id: b.vendor_id, upstream_model: b.upstream_model,
    params: b.params, weight: b.weight, priority: b.priority, enabled: b.enabled,
  })
}

async function saveBinding() {
  if (!bindingModel.value) return
  if (!bindingForm.upstream_model.trim()) { alert('上游模型名不能为空'); return }
  const payload: any = {
    vendor_id: bindingForm.vendor_id, upstream_model: bindingForm.upstream_model,
    params: bindingForm.params, weight: bindingForm.weight, priority: bindingForm.priority, enabled: bindingForm.enabled,
  }
  try {
    if (bindingForm.id) {
      await api.put(`/admin/models/${bindingModel.value.id}/bindings/${bindingForm.id}`, payload)
    } else {
      await api.post(`/admin/models/${bindingModel.value.id}/bindings`, payload)
    }
    const { data } = await api.get(`/admin/models/${bindingModel.value.id}/bindings`)
    bindings.value = data.data
    resetBindingForm()
    await loadModels()
  } catch (e: any) {
    alert(e.response?.data?.error || '保存绑定失败')
  }
}

async function removeBinding(b: Binding) {
  if (!bindingModel.value) return
  if (!confirm('确定删除此绑定？')) return
  await api.delete(`/admin/models/${bindingModel.value.id}/bindings/${b.id}`)
  const { data } = await api.get(`/admin/models/${bindingModel.value.id}/bindings`)
  bindings.value = data.data
  await loadModels()
}

// ---------- 发现导入 ----------
function openDiscover() {
  discoverVendorId.value = vendors.value[0]?.id || 0
  discovered.value = []
  selectedModels.value = new Set()
  importResult.value = ''
  showDiscover.value = true
}

async function doDiscover() {
  if (!discoverVendorId.value) { alert('请选择供应商'); return }
  discovering.value = true
  discovered.value = []
  importResult.value = ''
  try {
    const { data } = await api.get(`/admin/vendors/${discoverVendorId.value}/models`)
    discovered.value = data.data || []
  } catch (e: any) {
    alert(e.response?.data?.error || '发现模型失败')
  } finally {
    discovering.value = false
  }
}

function toggleSelect(m: string) {
  const s = new Set(selectedModels.value)
  if (s.has(m)) s.delete(m); else s.add(m)
  selectedModels.value = s
}

function selectAll() {
  selectedModels.value = new Set(discovered.value)
}

async function doImport() {
  if (selectedModels.value.size === 0) { alert('请至少选择一个模型'); return }
  try {
    const { data } = await api.post(`/admin/vendors/${discoverVendorId.value}/import-models`, {
      models: Array.from(selectedModels.value),
    })
    importResult.value = `导入完成：新建 ${data.created} 个，跳过 ${data.skipped} 个`
    await loadModels()
  } catch (e: any) {
    alert(e.response?.data?.error || '导入失败')
  }
}
</script>

<template>
  <div>
    <PageHeader>
      <template #title>模型</template>
      <template #description>虚拟模型目录：定价、启用开关与多供应商绑定（容灾）</template>
      <template #actions>
        <Button v-if="isAdmin" class="border px-4 h-9 mr-2" @click="openDiscover">
          <Download class="w-4 h-4" />
          获取模型
        </Button>
        <Button v-if="isAdmin" class="bg-primary text-primary-foreground px-4 h-9" @click="openCreate">
          <Plus class="w-4 h-4" />
          添加模型
        </Button>
      </template>
    </PageHeader>

    <Card class="p-0">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b">
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">模型名</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">展示名</th>
            <th class="h-10 px-4 text-center font-medium text-muted-foreground">定价 (积分/1M)</th>
            <th class="h-10 px-4 text-center font-medium text-muted-foreground">绑定数</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">状态</th>
            <th class="h-10 px-4 text-right font-medium text-muted-foreground">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="m in models" :key="m.id" class="border-b hover:bg-gray-50 dark:hover:bg-gray-800/50">
            <td class="px-4 py-3 font-mono text-xs font-medium">{{ m.name }}</td>
            <td class="px-4 py-3 text-muted-foreground">{{ m.display_name || '—' }}</td>
            <td class="px-4 py-3 text-center text-xs">
              <div>
                <span class="text-muted-foreground">入</span> {{ m.prompt_credits_per_1m }}
                <span class="text-muted-foreground"> / 出</span> {{ m.completion_credits_per_1m }}
              </div>
              <div v-if="m.cache_read_credits_per_1m || m.cache_write_credits_per_1m" class="text-xs text-muted-foreground mt-0.5">
                <span v-if="m.cache_read_credits_per_1m">缓存 {{ m.cache_read_credits_per_1m }}</span>
                <span v-if="m.cache_read_credits_per_1m && m.cache_write_credits_per_1m"> / </span>
                <span v-if="m.cache_write_credits_per_1m">写 {{ m.cache_write_credits_per_1m }}</span>
              </div>
            </td>
            <td class="px-4 py-3 text-center">
              <Badge :variant="m.bindings.length ? 'secondary' : 'destructive'">{{ m.bindings.length }}</Badge>
            </td>
            <td class="px-4 py-3">
              <Badge :variant="m.enabled ? 'success' : 'secondary'">{{ m.enabled ? '启用' : '停用' }}</Badge>
            </td>
            <td class="px-4 py-3">
              <div class="flex items-center justify-end gap-1">
                <button class="p-1.5 rounded hover:bg-accent text-muted-foreground" title="绑定管理" @click="openBindings(m)">
                  <Link2 class="w-4 h-4" />
                </button>
                <button v-if="isAdmin" class="p-1.5 rounded hover:bg-accent text-muted-foreground" title="编辑" @click="openEdit(m)">
                  <Pencil class="w-4 h-4" />
                </button>
                <button v-if="isAdmin" class="p-1.5 rounded hover:bg-red-50 text-destructive" title="删除" @click="removeModel(m)">
                  <Trash2 class="w-4 h-4" />
                </button>
              </div>
            </td>
          </tr>
          <tr v-if="models.length === 0">
            <td colspan="6" class="text-center py-12 text-muted-foreground">暂无模型，可「获取模型」从供应商导入，或手动添加</td>
          </tr>
        </tbody>
      </table>
    </Card>

    <!-- 模型表单 -->
    <div v-if="showForm" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" @click.self="showForm = false">
      <Card class="w-full max-w-lg p-6 max-h-[90vh] overflow-auto">
        <h2 class="text-lg font-semibold mb-4">{{ editingId ? '编辑模型' : '添加模型' }}</h2>
        <div class="space-y-4">
          <div class="space-y-2">
            <Label>模型名（客户端请求时填写）</Label>
            <Input v-model="form.name" :disabled="!!editingId" placeholder="例如: gpt-4o" />
          </div>
          <div class="space-y-2"><Label>展示名</Label><Input v-model="form.display_name" placeholder="例如: GPT-4o" /></div>
          <div class="space-y-2"><Label>描述</Label><Input v-model="form.description" placeholder="可选" /></div>
          <div class="grid grid-cols-2 gap-4">
            <div class="space-y-2"><Label>输入积分 / 1M token</Label><Input v-model.number="form.prompt_credits_per_1m" type="number" /></div>
            <div class="space-y-2"><Label>输出积分 / 1M token</Label><Input v-model.number="form.completion_credits_per_1m" type="number" /></div>
            <div class="space-y-2">
              <Label>缓存命中积分 / 1M token</Label>
              <Input v-model.number="form.cache_read_credits_per_1m" type="number" />
              <p class="text-xs text-muted-foreground">留 0 表示按输入原价计费（不给折扣）。设成低于输入价即可对命中缓存的 token 打折。</p>
            </div>
            <div class="space-y-2">
              <Label>缓存写入积分 / 1M token</Label>
              <Input v-model.number="form.cache_write_credits_per_1m" type="number" />
              <p class="text-xs text-muted-foreground">留 0 表示按输入原价计费。Anthropic 写缓存比原价更贵，需要时填高于输入价的值。</p>
            </div>
          </div>
          <label class="flex items-center gap-2 text-sm">
            <input type="checkbox" v-model="form.enabled" class="rounded border-input" /> 启用该模型
          </label>
        </div>
        <div class="flex justify-end gap-2 mt-6">
          <Button class="px-4 h-9 border" @click="showForm = false">取消</Button>
          <Button class="px-4 h-9 bg-primary text-primary-foreground" @click="saveModel">保存</Button>
        </div>
      </Card>
    </div>

    <!-- 绑定管理 -->
    <div v-if="showBindings && bindingModel" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" @click.self="showBindings = false">
      <Card class="w-full max-w-3xl p-6 max-h-[90vh] overflow-auto">
        <h2 class="text-lg font-semibold mb-1">绑定管理 — <span class="font-mono">{{ bindingModel.name }}</span></h2>
        <p class="text-sm text-muted-foreground mb-4">一个模型可绑定多个供应商实现容灾；按优先级分层、同层加权随机选择。</p>

        <table class="w-full text-sm mb-4">
          <thead>
            <tr class="border-b">
              <th class="h-9 px-3 text-left font-medium text-muted-foreground">供应商</th>
              <th class="h-9 px-3 text-left font-medium text-muted-foreground">上游模型</th>
              <th class="h-9 px-3 text-center font-medium text-muted-foreground">权重/优先级</th>
              <th class="h-9 px-3 text-center font-medium text-muted-foreground">状态</th>
              <th class="h-9 px-3 text-right font-medium text-muted-foreground">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="b in bindings" :key="b.id" class="border-b">
              <td class="px-3 py-2">{{ b.vendor_name }} <Badge variant="secondary" class="text-xs">{{ b.vendor_protocol }}</Badge></td>
              <td class="px-3 py-2 font-mono text-xs">{{ b.upstream_model }}</td>
              <td class="px-3 py-2 text-center text-xs">{{ b.weight }} / {{ b.priority }}</td>
              <td class="px-3 py-2 text-center"><Badge :variant="b.enabled ? 'success' : 'secondary'" class="text-xs">{{ b.enabled ? '启用' : '停用' }}</Badge></td>
              <td class="px-3 py-2 text-right">
                <button v-if="isAdmin" class="p-1 rounded hover:bg-accent text-muted-foreground" @click="editBinding(b)"><Pencil class="w-4 h-4" /></button>
                <button v-if="isAdmin" class="p-1 rounded hover:bg-red-50 text-destructive" @click="removeBinding(b)"><Trash2 class="w-4 h-4" /></button>
              </td>
            </tr>
            <tr v-if="bindings.length === 0"><td colspan="5" class="text-center py-6 text-muted-foreground">暂无绑定</td></tr>
          </tbody>
        </table>

        <div v-if="isAdmin" class="p-4 rounded-md bg-secondary/30 space-y-3">
          <h3 class="font-medium text-sm">{{ bindingForm.id ? '编辑绑定' : '添加绑定' }}</h3>
          <div class="grid grid-cols-2 gap-3">
            <div class="space-y-1">
              <Label>供应商</Label>
              <select v-model.number="bindingForm.vendor_id" class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm">
                <option v-for="v in vendors" :key="v.id" :value="v.id">{{ v.name }} ({{ v.protocol }})</option>
              </select>
            </div>
            <div class="space-y-1"><Label>上游模型名</Label><Input v-model="bindingForm.upstream_model" placeholder="例如: gpt-4o-2024-08-06" /></div>
          </div>
          <div class="space-y-1"><Label>默认参数 JSON（客户端未设置时生效）</Label><Input v-model="bindingForm.params" placeholder='{"temperature":0.7,"max_tokens":2048}' /></div>
          <div class="grid grid-cols-3 gap-3">
            <div class="space-y-1"><Label>权重</Label><Input v-model.number="bindingForm.weight" type="number" /></div>
            <div class="space-y-1"><Label>优先级</Label><Input v-model.number="bindingForm.priority" type="number" /></div>
            <div class="space-y-1"><Label>启用</Label>
              <select v-model="bindingForm.enabled" class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm">
                <option :value="true">启用</option><option :value="false">停用</option>
              </select>
            </div>
          </div>
          <div class="flex gap-2">
            <Button class="px-4 h-8 bg-primary text-primary-foreground text-sm" @click="saveBinding">{{ bindingForm.id ? '更新绑定' : '添加绑定' }}</Button>
            <Button v-if="bindingForm.id" class="px-4 h-8 border text-sm" @click="resetBindingForm">取消编辑</Button>
          </div>
        </div>

        <div class="flex justify-end mt-4">
          <Button class="px-4 h-9 border" @click="showBindings = false">关闭</Button>
        </div>
      </Card>
    </div>

    <!-- 发现导入 -->
    <div v-if="showDiscover" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" @click.self="showDiscover = false">
      <Card class="w-full max-w-2xl p-6 max-h-[90vh] overflow-auto">
        <h2 class="text-lg font-semibold mb-4">获取模型</h2>
        <div class="flex items-end gap-2 mb-4">
          <div class="space-y-1 flex-1">
            <Label>选择供应商</Label>
            <select v-model.number="discoverVendorId" class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm">
              <option v-for="v in vendors" :key="v.id" :value="v.id">{{ v.name }} ({{ v.protocol }})</option>
            </select>
          </div>
          <Button class="h-9 px-4 bg-primary text-primary-foreground" :disabled="discovering" @click="doDiscover">
            <Search class="w-4 h-4" />{{ discovering ? '发现中...' : '发现模型' }}
          </Button>
        </div>

        <div v-if="discovered.length" class="mb-4">
          <div class="flex items-center justify-between mb-2">
            <span class="text-sm text-muted-foreground">发现 {{ discovered.length }} 个上游模型，已选 {{ selectedModels.size }}</span>
            <button class="text-xs text-primary hover:underline" @click="selectAll">全选</button>
          </div>
          <div class="max-h-64 overflow-auto border rounded-md p-2 grid grid-cols-1 sm:grid-cols-2 gap-1">
            <label v-for="m in discovered" :key="m" class="flex items-center gap-2 text-xs font-mono px-2 py-1 rounded hover:bg-accent cursor-pointer">
              <input type="checkbox" :checked="selectedModels.has(m)" @change="toggleSelect(m)" class="rounded border-input" />
              {{ m }}
            </label>
          </div>
        </div>

        <p v-if="importResult" class="text-sm text-green-700 mb-3">{{ importResult }}</p>

        <div class="flex justify-end gap-2">
          <Button class="px-4 h-9 border" @click="showDiscover = false">关闭</Button>
          <Button v-if="isAdmin" class="px-4 h-9 bg-primary text-primary-foreground" :disabled="selectedModels.size === 0" @click="doImport">
            导入选中（{{ selectedModels.size }}）
          </Button>
        </div>
      </Card>
    </div>
  </div>
</template>
