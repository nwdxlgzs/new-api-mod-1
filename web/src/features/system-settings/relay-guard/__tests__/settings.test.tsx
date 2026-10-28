/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { RelayGuardSection } from '..'
import { SettingsPageProvider } from '../../components/settings-page-context'
import { guardSchema, type GuardConfig, type GuardStats } from '../api'

let client: QueryClient
let config: GuardConfig
let stats: GuardStats

beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  config = {
    cache: {
      enabled: false,
      stream: true,
      coalesce: true,
      scope: 'key',
      match: 'exact',
      prefix_chars: 1024,
      ttl_seconds: 300,
      max_entries: 1000,
      max_memory_mb: 128,
      max_response_kb: 2048,
      max_subscribers: 32,
      timeout_seconds: 300,
      replay_delay_ms: 0,
      charge_percent: 0,
      models: [],
    },
    limits: {
      enabled: false,
      count_cached_tokens: true,
      default_output_tokens: 4096,
      rules: [],
    },
  }
  stats = {
    cache: {
      entries: 1,
      bytes: 1024,
      active: 0,
      hits: 2,
      misses: 1,
      shared: 1,
      bypassed: 0,
    },
    usage: [],
    storage_error: '',
    quota_per_unit: 500000,
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/option/relay_guard'
          ? structuredClone(config)
          : structuredClone(stats),
    },
  }))
  vi.spyOn(api, 'put').mockImplementation(async (_url, data) => ({
    data: { success: true, data },
  }))
  vi.spyOn(api, 'delete').mockResolvedValue({ data: { success: true } })
  vi.spyOn(api, 'post').mockResolvedValue({ data: { success: true } })
})
afterEach(async () => {
  client.clear()
  vi.restoreAllMocks()
  await i18next.changeLanguage('en')
})

function renderSettings(view: 'cache' | 'limits') {
  const actions = document.createElement('div')
  document.body.append(actions)
  const result = render(
    <QueryClientProvider client={client}>
      <SettingsPageProvider actionsContainer={actions}>
        <RelayGuardSection view={view} />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
  return { ...result, actions }
}

describe('response reuse settings', () => {
  it('allows clearing a numeric setting before entering its replacement', async () => {
    config.cache.enabled = true
    const { actions } = renderSettings('cache')
    const lifetime = await screen.findByRole('spinbutton', {
      name: 'Cache lifetime (seconds)',
    })
    await userEvent.clear(lifetime)
    await userEvent.type(lifetime, '600')
    expect(lifetime).toHaveValue(600)
    actions.remove()
  })

  it('accepts multiple model patterns typed with comma separators', async () => {
    config.cache.enabled = true
    const { actions } = renderSettings('cache')
    const models = await screen.findByLabelText('Model patterns')
    await userEvent.type(models, 'gpt-*,claude-*,')
    await userEvent.keyboard('{Escape}')
    await userEvent.click(
      within(actions).getByRole('button', { name: 'Save Changes' })
    )
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        '/api/option/relay_guard',
        expect.objectContaining({
          cache: expect.objectContaining({ models: ['gpt-*', 'claude-*'] }),
        })
      )
    )
    actions.remove()
  })

  it('keeps dependent controls disabled until reuse is enabled and saves actual edited settings', async () => {
    const { actions } = renderSettings('cache')
    const enabled = await screen.findByRole('switch', {
      name: 'Enable response reuse',
    })
    expect(
      screen.getByRole('switch', { name: 'Reuse SSE responses' })
    ).toHaveAttribute('aria-disabled', 'true')
    await userEvent.click(enabled)
    expect(
      screen.getByRole('switch', { name: 'Reuse SSE responses' })
    ).not.toHaveAttribute('aria-disabled', 'true')
    fireEvent.change(
      screen.getByRole('spinbutton', { name: 'Cache lifetime (seconds)' }),
      { target: { value: '600' } }
    )
    await userEvent.click(
      within(actions).getByRole('button', { name: 'Save Changes' })
    )
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        '/api/option/relay_guard',
        expect.objectContaining({
          cache: expect.objectContaining({ enabled: true, ttl_seconds: 600 }),
        })
      )
    )
    actions.remove()
  })

  it('requires explicit models for prefix reuse and blocks invalid capacity before saving', async () => {
    config.cache.enabled = true
    const { actions } = renderSettings('cache')
    await screen.findByRole('combobox', { name: 'Request matching' })
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: 'Request matching' }),
      'prefix'
    )
    expect(screen.getByRole('alert')).toHaveTextContent('Prefix reuse ignores')
    await userEvent.click(
      within(actions).getByRole('button', { name: 'Save Changes' })
    )
    expect(api.put).not.toHaveBeenCalled()
    await userEvent.type(screen.getByLabelText('Model patterns'), 'gpt-*,')
    await userEvent.keyboard('{Escape}')
    fireEvent.change(
      screen.getByRole('spinbutton', { name: 'Maximum response size (KiB)' }),
      { target: { value: '65536' } }
    )
    fireEvent.change(
      screen.getByRole('spinbutton', { name: 'Cache memory (MiB)' }),
      { target: { value: '1' } }
    )
    await userEvent.click(
      within(actions).getByRole('button', { name: 'Save Changes' })
    )
    expect(api.put).not.toHaveBeenCalled()
    actions.remove()
  })

  it('clears cached responses only after the shared confirmation dialog is accepted', async () => {
    const { actions } = renderSettings('cache')
    await userEvent.click(
      await screen.findByRole('button', { name: 'Clear response cache' })
    )
    expect(api.delete).not.toHaveBeenCalled()
    await userEvent.click(
      within(screen.getByRole('alertdialog')).getByRole('button', {
        name: 'Continue',
      })
    )
    await waitFor(() =>
      expect(api.delete).toHaveBeenCalledWith('/api/option/relay_guard/cache')
    )
    actions.remove()
  })
})

