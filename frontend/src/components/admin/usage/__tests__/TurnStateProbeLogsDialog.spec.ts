import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import TurnStateProbeLogsDialog from '../TurnStateProbeLogsDialog.vue'
import type { TurnStateProbeLogPage } from '@/api/admin/turnStateProbe'

const { list } = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('@/api/admin/turnStateProbe', () => ({ listTurnStateProbeLogs: list }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const example = (): TurnStateProbeLogPage => ({
  status: { running: true, updated_at: new Date().toISOString(), account_ids: [1010], models: ['gpt-6-astra'], concurrency: 4, interval_seconds: 60 },
  retention_limit: 2000,
  items: [{ id: 'event-1', time: new Date().toISOString(), account_id: 1010, model: 'gpt-6-astra', route: 'proxy:28', completed: false,
    sent_state_length: 292, received_state_length: 312, http_status: 429, error_code: 'rate_limit_exceeded', retry_after_s: 120 }],
  next_before: 'cursor-1'
})
function create(show = true) {
  return mount(TurnStateProbeLogsDialog, { props: { show }, global: { stubs: {
    BaseDialog: { props: ['show'], template: '<div v-if="show" role="dialog"><slot /></div>' }
  } } })
}
beforeEach(() => { vi.useFakeTimers(); list.mockReset(); list.mockResolvedValue(example()) })
afterEach(() => { vi.useRealTimers() })

describe('TurnStateProbeLogsDialog', () => {
  it('is lazy, shows diagnostic fields and colors only received 312 red', async () => {
    const wrapper = create(false)
    expect(list).not.toHaveBeenCalled()
    await wrapper.setProps({ show: true }); await flushPromises()
    expect(wrapper.get('[data-testid="probe-returned"]').classes()).toContain('text-red-600')
    expect(wrapper.text()).toContain('proxy:28')
    expect(wrapper.text()).toContain('rate_limit_exceeded')
    expect(wrapper.text()).toContain('Retry-After: 120 s')
    expect(wrapper.text()).toContain('usage.probeLogs.online')
    const next = example(); next.items[0].received_state_length = 292
    list.mockResolvedValue(next)
    await wrapper.get('[data-testid="probe-refresh"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="probe-returned"]').classes()).not.toContain('text-red-600')
    wrapper.unmount()
  })

  it('filters, paginates without changing filter identity, and pauses history polling', async () => {
    const wrapper = create(); await flushPromises()
    await wrapper.get('[data-testid="probe-account"]').setValue('1010')
    await wrapper.get('[data-testid="probe-model"]').setValue('gpt-5.6-sol')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(list.mock.lastCall?.[0]).toEqual({ account_id: 1010, model: 'gpt-5.6-sol', before: undefined })
    // Changing a draft must not apply a new filter to an existing cursor.
    await wrapper.get('[data-testid="probe-account"]').setValue('537')
    await wrapper.get('[data-testid="probe-older"]').trigger('click'); await flushPromises()
    expect(list.mock.lastCall?.[0]).toEqual({ account_id: 1010, model: 'gpt-5.6-sol', before: 'cursor-1' })
    const count = list.mock.calls.length
    await vi.advanceTimersByTimeAsync(30000)
    expect(list).toHaveBeenCalledTimes(count)
    await wrapper.get('[data-testid="probe-latest"]').trigger('click'); await flushPromises()
    expect(list.mock.lastCall?.[0].account_id).toBe(537)
    await vi.advanceTimersByTimeAsync(10000)
    expect(list).toHaveBeenCalledTimes(count + 2)
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(30000)
    expect(list).toHaveBeenCalledTimes(count + 2)
  })

  it('aborts on close and ignores late results after reopening', async () => {
    let resolveOld!: (page: TurnStateProbeLogPage) => void
    list.mockReturnValueOnce(new Promise<TurnStateProbeLogPage>((resolve) => { resolveOld = resolve }))
    const wrapper = create(); await flushPromises()
    const signal = list.mock.calls[0][1] as AbortSignal
    await wrapper.setProps({ show: false })
    expect(signal.aborted).toBe(true)
    await wrapper.setProps({ show: true }); await flushPromises()
    const old = example(); old.items[0].account_id = 999
    resolveOld(old); await flushPromises()
    expect(wrapper.text()).toContain('#1010')
    expect(wrapper.text()).not.toContain('#999')
    wrapper.unmount()
  })

  it('shows failures without raw error text and rejects invalid filters', async () => {
    list.mockRejectedValueOnce(new Error('SECRET_RESPONSE_BODY'))
    const wrapper = create(); await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('usage.probeLogs.loadError')
    expect(wrapper.text()).not.toContain('SECRET')
    await wrapper.get('[data-testid="probe-account"]').setValue('-1')
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(list).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[role="alert"]').text()).toBe('usage.probeLogs.invalidAccount')
    wrapper.unmount()
  })

  it('never renders unknown/raw fields and reports stale worker status', async () => {
    const result = example()
    result.status!.updated_at = new Date(Date.now() - 60000).toISOString()
    Object.assign(result.items[0], { state: 'SECRET_STATE', prompt: 'SECRET_PROMPT', credentials: 'SECRET_CREDENTIAL' })
    list.mockResolvedValue(result)
    const wrapper = create(); await flushPromises()
    expect(wrapper.text()).not.toContain('SECRET')
    expect(wrapper.text()).toContain('usage.probeLogs.offline')
    wrapper.unmount()
  })
})
