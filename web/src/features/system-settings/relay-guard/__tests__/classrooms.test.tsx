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
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { type GuardConfig, newLimitRule } from '../api'
import type { Classroom, ClassroomBill } from '../classroom-api'
import { classroomBillCsv } from '../classroom-export'
import { ClassroomsSection } from '../classrooms'

let client: QueryClient
let config: GuardConfig
let classes: Classroom[]
let bill: ClassroomBill

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
      enabled: true,
      count_cached_tokens: false,
      default_output_tokens: 4096,
      rules: [{ ...newLimitRule('key_ip'), id: 'students', name: 'Students' }],
    },
  }
  classes = [
    {
      name: 'Class A',
      course: 'Computing',
      teacher: 'Teacher',
      note: '',
      key_id: 7,
      ips: ['192.0.2.0/25'],
      rule_ids: ['students'],
      id: 'a',
      started_at: 1793152800,
      ended_at: 0,
      concurrent: 0,
      reset_count: 0,
    },
    {
      name: 'Class B',
      course: '',
      teacher: '',
      note: '',
      key_id: 7,
      ips: ['192.0.2.128/25'],
      rule_ids: ['students'],
      id: 'b',
      started_at: 1793152800,
      ended_at: 0,
      concurrent: 1,
      reset_count: 0,
    },
    {
      name: 'Past class',
      course: '',
      teacher: '',
      note: '',
      key_id: 8,
      ips: ['*'],
      rule_ids: ['students'],
      id: 'past',
      started_at: 1793152800,
      ended_at: 1793156400,
      concurrent: 0,
      reset_count: 1,
    },
  ]
  bill = {
    classroom: classes[0],
    timezone: 'UTC+08:00',
    rows: [
      {
        classroom_id: 'a',
        date: '2026-10-28',
        ip: '192.0.2.1',
        model: 'model',
        group: 'default',
        source: 'UPSTREAM',
        billing_status: 'settled',
        quota_per_unit: 500000,
        prompt_tokens: 10,
        completion_tokens: 2,
        requests: 1,
        tokens: 12,
        quota: 50,
        first_at: 1793152800,
        last_at: 1793152800,
      },
    ],
    resets: [{ classroom_id: 'a', at: 1793156400, subjects: 2 }],
    summary: {
      requests: 1,
      tokens: 12,
      quota: 50,
      amount_usd: 0.0001,
      reused_requests: 0,
      unsettled_requests: 0,
      ips: 1,
    },
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (String(url).startsWith('/api/token/')) {
      return {
        data: {
          success: true,
          data: { items: [{ id: 7, name: 'Teaching key' }] },
        },
      }
    }
    let data: GuardConfig | ClassroomBill | Classroom[] = classes
    if (url === '/api/option/relay_guard') data = config
    else if (String(url).endsWith('/bill')) data = bill
    return { data: { success: true, data: structuredClone(data) } }
  })
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: classes[0] },
  })
})
afterEach(() => {
  client.clear()
  vi.restoreAllMocks()
})

function showClassrooms() {
  return render(
    <QueryClientProvider client={client}>
      <ClassroomsSection />
    </QueryClientProvider>
  )
}

