import { ref, watch } from 'vue'

type Theme = 'light' | 'dark' | 'system'

const STORAGE_KEY = 'routerapi-theme'

function getSystemTheme(): 'light' | 'dark' {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function getResolved(theme: Theme): 'light' | 'dark' {
  if (theme === 'system') return getSystemTheme()
  return theme
}

function applyTheme(theme: 'light' | 'dark') {
  const root = document.documentElement
  if (theme === 'dark') {
    root.classList.add('dark')
  } else {
    root.classList.remove('dark')
  }
}

const stored = localStorage.getItem(STORAGE_KEY) as Theme | null
export const theme = ref<Theme>(stored || 'system')

// 初始应用
applyTheme(getResolved(theme.value))

// 监听系统主题变化
if (window.matchMedia) {
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    if (theme.value === 'system') {
      applyTheme(getResolved('system'))
    }
  })
}

export function setTheme(t: Theme) {
  theme.value = t
  localStorage.setItem(STORAGE_KEY, t)
  applyTheme(getResolved(t))
}

export function toggleTheme() {
  const current = getResolved(theme.value)
  setTheme(current === 'dark' ? 'light' : 'dark')
}

export function getResolvedTheme(): 'light' | 'dark' {
  return getResolved(theme.value)
}

// 持续监听 theme 变化
watch(theme, (t) => {
  applyTheme(getResolved(t))
})
