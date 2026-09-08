<script setup lang="ts">
import { cn } from '@/lib/utils'

defineProps<{
  variant?: 'default' | 'success' | 'warning' | 'destructive' | 'secondary'
}>()
</script>

<template>
  <span
    :class="cn(
      'inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium transition-colors',
      variant === 'default' && 'bg-primary text-primary-foreground',
      variant === 'success' && 'bg-green-100 text-green-700 dark:bg-green-950 dark:text-green-400',
      variant === 'warning' && 'bg-yellow-100 text-yellow-700 dark:bg-yellow-950 dark:text-yellow-400',
      variant === 'destructive' && 'bg-red-100 text-red-700 dark:bg-red-950 dark:text-red-400',
      variant === 'secondary' && 'bg-secondary text-secondary-foreground',
      !variant && 'bg-secondary text-secondary-foreground',
      $attrs.class as string
    )"
  >
    <slot />
  </span>
</template>
