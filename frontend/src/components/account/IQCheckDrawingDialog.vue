<template>
  <BaseDialog :show="show" :title="t('admin.accounts.iqCheck.drawing.title')" width="wide" @close="emit('close')">
    <div v-if="drawing" class="space-y-3" data-testid="iq-check-drawing-dialog">
      <div class="flex aspect-[4/3] items-center justify-center overflow-hidden rounded-lg bg-gray-50 dark:bg-dark-700">
        <!-- Only <img>: the browser runs no script and loads nothing external from an SVG image. -->
        <img v-if="drawing.svg" :src="iqCheckDrawingSrc(drawing.svg)" :alt="t('admin.accounts.iqCheck.drawing.title')" class="h-full w-full object-contain" data-testid="iq-check-drawing-large" />
        <p v-else class="p-4 text-sm text-red-600 dark:text-red-400">{{ t('admin.accounts.iqCheck.drawing.failed') }}</p>
      </div>
      <p class="text-sm text-gray-600 dark:text-gray-300">
        {{ t('admin.accounts.iqCheck.drawing.meta', { model: drawing.model || '-', seconds: iqCheckDrawingSeconds(drawing), time: formatDateTime(drawing.drawn_at) }) }}
      </p>
      <p v-if="drawing.error" class="break-all text-sm text-red-600 dark:text-red-400">
        {{ t('admin.accounts.iqCheck.details.error', { error: drawing.error }) }}
      </p>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { formatDateTime } from '@/utils/format'
import { iqCheckDrawingSeconds, iqCheckDrawingSrc } from '@/utils/iqCheckDrawing'
import type { IQCheckDrawing } from '@/types'

defineProps<{ show: boolean; drawing: IQCheckDrawing | null }>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()
</script>
