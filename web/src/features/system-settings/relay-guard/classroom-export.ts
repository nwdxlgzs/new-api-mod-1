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
import { toCsvLine } from '@/features/usage-logs/lib/export'

import type { ClassroomBill } from './classroom-api'

type Translate = (key: string) => string

export function classroomBillCsv(
  bill: ClassroomBill,
  from: string,
  to: string,
  cnyRate: number | undefined,
  t: Translate
): string[] {
  if (
    cnyRate !== undefined &&
    (!Number.isFinite(cnyRate) || cnyRate <= 0 || cnyRate > 10000)
  ) {
    throw new Error('Invalid exchange rate')
  }
  const classroom = bill.classroom
  // Keep raw quota and its conversion rate alongside USD for reconciliation.
  const lines: string[][] = [
    [t('Classroom billing'), classroom.name],
    [t('Classroom ID'), classroom.id],
    [t('Course'), classroom.course],
    [t('Teacher'), classroom.teacher],
    [t('Notes'), classroom.note],
    [t('API key ID'), String(classroom.key_id)],
    [t('Classroom IP addresses'), classroom.ips.join(', ')],
    [t('Independent limit rules'), classroom.rule_ids.join(', ')],
    [t('Start date'), from],
    [t('End date'), to],
    [t('Time zone'), bill.timezone],
    [t('Exported at'), new Date().toISOString()],
    [
      t(
        'Gateway deductions use configured prices. Reconcile them with upstream payment receipts before reimbursement.'
      ),
    ],
    [t('Requests'), String(bill.summary.requests)],
    [t('Total Tokens'), String(bill.summary.tokens)],
    [t('Student IPs'), String(bill.summary.ips)],
    [t('Reused requests'), String(bill.summary.reused_requests)],
    [t('Unconfirmed settlements'), String(bill.summary.unsettled_requests)],
    [t('Gateway deductions (USD)'), String(bill.summary.amount_usd)],
  ]
  if (cnyRate !== undefined) {
    lines.push(
      [t('CNY per USD'), String(cnyRate)],
      [t('Converted amount (CNY)'), String(bill.summary.amount_usd * cnyRate)]
    )
  }
  lines.push(
    [],
    [
      t('Date'),
      t('IP'),
      t('Model'),
      t('Group'),
      t('Response source'),
      t('Settlement status'),
      t('Requests'),
      t('Input Tokens'),
      t('Output Tokens'),
      t('Total Tokens'),
      t('Raw quota'),
      t('Quota units per USD'),
      t('Gateway deductions (USD)'),
    ]
  )
  for (const row of bill.rows) {
    // Unconfirmed charges are visible, but excluded from reimbursable totals.
    const amount =
      row.billing_status === 'settled'
        ? String(row.quota / row.quota_per_unit)
        : ''
    lines.push([
      row.date,
      row.ip,
      row.model,
      row.group,
      row.source,
      row.billing_status,
      String(row.requests),
      String(row.prompt_tokens),
      String(row.completion_tokens),
      String(row.tokens),
      String(row.quota),
      String(row.quota_per_unit),
      amount,
    ])
  }
  lines.push([], [t('Reset history')], [t('Time'), t('Reset subjects')])
  for (const reset of bill.resets) {
    lines.push([
      new Date(reset.at * 1000).toISOString(),
      String(reset.subjects),
    ])
  }
  return lines.map(toCsvLine)
}
