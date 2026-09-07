<script setup>
import { statusTagType, statusLabel } from '@/lib/status'

defineProps({
  status: { type: String, default: '' },
  size: { type: String, default: 'small' },
})
</script>

<template>
  <el-tag :type="statusTagType(status)" :size="size" disable-transitions>
    {{ statusLabel(status) }}
  </el-tag>
</template>
