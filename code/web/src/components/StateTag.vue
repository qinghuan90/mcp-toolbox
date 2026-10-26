<script setup lang="ts">
import { computed } from 'vue'
const props = defineProps<{ value: string }>()
const names: Record<string,string> = { quarantined:'已隔离',accepted:'处理中',limited:'限流',available:'可用',ready:'可用',running:'运行中',stopped:'已停止',disabled:'已禁用',error:'故障',failed:'失败',fault:'故障',starting:'启动中',pending:'待执行',queued:'排队中',preparing:'准备中',success:'成功',succeeded:'成功',completed:'完成',denied:'已拒绝',rejected:'已拒绝',timeout:'超时',unknown:'结果未知',cancelled:'已取消',not_ready:'未准备',interrupted:'已中断' }
const type = computed(() => ['available','ready','success','succeeded','completed'].includes(props.value) ? 'success' : ['error','failed','fault','denied','rejected'].includes(props.value) ? 'danger' : ['quarantined','accepted','limited','running','starting','pending','preparing','queued','timeout','unknown'].includes(props.value) ? 'warning' : 'info')
</script>
<template><el-tag :type="type" effect="light" round size="small">{{ names[value] || value || '未知' }}</el-tag></template>
