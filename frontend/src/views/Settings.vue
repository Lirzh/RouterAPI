<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import api from '@/lib/api'
import PageHeader from '@/components/ui/PageHeader.vue'
import Card from '@/components/ui/Card.vue'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Badge from '@/components/ui/Badge.vue'
import { Save, KeyRound, Mail } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const router = useRouter()

interface Profile {
  id: number
  username: string
  email: string
  role: string
  status: number
  last_login_at: string | null
  created_at: string
}

const profile = ref<Profile | null>(null)
const loading = ref(true)

// 邮箱表单
const emailForm = reactive({
  email: '',
})
const emailSaving = ref(false)
const emailMsg = ref('')

// 密码表单
const pwdForm = reactive({
  old_password: '',
  new_password: '',
  confirm_password: '',
})
const pwdSaving = ref(false)
const pwdMsg = ref('')
const pwdError = ref('')

onMounted(async () => {
  try {
    const { data } = await api.get('/admin/profile')
    profile.value = data.data
    emailForm.email = data.data.email || ''
    // 存储 user_id 供 Users 页面比较
    localStorage.setItem('user_id', String(data.data.id))
  } finally {
    loading.value = false
  }
})

async function saveEmail() {
  emailSaving.value = true
  emailMsg.value = ''
  try {
    const { data } = await api.put('/admin/profile', { email: emailForm.email })
    profile.value = data.data
    emailMsg.value = '邮箱已更新'
    setTimeout(() => { emailMsg.value = '' }, 3000)
  } catch (e: any) {
    emailMsg.value = e.response?.data?.error || '保存失败'
  } finally {
    emailSaving.value = false
  }
}

async function changePassword() {
  pwdError.value = ''
  pwdMsg.value = ''

  // 前端校验
  if (pwdForm.new_password !== pwdForm.confirm_password) {
    pwdError.value = '两次输入的新密码不一致'
    return
  }
  if (pwdForm.new_password.length < 6) {
    pwdError.value = '新密码至少 6 位'
    return
  }

  pwdSaving.value = true
  try {
    await api.put('/admin/profile/password', {
      old_password: pwdForm.old_password,
      new_password: pwdForm.new_password,
    })
    pwdMsg.value = '密码修改成功，正在跳转登录…'
    pwdForm.old_password = ''
    pwdForm.new_password = ''
    pwdForm.confirm_password = ''
    // 改密后旧令牌已被服务端撤销（TokenVersion 递增），需重新登录
    setTimeout(() => {
      auth.logout()
      router.push('/login')
    }, 1200)
  } catch (e: any) {
    pwdError.value = e.response?.data?.error || '修改失败'
  } finally {
    pwdSaving.value = false
  }
}
</script>

<template>
  <div>
    <PageHeader>
      <template #title>个人设置</template>
      <template #description>管理你的账户信息和密码</template>
    </PageHeader>

    <div v-if="loading" class="text-center py-12 text-muted-foreground">加载中...</div>

    <div v-else class="space-y-6 max-w-2xl">
      <!-- 账户信息 -->
      <Card class="p-6">
        <h3 class="font-semibold mb-4">账户信息</h3>
        <div class="grid grid-cols-2 gap-4 text-sm">
          <div>
            <label class="text-muted-foreground text-xs">用户名</label>
            <p class="font-medium mt-1">{{ profile?.username }}</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">角色</label>
            <div class="mt-1">
              <Badge :variant="profile?.role === 'admin' ? 'default' : 'secondary'" class="text-xs">
                {{ profile?.role === 'admin' ? '管理员' : '查看者' }}
              </Badge>
            </div>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">账户状态</label>
            <div class="mt-1">
              <Badge :variant="profile?.status === 1 ? 'success' : 'destructive'" class="text-xs">
                {{ profile?.status === 1 ? '正常' : '已禁用' }}
              </Badge>
            </div>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">最后登录</label>
            <p class="mt-1">{{ profile?.last_login_at ? new Date(profile.last_login_at).toLocaleString() : '—' }}</p>
          </div>
          <div>
            <label class="text-muted-foreground text-xs">创建时间</label>
            <p class="mt-1">{{ profile?.created_at ? new Date(profile.created_at).toLocaleString() : '—' }}</p>
          </div>
        </div>
      </Card>

      <!-- 邮箱设置 -->
      <Card class="p-6">
        <div class="flex items-center gap-2 mb-4">
          <Mail class="w-5 h-5 text-muted-foreground" />
          <h3 class="font-semibold">邮箱设置</h3>
        </div>

        <div class="space-y-3">
          <div class="space-y-2">
            <Label>邮箱地址</Label>
            <Input v-model="emailForm.email" type="email" placeholder="user@example.com" />
          </div>

          <div v-if="emailMsg" class="text-sm" :class="emailMsg.includes('失败') ? 'text-destructive' : 'text-green-600'">
            {{ emailMsg }}
          </div>

          <Button class="h-9 px-4 bg-primary text-primary-foreground" :disabled="emailSaving" @click="saveEmail">
            <Save class="w-4 h-4" />
            {{ emailSaving ? '保存中...' : '保存邮箱' }}
          </Button>
        </div>
      </Card>

      <!-- 修改密码 -->
      <Card class="p-6">
        <div class="flex items-center gap-2 mb-4">
          <KeyRound class="w-5 h-5 text-muted-foreground" />
          <h3 class="font-semibold">修改密码</h3>
        </div>

        <div class="space-y-3">
          <div class="space-y-2">
            <Label>当前密码</Label>
            <Input v-model="pwdForm.old_password" type="password" placeholder="输入当前密码" />
          </div>

          <div class="space-y-2">
            <Label>新密码</Label>
            <Input v-model="pwdForm.new_password" type="password" placeholder="至少 6 位" />
          </div>

          <div class="space-y-2">
            <Label>确认新密码</Label>
            <Input v-model="pwdForm.confirm_password" type="password" placeholder="再次输入新密码" />
          </div>

          <div v-if="pwdError" class="text-sm text-destructive">
            {{ pwdError }}
          </div>
          <div v-if="pwdMsg" class="text-sm text-green-600">
            {{ pwdMsg }}
          </div>

          <Button class="h-9 px-4 bg-primary text-primary-foreground" :disabled="pwdSaving" @click="changePassword">
            <KeyRound class="w-4 h-4" />
            {{ pwdSaving ? '修改中...' : '修改密码' }}
          </Button>
        </div>
      </Card>
    </div>
  </div>
</template>
