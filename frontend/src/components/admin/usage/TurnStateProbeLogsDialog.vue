<template>
  <BaseDialog :show="show" :title="t('usage.probeLogs.title')" width="extra-wide" @close="$emit('close')">
    <div class="space-y-4">
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('usage.probeLogs.description') }}</p>
      <form class="flex flex-wrap items-end gap-3" @submit.prevent="loadPage()">
        <label class="block text-sm text-gray-700 dark:text-gray-200">
          {{ t('usage.probeLogs.account') }}
          <input v-model="account" data-testid="probe-account" type="number" min="1" step="1" class="input mt-1 block w-36" :placeholder="t('usage.probeLogs.all')" />
        </label>
        <label class="block min-w-0 text-sm text-gray-700 dark:text-gray-200">
          {{ t('usage.probeLogs.model') }}
          <select v-model="model" data-testid="probe-model" class="input mt-1 block w-full sm:w-44">
            <option value="">{{ t('usage.probeLogs.all') }}</option>
            <option value="gpt-6-astra">gpt-6-astra</option>
            <option value="gpt-5.6-sol">gpt-5.6-sol</option>
          </select>
        </label>
        <button type="submit" class="btn btn-primary min-h-11" :disabled="loading">{{ t('usage.probeLogs.filter') }}</button>
        <button type="button" data-testid="probe-refresh" class="btn btn-secondary min-h-11" :disabled="loading" @click="loadPage()">
          {{ t(loading ? 'usage.probeLogs.loading' : 'usage.probeLogs.refresh') }}
        </button>
        <label class="flex min-h-11 items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
          <input v-model="autoRefresh" data-testid="probe-auto" type="checkbox" class="checkbox" />
          {{ t('usage.probeLogs.autoRefresh') }}
        </label>
      </form>
      <div class="flex flex-wrap gap-x-4 gap-y-1 rounded-lg bg-gray-50 p-3 text-sm text-gray-700 dark:bg-dark-800 dark:text-gray-200">
        <span data-testid="probe-worker-status" class="font-medium">{{ t(workerOnline ? 'usage.probeLogs.online' : 'usage.probeLogs.offline') }}</span>
        <span v-if="page?.status">{{ t('usage.probeLogs.updated') }}: {{ formatTime(page.status.updated_at) }}</span>
        <span>{{ t('usage.probeLogs.retention', { count: page?.retention_limit ?? 2000 }) }}</span>
        <span v-if="cursor">{{ t('usage.probeLogs.historyPaused') }}</span>
      </div>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <div :aria-busy="loading" class="space-y-3">
        <p v-if="loading && !page" role="status" class="py-6 text-center text-gray-600 dark:text-gray-300">{{ t('usage.probeLogs.loading') }}</p>
        <p v-else-if="!page?.items.length && !error" class="py-6 text-center text-sm text-gray-600 dark:text-gray-300">{{ t('usage.probeLogs.empty') }}</p>
        <article v-for="item in page?.items ?? []" :key="item.id" data-testid="probe-event" class="rounded-xl border border-gray-200 p-4 text-sm dark:border-dark-600">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <div class="flex flex-wrap items-center gap-2 text-gray-900 dark:text-gray-100">
              <span class="font-semibold">{{ t('usage.probeLogs.account') }} #{{ item.account_id ?? '—' }}</span>
              <span class="break-all">{{ item.model ?? '—' }}</span>
              <span class="rounded bg-gray-100 px-2 py-1 text-xs dark:bg-dark-700">{{ resultLabel(item) }}</span>
            </div>
            <time :datetime="item.time" class="text-xs text-gray-600 dark:text-gray-300">{{ formatTime(item.time) }}</time>
          </div>
          <div class="mt-2 flex flex-wrap gap-x-4 gap-y-1 font-mono text-xs text-gray-700 dark:text-gray-200">
            <span>{{ t('usage.turnStateSent') }}: {{ item.sent_state_length ?? '—' }}</span>
            <span data-testid="probe-returned" :class="item.received_state_length === 312 ? 'font-semibold text-red-600 dark:text-red-400' : ''">
              {{ t('usage.turnStateReceived') }}: {{ item.received_state_length ?? '—' }}
            </span>
            <span>HTTP: {{ item.http_status ?? '—' }}</span>
            <span>{{ t('usage.probeLogs.route') }}: {{ item.route ?? '—' }}</span>
            <span>{{ t('usage.probeLogs.attempt') }}: {{ item.attempt ?? '—' }}</span>
            <span>{{ item.elapsed_s ?? '—' }} s</span>
          </div>
          <dl class="mt-2 grid gap-x-6 gap-y-1 break-words text-xs text-gray-600 dark:text-gray-300 sm:grid-cols-2">
            <div v-if="item.response_model"><dt class="inline">{{ t('usage.probeLogs.responseModel') }}: </dt><dd class="inline">{{ item.response_model }}</dd></div>
            <div v-if="item.error_code || item.error_type || item.transient_error"><dt class="inline">{{ t('usage.probeLogs.error') }}: </dt><dd class="inline">{{ [item.error_code, item.error_type, item.transient_error].filter(Boolean).join(' / ') }}</dd></div>
            <div v-if="item.upstream_server"><dt class="inline">Server: </dt><dd class="inline">{{ item.upstream_server }}</dd></div>
            <div v-if="item.retry_after_s != null"><dt class="inline">Retry-After: </dt><dd class="inline">{{ item.retry_after_s }} s</dd></div>
            <div v-if="item.wait_seconds != null"><dt class="inline">{{ t('usage.probeLogs.wait') }}: </dt><dd class="inline">{{ item.wait_seconds }} s</dd></div>
            <div v-if="item.cross_session_check"><dt class="inline">{{ t('usage.probeLogs.shared') }}: </dt><dd class="inline">{{ t(item.shared_state_published ? 'usage.probeLogs.published' : 'usage.probeLogs.notPublished') }}</dd></div>
          </dl>
        </article>
      </div>
      <div class="flex flex-wrap justify-between gap-3">
        <button type="button" data-testid="probe-latest" class="btn btn-secondary min-h-11" :disabled="loading || !cursor" @click="loadPage()">{{ t('usage.probeLogs.latest') }}</button>
        <button type="button" data-testid="probe-older" class="btn btn-secondary min-h-11" :disabled="loading || !page?.next_before" @click="loadPage(page?.next_before)">{{ t('usage.probeLogs.older') }}</button>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { listTurnStateProbeLogs, type TurnStateProbeEvent, type TurnStateProbeLogPage } from '@/api/admin/turnStateProbe'