describe('joint IP and key settings', () => {
  it('creates an independent per-key IP rule with quota converted from dollars', async () => {
    const { actions } = renderSettings('limits')
    await userEvent.click(
      await screen.findByRole('switch', { name: 'Enable joint request limits' })
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Add limit rule' })
    )
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: 'Limit subject' }),
      'key_ip'
    )
    fireEvent.change(
      screen.getByRole('textbox', { name: 'IP, CIDR or API key ID' }),
      { target: { value: '42' } }
    )
    fireEvent.change(
      screen.getByRole('spinbutton', { name: 'Quota limit (USD)' }),
      { target: { value: '2.5' } }
    )
    await userEvent.click(
      within(actions).getByRole('button', { name: 'Save Changes' })
    )
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        '/api/option/relay_guard',
        expect.objectContaining({
          limits: expect.objectContaining({
            enabled: true,
            rules: [
              expect.objectContaining({
                subject: 'key_ip',
                match: '42',
                quota: 1250000,
              }),
            ],
          }),
        })
      )
    )
    actions.remove()
  })

  it('disables resetting active subjects and allows a confirmed reset for idle subjects', async () => {
    stats.usage = [
      {
        key: 'rule/active/total',
        rule_id: 'rule',
        subject: 'active',
        period: 'total',
        quota: 0,
        requests: 1,
        tokens: 100,
        concurrent: 1,
      },
      {
        key: 'rule/idle/total',
        rule_id: 'rule',
        classroom_id: 'class-a',
        subject: 'idle',
        period: 'total',
        quota: 10,
        requests: 2,
        tokens: 200,
        concurrent: 0,
      },
    ]
    stats.classroom_names = { 'class-a': 'Teaching A' }
    const { actions } = renderSettings('limits')
    await screen.findByText('active')
    expect(screen.getByText('Teaching A')).toBeVisible()
    const buttons = screen.getAllByRole('button', { name: 'Reset usage' })
    expect(buttons[0]).toBeDisabled()
    await userEvent.click(buttons[1])
    expect(api.post).not.toHaveBeenCalled()
    await userEvent.click(
      within(screen.getByRole('alertdialog')).getByRole('button', {
        name: 'Continue',
      })
    )
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/api/option/relay_guard/reset', {
        key: 'rule/idle/total',
      })
    )
    actions.remove()
  })

  it('renders loading failures with a retry action', async () => {
    vi.mocked(api.get).mockRejectedValue(new Error('network offline'))
    const { actions } = renderSettings('limits')
    expect(await screen.findByText('Failed to load settings')).toBeVisible()
    actions.remove()
  })

  it('updates translated controls when the language changes', async () => {
    i18next.addResourceBundle('zh', 'translation', {
      'Enable joint request limits': '启用联合请求限制',
    })
    const { actions } = renderSettings('limits')
    await screen.findByRole('switch', { name: 'Enable joint request limits' })
    await act(() => i18next.changeLanguage('zh'))
    expect(
      screen.getByRole('switch', { name: '启用联合请求限制' })
    ).toBeVisible()
    actions.remove()
  })
})

it('rejects an out-of-range quota or negative TPM before saving', () => {
  config.limits.rules = [
    {
      id: 'r',
      name: '',
      enabled: true,
      subject: 'ip',
      match: '*',
      models: [],
      rpm: 1,
      tpm: -1,
      quota: 0,
      period: 'day',
      concurrent: 0,
    },
  ]
  expect(guardSchema.safeParse(config).success).toBe(false)
})
