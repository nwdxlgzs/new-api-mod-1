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
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { downloadCsv } from '@/features/usage-logs/lib/export'
import { toIntlLocale } from '@/i18n/languages'
import { formatLocalCurrencyAmount } from '@/lib/currency'
import { formatNumber } from '@/lib/format'

import { getClassroomBill, type Classroom } from './classroom-api'
import { classroomBillCsv } from './classroom-export'

export function ClassroomBillDialog(props: {
  classroom: Classroom
  onClose: () => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [rate, setRate] = useState('')
  const dateValid = !from || !to || from <= to
  const cnyRate = rate === '' ? undefined : Number(rate)
  const rateValid =
    cnyRate === undefined ||
    (Number.isFinite(cnyRate) && cnyRate > 0 && cnyRate <= 10000)
  const bill = useQuery({
    queryKey: ['relay-classroom-bill', props.classroom.id, from, to],
    queryFn: () => getClassroomBill(props.classroom.id, from, to),
    enabled: dateValid,
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={`${t('Classroom billing')} · ${props.classroom.name}`}
      description={t(
        'Dates include the whole day in UTC+08:00. Resets keep billing history.'
      )}
      contentClassName='sm:max-w-5xl'
      footer={
        <Button
          disabled={
            !dateValid ||
            !rateValid ||
            !bill.data ||
            bill.isFetching ||
            bill.isError
          }
          onClick={() => {
            if (!bill.data) return
            const filename = `classroom-${props.classroom.id}-${from || 'start'}-${to || 'latest'}.csv`
            downloadCsv(
              filename,
              classroomBillCsv(bill.data, from, to, cnyRate, t)
            )
          }}
        >
          {t('Export CSV')}
        </Button>
      }
    >
      <div className='flex min-w-0 flex-col gap-4'>
        <Alert>
          <AlertDescription>
            {t(
              'Gateway deductions use configured prices. Reconcile them with upstream payment receipts before reimbursement.'
            )}
          </AlertDescription>
        </Alert>
        <div className='grid gap-4 sm:grid-cols-3'>
          <div className='space-y-2'>
            <Label htmlFor='class-bill-from'>{t('Start date')}</Label>
            <Input
              id='class-bill-from'
              type='date'
              value={from}
              onChange={(event) => setFrom(event.target.value)}
            />
          </div>
          <div className='space-y-2'>
            <Label htmlFor='class-bill-to'>{t('End date')}</Label>
            <Input
              id='class-bill-to'
              type='date'
              value={to}
              onChange={(event) => setTo(event.target.value)}
            />
          </div>
          <div className='space-y-2'>
            <Label htmlFor='class-bill-rate'>{t('CNY per USD')}</Label>
            <Input
              id='class-bill-rate'
              type='number'
              min={0.000001}
              max={10000}
              step='any'
              value={rate}
              placeholder={t('Optional')}
              onChange={(event) => setRate(event.target.value)}
              aria-invalid={!rateValid}
            />
          </div>
        </div>
        {!dateValid && (
          <p role='alert' className='text-destructive text-sm'>
            {t('Invalid billing date range')}
          </p>
        )}
        {!rateValid && (
          <p role='alert' className='text-destructive text-sm'>
            {t('Invalid exchange rate')}
          </p>
        )}
        {bill.isPending && dateValid && <LoadingState />}
        {bill.isError && (
          <ErrorState
            title={t('Failed to load usage')}
            onRetry={() => void bill.refetch()}
          />
        )}
        {bill.data && dateValid && (
          <>
            <dl className='grid grid-cols-2 gap-4 rounded-lg border p-4 sm:grid-cols-4'>
              <div>
                <dt className='text-muted-foreground text-xs'>
                  {t('Gateway deductions (USD)')}
                </dt>
                <dd className='mt-1 font-semibold tabular-nums'>
                  {formatLocalCurrencyAmount(bill.data.summary.amount_usd, {
                    showSymbol: false,
                    digitsLarge: 6,
                    digitsSmall: 6,
                    abbreviate: false,
                    locale,
                  })}
                </dd>
              </div>
              <div>
                <dt className='text-muted-foreground text-xs'>
                  {t('Requests')}
                </dt>
                <dd className='mt-1 font-semibold tabular-nums'>
                  {formatNumber(bill.data.summary.requests, locale)}
                </dd>
              </div>
              <div>
                <dt className='text-muted-foreground text-xs'>
                  {t('Total Tokens')}
                </dt>
                <dd className='mt-1 font-semibold tabular-nums'>
                  {formatNumber(bill.data.summary.tokens, locale)}
                </dd>
              </div>
              <div>
                <dt className='text-muted-foreground text-xs'>
                  {t('Reused requests')}
                </dt>
                <dd className='mt-1 font-semibold tabular-nums'>
                  {formatNumber(bill.data.summary.reused_requests, locale)}
                </dd>
              </div>
            </dl>
            <p className='text-muted-foreground text-sm'>
              {t('Student IPs')}: {formatNumber(bill.data.summary.ips, locale)}{' '}
              · {t('Reset history')}:{' '}
              {formatNumber(bill.data.resets.length, locale)} ·{' '}
              {t('Unconfirmed settlements')}:{' '}
              {formatNumber(bill.data.summary.unsettled_requests, locale)}
            </p>
            {bill.data.summary.unsettled_requests > 0 && (
              <Alert variant='destructive'>
                <AlertDescription>
                  {t(
                    'Unconfirmed settlements are excluded from deduction totals. Check usage logs before reimbursement.'
                  )}
                </AlertDescription>
              </Alert>
            )}
            <p className='text-muted-foreground text-xs'>
              {t(
                'Preview shows the first 100 summary rows. CSV includes every row in the selected date range.'
              )}
            </p>
            <StaticDataTable
              data={bill.data.rows.slice(0, 100)}
              emptyContent={t('No usage recorded')}
              columns={[
                { id: 'date', header: t('Date'), cell: (row) => row.date },
                { id: 'ip', header: t('IP'), cell: (row) => row.ip },
                { id: 'model', header: t('Model'), cell: (row) => row.model },
                {
                  id: 'source',
                  header: t('Response source'),
                  cell: (row) => row.source,
                },
                {
                  id: 'status',
                  header: t('Settlement status'),
                  cell: (row) =>
                    t(
                      row.billing_status === 'settled'
                        ? 'Settled'
                        : 'Unconfirmed'
                    ),
                },
                {
                  id: 'requests',
                  header: t('Requests'),
                  cell: (row) => formatNumber(row.requests, locale),
                },
                {
                  id: 'tokens',
                  header: t('Total Tokens'),
                  cell: (row) => formatNumber(row.tokens, locale),
                },
                {
                  id: 'amount',
                  header: t('Gateway deductions (USD)'),
                  cell: (row) =>
                    row.billing_status === 'settled'
                      ? formatLocalCurrencyAmount(
                          row.quota / row.quota_per_unit,
                          {
                            showSymbol: false,
                            digitsLarge: 6,
                            digitsSmall: 6,
                            abbreviate: false,
                            locale,
                          }
                        )
                      : '—',
                },
              ]}
            />
          </>
        )}
      </div>
    </Dialog>
  )
}
