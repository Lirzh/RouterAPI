<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, RouterView, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import {
  LayoutDashboard,
  Server,
  GitBranch,
  Key,
  Wallet,
  ScrollText,
  ShieldCheck,
  LogOut,
  Users,
  Settings,
  Sun,
  Moon,
  Monitor,
} from 'lucide-vue-next'
import { theme, setTheme, getResolvedTheme } from '@/lib/theme'

const auth = useAuthStore()
const router = useRouter()

function handleLogout() {
  auth.logout()
  router.push('/login')
}

interface NavItem {
  to: string
  label: string
  icon: any
  adminOnly?: boolean
}

const allNavItems: NavItem[] = [
  { to: '/dashboard', label: '仪表盘', icon: LayoutDashboard },
  { to: '/vendors', label: '供应商', icon: Server },
  { to: '/models', label: '模型', icon: GitBranch },
  { to: '/api-keys', label: 'API Key', icon: Key },
  { to: '/wallet', label: '积分钱包', icon: Wallet },
  { to: '/logs', label: '调用日志', icon: ScrollText },
  { to: '/audit-logs', label: '操作审计', icon: ShieldCheck, adminOnly: true },
  { to: '/users', label: '用户管理', icon: Users, adminOnly: true },
  { to: '/settings', label: '个人设置', icon: Settings },
]

const navItems = allNavItems.filter(item => !item.adminOnly || auth.role === 'admin')

const isDark = computed(() => getResolvedTheme() === 'dark')

const themeOptions = [
  { value: 'light' as const, label: '浅色', icon: Sun },
  { value: 'dark' as const, label: '深色', icon: Moon },
  { value: 'system' as const, label: '跟随系统', icon: Monitor },
]

const showThemeMenu = ref(false)

import { ref } from 'vue'

function toggleThemeMenu() {
  showThemeMenu.value = !showThemeMenu.value
}

function selectTheme(t: 'light' | 'dark' | 'system') {
  setTheme(t)
  showThemeMenu.value = false
}

// 点击外部关闭菜单
function closeThemeMenu() {
  showThemeMenu.value = false
}
</script>

<template>
  <div class="flex h-screen bg-background" @click="showThemeMenu && closeThemeMenu()">
    <!-- 侧边栏 -->
    <aside class="w-60 bg-card border-r flex flex-col">
      <!-- Logo -->
      <div class="h-14 flex items-center justify-between px-6 border-b">
        <span class="text-lg font-bold">RouterAPI</span>
        <!-- 主题切换 -->
        <div class="relative" @click.stop>
          <button
            @click="toggleThemeMenu"
            class="p-1.5 rounded-md text-muted-foreground hover:bg-accent transition-colors"
            :title="isDark ? '切换到浅色' : '切换到深色'"
          >
            <Sun v-if="isDark" class="w-4 h-4" />
            <Moon v-else class="w-4 h-4" />
          </button>
          <!-- 下拉菜单 -->
          <div
            v-if="showThemeMenu"
            class="absolute right-0 top-full mt-1 w-36 bg-card border rounded-md shadow-lg z-50 py-1"
          >
            <button
              v-for="opt in themeOptions"
              :key="opt.value"
              @click="selectTheme(opt.value)"
              class="flex items-center gap-2 w-full px-3 py-1.5 text-sm hover:bg-accent transition-colors"
              :class="theme === opt.value ? 'font-medium text-foreground' : 'text-muted-foreground'"
            >
              <component :is="opt.icon" class="w-4 h-4" />
              {{ opt.label }}
              <span v-if="theme === opt.value" class="ml-auto text-xs">✓</span>
            </button>
          </div>
        </div>
      </div>

      <!-- 导航 -->
      <nav class="flex-1 p-3 space-y-1">
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="flex items-center gap-3 px-3 py-2 rounded-md text-sm font-medium transition-colors"
          :class="[
            $route.path === item.to
              ? 'bg-primary text-primary-foreground'
              : 'text-muted-foreground hover:bg-accent hover:text-accent-foreground'
          ]"
        >
          <component :is="item.icon" class="w-4 h-4" />
          {{ item.label }}
        </RouterLink>
      </nav>

      <!-- 用户信息 -->
      <div class="p-3 border-t">
        <div class="flex items-center gap-3 px-3 py-2">
          <div class="w-8 h-8 rounded-full bg-primary text-primary-foreground flex items-center justify-center text-sm font-medium">
            {{ auth.username.charAt(0).toUpperCase() }}
          </div>
          <div class="flex-1 min-w-0">
            <p class="text-sm font-medium truncate">{{ auth.username }}</p>
            <p class="text-xs text-muted-foreground">
              {{ auth.role === 'admin' ? '管理员' : '查看者' }}
            </p>
          </div>
          <button
            @click="handleLogout"
            class="text-muted-foreground hover:text-destructive transition-colors"
            title="退出登录"
          >
            <LogOut class="w-4 h-4" />
          </button>
        </div>
      </div>
    </aside>

    <!-- 主内容 -->
    <main class="flex-1 overflow-auto bg-background">
      <div class="p-6">
        <RouterView />
      </div>
    </main>
  </div>
</template>
