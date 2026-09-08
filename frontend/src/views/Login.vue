<script setup lang="ts">
import { ref, reactive } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import Button from '@/components/ui/Button.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import { Sun, Moon } from 'lucide-vue-next'
import { toggleTheme, getResolvedTheme } from '@/lib/theme'
import { computed } from 'vue'

const auth = useAuthStore()
const router = useRouter()

const form = reactive({
  username: '',
  password: '',
})

const loading = ref(false)
const error = ref('')
const isDark = computed(() => getResolvedTheme() === 'dark')

async function handleLogin() {
  loading.value = true
  error.value = ''
  try {
    await auth.login(form.username, form.password)
    router.push('/dashboard')
  } catch (e: any) {
    error.value = e.response?.data?.error || '登录失败'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="min-h-screen flex items-center justify-center bg-background relative">
    <!-- 主题切换 -->
    <button
      @click="toggleTheme()"
      class="absolute top-4 right-4 p-2 rounded-md text-muted-foreground hover:bg-accent transition-colors"
      :title="isDark ? '切换到浅色' : '切换到深色'"
    >
      <Sun v-if="isDark" class="w-5 h-5" />
      <Moon v-else class="w-5 h-5" />
    </button>
    <div class="w-full max-w-md">
      <div class="text-center mb-8">
        <h1 class="text-3xl font-bold">RouterAPI</h1>
        <p class="text-muted-foreground mt-2">AI 接口路由网关</p>
      </div>

      <div class="bg-card rounded-lg border shadow-sm p-6 space-y-4">
        <h2 class="text-xl font-semibold">登录</h2>

        <div v-if="error" class="bg-red-50 dark:bg-red-950 text-red-600 dark:text-red-400 text-sm p-3 rounded-md">
          {{ error }}
        </div>

        <form @submit.prevent="handleLogin" class="space-y-4">
          <div class="space-y-2">
            <Label>用户名</Label>
            <Input
              v-model="form.username"
              placeholder="请输入用户名"
              :disabled="loading"
            />
          </div>

          <div class="space-y-2">
            <Label>密码</Label>
            <Input
              v-model="form.password"
              type="password"
              placeholder="请输入密码"
              :disabled="loading"
            />
          </div>

          <Button
            type="submit"
            class="w-full bg-primary text-primary-foreground hover:bg-primary/90 h-10"
            :disabled="loading"
          >
            {{ loading ? '登录中...' : '登录' }}
          </Button>
        </form>

        <p class="text-xs text-muted-foreground text-center">
          默认账号: admin / admin123
        </p>
      </div>
    </div>
  </div>
</template>
