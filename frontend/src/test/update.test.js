import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, expect, it, vi } from 'vitest'
import UpdateCard from '@/components/UpdateCard.vue'

const base = () => ({ current_version: '1.0.0', phase: 'idle', service: true, can_install: true, preferences: { auto_check: true, auto_install: false, install_hour: 4 }, latest: { tag_name: 'v1.1.0', html_url: 'https://github.com/ssfun/surge-external-bridge/releases/tag/v1.1.0', body: '<script>danger()</script>' } })
const response = value => ({ ok: true, status: 200, json: async () => value })
afterEach(() => vi.useRealTimers())
it('does not report accepted update as successful and polls real completion', async () => {
 vi.useFakeTimers()
 const state = base()
 vi.stubGlobal('fetch', vi.fn(async (path, options) => response(options.method === 'POST' ? { status: 'accepted' } : state)))
 const wrapper = mount(UpdateCard)
 await flushPromises()
 expect(wrapper.text()).toContain('<script>danger()</script>')
 expect(wrapper.find('script').exists()).toBe(false)
 await wrapper.findAll('button').find(b => b.text() === '更新并重启').trigger('click')
 await flushPromises()
 expect(wrapper.text()).toContain('正在确认更新结果')
 expect(wrapper.text()).not.toContain('更新成功')
 state.target_version = 'v1.1.0'; state.phase = 'succeeded'; state.current_version = '1.1.0'
 await vi.advanceTimersByTimeAsync(10000)
 await flushPromises()
 expect(wrapper.text()).toContain('更新成功')
 expect(wrapper.text()).toContain('1.1.0')
 expect(fetch.mock.calls.filter(([path]) => path === '/api/update/install')).toHaveLength(1)
 wrapper.unmount()
})
it('shows manual-mode limitation and disables automatic installation', async () => {
 const state = { ...base(), service: false, can_install: false, install_reason: '停止程序后执行 SurgeEB update install' }
 vi.stubGlobal('fetch', vi.fn(async () => response(state)))
 const wrapper = mount(UpdateCard); await flushPromises()
 expect(wrapper.text()).toContain('SurgeEB update install')
 expect(wrapper.findAll('input[type="checkbox"]')[1].attributes('disabled')).toBeDefined()
 expect(wrapper.findAll('button').find(b => b.text() === '更新并重启').attributes('disabled')).toBeDefined()
 wrapper.unmount()
})
it('requires explicit confirmation before enabling unattended restarts', async () => {
 vi.stubGlobal('fetch', vi.fn(async () => response(base())))
 const wrapper = mount(UpdateCard); await flushPromises()
 await wrapper.findAll('input[type="checkbox"]')[1].setValue(true)
 vi.mocked(window.confirm).mockReturnValue(false)
 await wrapper.findAll('button').find(b => b.text() === '保存更新设置').trigger('click')
 expect(fetch.mock.calls.some(([path,options]) => options.method === 'PUT')).toBe(false)
 wrapper.unmount()
})