describe('concurrent classroom management', () => {
  it('resets only the selected class after confirmation and blocks active requests', async () => {
    showClassrooms()
    const rowA = (await screen.findByText('Class A')).closest(
      'tr'
    ) as HTMLTableRowElement
    const rowB = screen
      .getByText('Class B')
      .closest('tr') as HTMLTableRowElement
    expect(
      within(rowB).getByRole('button', { name: 'Reset class limits' })
    ).toBeDisabled()
    await userEvent.click(
      within(rowA).getByRole('button', { name: 'Reset class limits' })
    )
    expect(api.post).not.toHaveBeenCalled()
    const confirmation = screen.getByRole('alertdialog')
    expect(confirmation).toHaveTextContent('Class A')
    await userEvent.click(
      within(confirmation).getByRole('button', { name: 'Continue' })
    )
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(
        '/api/option/relay_guard/classrooms/a/reset'
      )
    )
    expect(api.post).toHaveBeenCalledTimes(1)
  })

  it('keeps confirmation open on a failed reset and preserves other class rows', async () => {
    vi.mocked(api.post).mockRejectedValue(new Error('Storage unavailable'))
    showClassrooms()
    const row = (await screen.findByText('Class A')).closest(
      'tr'
    ) as HTMLTableRowElement
    await userEvent.click(
      within(row).getByRole('button', { name: 'Reset class limits' })
    )
    await userEvent.click(
      within(screen.getByRole('alertdialog')).getByRole('button', {
        name: 'Continue',
      })
    )
    await waitFor(() =>
      expect(
        within(screen.getByRole('alertdialog')).getByRole('button', {
          name: 'Continue',
        })
      ).toBeEnabled()
    )
    expect(screen.getByText('Class B')).toBeVisible()
  })

  it('opens archived bills without offering quota resets', async () => {
    showClassrooms()
    await userEvent.selectOptions(
      await screen.findByRole('combobox', { name: 'Classroom status' }),
      'archived'
    )
    expect(screen.queryByText('Class A')).not.toBeInTheDocument()
    expect(screen.getByText('Past class')).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Reset class limits' })
    ).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'View bill' })).toBeEnabled()
  })

  it('creates a second classroom using the same key and its own IP scope', async () => {
    showClassrooms()
    await userEvent.click(
      await screen.findByRole('button', { name: 'Start classroom' })
    )
    const dialog = screen.getByRole('dialog')
    await userEvent.type(
      within(dialog).getByLabelText('Classroom name'),
      'Class C'
    )
    await userEvent.click(
      within(dialog).getByRole('combobox', { name: 'API Key' })
    )
    await userEvent.click(
      await screen.findByRole('option', { name: '#7 Teaching key' })
    )
    expect(
      within(dialog).getByRole('spinbutton', { name: 'API key ID' })
    ).toHaveValue(7)
    await userEvent.type(
      within(dialog).getByLabelText('Classroom IP addresses'),
      '198.51.100.0/24,'
    )
    await userEvent.keyboard('{Escape}')
    await userEvent.click(
      within(dialog).getByRole('button', { name: 'Start classroom' })
    )
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(
        '/api/option/relay_guard/classrooms',
        expect.objectContaining({
          name: 'Class C',
          key_id: 7,
          ips: ['198.51.100.0/24'],
          rule_ids: ['students'],
        })
      )
    )
  })

  it('disables new classrooms when joint limits are off and shows an empty result after search', async () => {
    config.limits.enabled = false
    showClassrooms()
    expect(
      await screen.findByRole('button', { name: 'Start classroom' })
    ).toBeDisabled()
    await userEvent.type(
      screen.getByRole('textbox', { name: 'Search classrooms' }),
      'No match'
    )
    expect(screen.getByText('No classrooms found')).toBeVisible()
  })

  it('blocks exporting reversed dates or invalid exchange rates', async () => {
    showClassrooms()
    await userEvent.click(
      (await screen.findAllByRole('button', { name: 'View bill' }))[0]
    )
    const dialog = screen.getByRole('dialog')
    const exportButton = await within(dialog).findByRole('button', {
      name: 'Export CSV',
    })
    await waitFor(() => expect(exportButton).toBeEnabled())
    fireEvent.change(within(dialog).getByLabelText('Start date'), {
      target: { value: '2026-10-29' },
    })
    fireEvent.change(within(dialog).getByLabelText('End date'), {
      target: { value: '2026-10-28' },
    })
    expect(exportButton).toBeDisabled()
    expect(
      within(dialog).getByText('Invalid billing date range')
    ).toHaveAttribute('role', 'alert')
    fireEvent.change(within(dialog).getByLabelText('Start date'), {
      target: { value: '2026-10-28' },
    })
    fireEvent.change(within(dialog).getByLabelText('CNY per USD'), {
      target: { value: '-1' },
    })
    expect(exportButton).toBeDisabled()
  })

  it('exports metadata, zero-cost reuse, unconfirmed amounts and reset history without formula execution', () => {
    bill.classroom.name = '=SUM(1,2)'
    bill.classroom.teacher = ' \t=HYPERLINK("example")'
    bill.rows.push(
      { ...bill.rows[0], source: 'HIT', quota: 0 },
      { ...bill.rows[0], billing_status: 'failed', quota: 100 }
    )
    const csv = classroomBillCsv(
      bill,
      '2026-10-28',
      '2026-10-28',
      7,
      (key) => key
    ).join('')
    expect(csv).toContain("'=SUM(1,2)")
    expect(csv).toContain("' \t=HYPERLINK")
    expect(csv).toContain('"Gateway deductions (USD)","0.0001"')
    expect(csv).toContain('"HIT","settled","1","10","2","12","0","500000","0"')
    expect(csv).toContain('"failed","1","10","2","12","100","500000",""')
    expect(csv).toContain('"Reset history"')
    expect(csv).toContain('"CNY per USD","7"')
    expect(() =>
      classroomBillCsv(bill, '', '', Number.NaN, (key) => key)
    ).toThrow('Invalid exchange rate')
  })
})
