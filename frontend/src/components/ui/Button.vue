<script setup lang="ts">
import { cn } from '@/lib/utils'
</script>

<template>
  <button
    :class="cn(
      'inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 disabled:pointer-events-none disabled:opacity-50',
      $attrs.class as string
    )"
  >
    <slot />
  </button>
</template>