const props = defineProps<{ show: boolean }>()
defineEmits<{ (event: 'close'): void }>()
const { t } = useI18n()
const account = ref<string | number>('')
const model = ref('')
const autoRefresh = ref(true)
const loading = ref(false)
const error = ref('')
const page = ref<TurnStateProbeLogPage | null>(null)
const cursor = ref<string | undefined>()
const snapshotTime = ref(Date.now())
const applied = ref({ account: '' as string | number, model: '' })
let controller: AbortController | undefined
let sequence = 0
let timer: ReturnType<typeof setInterval> | undefined
const workerOnline = computed(() => page.value?.status?.running &&
  snapshotTime.value - Date.parse(page.value.status.updated_at) < 45000)

function formatTime(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString()
}
function resultLabel(item: TurnStateProbeEvent) {
  if (item.event) return t(`usage.probeLogs.events.${item.event}`, item.event)
  return t(item.completed ? 'usage.probeLogs.completed' : 'usage.probeLogs.incomplete')
}
function stopRequests() {
  sequence++
  controller?.abort()
  clearInterval(timer)
  timer = undefined
  loading.value = false
  document.removeEventListener('keydown', trapFocus)
}
async function loadPage(before?: string) {
  if (!props.show) return
  const selected = before ? applied.value : { account: account.value, model: model.value }
  const accountID = selected.account === '' ? undefined : Number(selected.account)
  if (accountID != null && (!Number.isSafeInteger(accountID) || accountID <= 0)) {
    error.value = t('usage.probeLogs.invalidAccount')
    return
  }
  controller?.abort()
  const request = new AbortController()
  controller = request
  const requestID = ++sequence
  loading.value = true
  error.value = ''
  try {
    const result = await listTurnStateProbeLogs({ account_id: accountID, model: selected.model || undefined, before }, request.signal)
    if (requestID !== sequence || !props.show) return
    page.value = result
    cursor.value = before
    applied.value = selected
    snapshotTime.value = Date.now()
  } catch {
    if (requestID === sequence && !request.signal.aborted) error.value = t('usage.probeLogs.loadError')
  } finally {
    if (requestID === sequence) loading.value = false
  }
}
watch(() => props.show, (show) => {
  stopRequests()
  if (!show) return
  document.addEventListener('keydown', trapFocus)
  page.value = null
  cursor.value = undefined
  void loadPage()
  timer = setInterval(() => {
    snapshotTime.value = Date.now()
    if (autoRefresh.value && !cursor.value && !loading.value && account.value === applied.value.account && model.value === applied.value.model && document.visibilityState !== 'hidden') void loadPage()
  }, 10000)
}, { immediate: true })
// Keep keyboard traversal inside this modal, including its header close button.
function trapFocus(event: KeyboardEvent) {
  if (event.key !== 'Tab') return
  const dialog = (event.target as HTMLElement).closest('[role="dialog"]')
  const elements = Array.from(dialog?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), select:not(:disabled), [href], [tabindex="0"]') ?? [])
  const first = elements[0]
  const last = elements.at(-1)
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus() }
  if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
}
onUnmounted(stopRequests)
</script>
