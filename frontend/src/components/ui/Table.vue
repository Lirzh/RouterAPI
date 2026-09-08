<script setup lang="ts">
import { cn } from '@/lib/utils'

defineProps<{
  headers?: string[]
}>()

defineEmits<{
  rowClick: [row: any]
}>()
</script>

<template>
  <div :class="cn('w-full overflow-auto', $attrs.class as string)">
    <table class="w-full text-sm">
      <thead>
        <tr class="border-b">
          <th
            v-for="(header, i) in headers"
            :key="i"
            class="h-10 px-3 text-left font-medium text-muted-foreground"
          >
            {{ header }}
          </th>
        </tr>
      </thead>
      <tbody>
        <slot />
      </tbody>
    </table>
  </div>
</template>
