<script setup lang="ts">
import { ref, onMounted, reactive, computed } from 'vue'
import api from '@/lib/api'
import PageHeader from '@/components/ui/PageHeader.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Badge from '@/components/ui/Badge.vue'
import {
  Plus,
  Pencil,
  Trash2,
  KeyRound,
  Search,
  ChevronLeft,
  ChevronRight,
  ShieldCheck,
  ShieldAlert,
} from 'lucide-vue-next'

interface User {
  id: number
  username: string
  email: string
  role: string
  status: number
  credits: number
  last_login_at: string | null
  created_at: string
}

const users = ref<User[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const loading = ref(false)

const filters = reactive({
  keyword: '',
  role: '',
  status: '',
})

const showForm = ref(false)
const editingId = ref<number | null>(null)
const showResetForm = ref(false)
const resetTarget = ref<User | null>(null)
const newPassword = ref('')

const form = reactive({
  username: '',
  password: '',
  email: '',
  role: 'viewer',
})

const currentUserId = computed(() => {
  // 从 localStorage 获取当前用户信息来比较
  return parseInt(localStorage.getItem('user_id') || '0')
})

onMounted(() => {
  loadUsers()
})

async function loadUsers() {
  loading.value = true
  try {
    const params: any = {
      page: page.value,
      page_size: pageSize.value,
    }
    if (filters.keyword) params.keyword = filters.keyword
    if (filters.role) params.role = filters.role
    if (filters.status) params.status = filters.status

    const { data } = await api.get('/admin/users', { params })
    users.value = data.data
    total.value = data.total
  } finally {
    loading.value = false
  }
}

function applyFilters() {
  page.value = 1
  loadUsers()
}

function openCreate() {
  editingId.value = null
  form.username = ''
  form.password = ''
  form.email = ''
  form.role = 'viewer'
  showForm.value = true
}

function openEdit(u: User) {
  editingId.value = u.id
  form.username = u.username
  form.password = ''
  form.email = u.email || ''
  form.role = u.role
  showForm.value = true
}

async function save() {
  if (editingId.value) {
    // 编辑模式：只更新 email/role/status
    await api.put(`/admin/users/${editingId.value}`, {
      email: form.email,
      role: form.role,
    })
  } else {
    // 创建模式
    await api.post('/admin/users', {
      username: form.username,
      password: form.password,
      email: form.email,
      role: form.role,
    })
  }
  showForm.value = false
  await loadUsers()
}

async function remove(u: User) {
  if (!confirm(`确定删除用户「${u.username}」？此操作不可恢复。`)) return
  try {
    await api.delete(`/admin/users/${u.id}`)
    await loadUsers()
  } catch (e: any) {
    alert(e.response?.data?.error || '删除失败')
  }
}

function openReset(u: User) {
  resetTarget.value = u
  newPassword.value = ''
  showResetForm.value = true
}

async function doReset() {
  if (!resetTarget.value) return
  try {
    await api.put(`/admin/users/${resetTarget.value.id}/reset-password`, {
      password: newPassword.value,
    })
    showResetForm.value = false
    resetTarget.value = null
    alert('密码已重置')
  } catch (e: any) {
    alert(e.response?.data?.error || '重置失败')
  }
}

async function toggleStatus(u: User) {
  const newStatus = u.status === 1 ? 0 : 1
  try {
    await api.put(`/admin/users/${u.id}`, { status: newStatus })
    await loadUsers()
  } catch (e: any) {
    alert(e.response?.data?.error || '操作失败')
  }
}

function prevPage() {
  if (page.value > 1) {
    page.value--
    loadUsers()
  }
}

function nextPage() {
  if (page.value * pageSize.value < total.value) {
    page.value++
    loadUsers()
  }
}

const totalPages = () => Math.ceil(total.value / pageSize.value)
</script>

<template>
  <div>
    <PageHeader>
      <template #title>用户管理</template>
      <template #description>管理系统用户，分配角色和权限</template>
      <template #actions>
        <Button class="bg-primary text-primary-foreground px-4 h-9" @click="openCreate">
          <Plus class="w-4 h-4" />
          添加用户
        </Button>
      </template>
    </PageHeader>

    <!-- 筛选 -->
    <Card class="p-4 mb-4">
      <div class="grid grid-cols-1 md:grid-cols-4 gap-3">
        <Input v-model="filters.keyword" placeholder="用户名或邮箱" @keyup.enter="applyFilters" />
        <select
          v-model="filters.role"
          class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm"
        >
          <option value="">全部角色</option>
          <option value="admin">管理员</option>
          <option value="viewer">查看者</option>
        </select>
        <select
          v-model="filters.status"
          class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm"
        >
          <option value="">全部状态</option>
          <option value="1">启用</option>
          <option value="0">禁用</option>
        </select>
        <Button class="h-9 bg-primary text-primary-foreground px-4" @click="applyFilters">
          <Search class="w-4 h-4" />
          搜索
        </Button>
      </div>
    </Card>

    <!-- 用户列表 -->
    <Card class="p-0">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b">
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">用户名</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">邮箱</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">角色</th>
            <th class="h-10 px-4 text-right font-medium text-muted-foreground">积分</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">状态</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">最后登录</th>
            <th class="h-10 px-4 text-left font-medium text-muted-foreground">创建时间</th>
            <th class="h-10 px-4 text-right font-medium text-muted-foreground">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in users" :key="u.id" class="border-b hover:bg-gray-50 dark:hover:bg-gray-800/50">
            <td class="px-4 py-3 font-medium">
              <div class="flex items-center gap-2">
                <div class="w-7 h-7 rounded-full bg-primary text-primary-foreground flex items-center justify-center text-xs font-medium">
                  {{ u.username.charAt(0).toUpperCase() }}
                </div>
                {{ u.username }}
              </div>
            </td>
            <td class="px-4 py-3 text-muted-foreground">{{ u.email || '—' }}</td>
            <td class="px-4 py-3">
              <Badge :variant="u.role === 'admin' ? 'default' : 'secondary'" class="text-xs">
                <component :is="u.role === 'admin' ? ShieldCheck : ShieldAlert" class="w-3 h-3 mr-1" />
                {{ u.role === 'admin' ? '管理员' : '查看者' }}
              </Badge>
            </td>
            <td class="px-4 py-3 text-right font-mono text-xs">{{ new Intl.NumberFormat().format(u.credits || 0) }}</td>
            <td class="px-4 py-3">
              <button @click="toggleStatus(u)" class="inline-flex">
                <Badge :variant="u.status === 1 ? 'success' : 'destructive'" class="text-xs cursor-pointer">
                  {{ u.status === 1 ? '启用' : '禁用' }}
                </Badge>
              </button>
            </td>
            <td class="px-4 py-3 text-xs text-muted-foreground">
              {{ u.last_login_at ? new Date(u.last_login_at).toLocaleString() : '—' }}
            </td>
            <td class="px-4 py-3 text-xs text-muted-foreground">
              {{ new Date(u.created_at).toLocaleDateString() }}
            </td>
            <td class="px-4 py-3">
              <div class="flex items-center justify-end gap-1">
                <button
                  class="p-1.5 rounded hover:bg-accent text-muted-foreground"
                  title="重置密码"
                  @click="openReset(u)"
                >
                  <KeyRound class="w-4 h-4" />
                </button>
                <button
                  class="p-1.5 rounded hover:bg-accent text-muted-foreground"
                  title="编辑"
                  @click="openEdit(u)"
                >
                  <Pencil class="w-4 h-4" />
                </button>
                <button
                  v-if="u.id !== currentUserId"
                  class="p-1.5 rounded hover:bg-red-50 text-destructive"
                  title="删除"
                  @click="remove(u)"
                >
                  <Trash2 class="w-4 h-4" />
                </button>
              </div>
            </td>
          </tr>
          <tr v-if="users.length === 0 && !loading">
            <td colspan="8" class="text-center py-12 text-muted-foreground">
              暂无用户数据
            </td>
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

    <!-- 新增/编辑弹窗 -->
    <div v-if="showForm" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" @click.self="showForm = false">
      <Card class="w-full max-w-lg p-6">
        <h2 class="text-lg font-semibold mb-4">{{ editingId ? '编辑用户' : '添加用户' }}</h2>

        <div class="space-y-4">
          <div class="space-y-2">
            <Label>用户名</Label>
            <Input v-model="form.username" :disabled="!!editingId" placeholder="登录用户名" />
            <p v-if="!editingId" class="text-xs text-muted-foreground">创建后不可修改</p>
          </div>

          <div v-if="!editingId" class="space-y-2">
            <Label>密码</Label>
            <Input v-model="form.password" type="password" placeholder="设置初始密码" />
          </div>

          <div class="space-y-2">
            <Label>邮箱</Label>
            <Input v-model="form.email" type="email" placeholder="user@example.com" />
          </div>

          <div class="space-y-2">
            <Label>角色</Label>
            <select
              v-model="form.role"
              class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm"
            >
              <option value="admin">管理员 — 可管理供应商、模型、用户、密钥等全部资源</option>
              <option value="viewer">查看者 — 仅可查看仪表盘和日志</option>
            </select>
          </div>
        </div>

        <div class="flex justify-end gap-2 mt-6">
          <Button class="px-4 h-9 border" @click="showForm = false">取消</Button>
          <Button class="px-4 h-9 bg-primary text-primary-foreground" @click="save">
            {{ editingId ? '保存' : '创建' }}
          </Button>
        </div>
      </Card>
    </div>

    <!-- 重置密码弹窗 -->
    <div v-if="showResetForm" class="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" @click.self="showResetForm = false">
      <Card class="w-full max-w-md p-6">
        <h2 class="text-lg font-semibold mb-2">重置密码</h2>
        <p class="text-sm text-muted-foreground mb-4">
          为用户「{{ resetTarget?.username }}」设置新密码
        </p>

        <div class="space-y-2">
          <Label>新密码</Label>
          <Input v-model="newPassword" type="password" placeholder="输入新密码" />
        </div>

        <div class="flex justify-end gap-2 mt-6">
          <Button class="px-4 h-9 border" @click="showResetForm = false">取消</Button>
          <Button class="px-4 h-9 bg-primary text-primary-foreground" :disabled="!newPassword" @click="doReset">
            确认重置
          </Button>
        </div>
      </Card>
    </div>
  </div>
</template>
