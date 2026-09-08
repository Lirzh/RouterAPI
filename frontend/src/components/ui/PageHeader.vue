<script setup lang="ts">
import { cn } from '@/lib/utils'
</script>

<template>
  <div :class="cn('mb-4 flex items-center justify-between', $attrs.class as string)">
    <div>
      <h1 class="text-2xl font-bold tracking-tight">
        <slot name="title" />
      </h1>
      <p v-if="$slots.description" class="text-sm text-muted-foreground mt-1">
        <slot name="description" />
      </p>
    </div>
    <div class="flex items-center gap-2">
      <slot name="actions" />
    </div>
  </div>
</template>
