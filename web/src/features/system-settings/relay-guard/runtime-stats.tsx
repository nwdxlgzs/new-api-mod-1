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
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber } from '@/lib/format'

import { SettingsCard } from '../components/settings-card'
import type { GuardConfig, GuardStats } from './api'

export function GuardRuntimeStats(props: {
  view: 'cache' | 'limits'
  data: GuardStats
  config: GuardConfig
  busy: boolean
  onClear: () => void
  onReset: (key: string) => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [confirm, setConfirm] = useState<string | null>(null)
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(0)
  const rows = useMemo(
    () =>
      props.data.usage.filter((row) =>
        `${row.subject} ${row.rule_id} ${props.data.classroom_names?.[row.classroom_id ?? ''] ?? ''}`
          .toLowerCase()
          .includes(search.toLowerCase())
      ),
    [props.data.usage, props.data.classroom_names, search]
  )
  const lastPage = Math.max(0, Math.ceil(rows.length / 20) - 1)
  const currentPage = Math.min(page, lastPage)
  if (props.view === 'cache') {
    const metrics = [
      { label: t('Cache hits'), value: props.data.cache.hits },
      { label: t('Shared requests'), value: props.data.cache.shared },
      { label: t('Active generations'), value: props.data.cache.active },
      { label: t('Cached responses'), value: props.data.cache.entries },
    ]
    return (
      <SettingsCard title={t('Reuse activity')} className='shadow-none'>
        <div className='grid grid-cols-2 gap-4 lg:grid-cols-4'>
          {metrics.map((metric) => (
            <div key={metric.label} className='min-w-0 rounded-lg border p-4'>
              <p className='text-muted-foreground text-xs'>{metric.label}</p>
              <p className='mt-2 text-2xl font-semibold tabular-nums'>
                {formatNumber(metric.value, locale)}
              </p>
            </div>
          ))}
        </div>
        <div className='mt-4 flex flex-wrap items-center justify-between gap-3'>
          <p className='text-muted-foreground text-sm'>
            {t('Cache memory (MiB)')}:{' '}
            {formatNumber(props.data.cache.bytes / 1048576, locale)}
          </p>
          <Button
            type='button'
            variant='outline'
            disabled={props.busy || props.data.cache.entries === 0}
            onClick={() => setConfirm('cache')}
          >
            {t('Clear response cache')}
          </Button>
        </div>
        <ConfirmDialog
          open={confirm === 'cache'}
          onOpenChange={(open) => {
            if (!open) setConfirm(null)
          }}
          title={t('Clear response cache')}
          desc={t(
            'Remove saved responses. Active generations continue, and existing usage limits stay unchanged.'
          )}
          isLoading={props.busy}
          handleConfirm={() => {
            props.onClear()
            setConfirm(null)
          }}
        />
      </SettingsCard>
    )
  }
  return (
    <SettingsCard
      title={t('Current IP and key usage')}
      description={t(
        'Showing up to 1,000 recently active subjects. Token reservations include in-flight requests.'
      )}
      className='shadow-none'
    >
      {props.data.storage_error && (
        <Alert variant='destructive' className='mb-4'>
          <AlertDescription>
            {t(
              'Usage persistence is unavailable. New limited requests are rejected until storage recovers.'
            )}
          </AlertDescription>
        </Alert>
      )}
      <Input
        aria-label={t('Filter usage by subject')}
        placeholder={t('Filter usage by subject')}
        value={search}
        className='mb-4 max-w-sm'
        onChange={(event) => {
          setSearch(event.target.value)
          setPage(0)
        }}
      />
      <StaticDataTable
        data={rows.slice(currentPage * 20, currentPage * 20 + 20)}
        getRowKey={(row) => row.key}
        emptyContent={t('No usage recorded')}
        columns={[
          {
            id: 'rule',
            header: t('Rule name'),
            cell: (row) =>
              props.config.limits.rules.find((rule) => rule.id === row.rule_id)
                ?.name || row.rule_id,
          },
          {
            id: 'subject',
            header: t('Limit subject'),
            cell: (row) => (
              <div>
                <span className='font-mono text-xs'>{row.subject}</span>
                {row.classroom_id && (
                  <p className='text-muted-foreground text-xs'>
                    {props.data.classroom_names?.[row.classroom_id] ??
                      row.classroom_id}
                  </p>
                )}
              </div>
            ),
          },
          {
            id: 'rpm',
            header: 'RPM',
            cell: (row) => formatNumber(row.requests, locale),
          },
          {
            id: 'tpm',
            header: 'TPM',
            cell: (row) => formatNumber(row.tokens, locale),
          },
          {
            id: 'concurrent',
            header: t('Concurrent requests'),
            cell: (row) => formatNumber(row.concurrent, locale),
          },
          {
            id: 'quota',
            header: t('Used quota'),
            cell: (row) => formatQuotaWithCurrency(row.quota),
          },
          {
            id: 'period',
            header: t('Quota period'),
            cell: (row) =>
              row.period === 'total' ? t('Lifetime') : row.period,
          },
          {
            id: 'reset',
            header: t('Actions'),
            cell: (row) => (
              <Button
                type='button'
                variant='ghost'
                size='sm'
                disabled={props.busy || row.concurrent > 0}
                onClick={() => setConfirm(row.key)}
              >
                {t('Reset usage')}
              </Button>
            ),
          },
        ]}
      />
      <div className='mt-4 flex items-center justify-end gap-2'>
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={currentPage === 0}
          onClick={() => setPage(currentPage - 1)}
        >
          {t('Previous')}
        </Button>
        <span className='text-muted-foreground text-xs'>
          {formatNumber(currentPage + 1, locale)} /{' '}
          {formatNumber(lastPage + 1, locale)}
        </span>
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={currentPage === lastPage}
          onClick={() => setPage(currentPage + 1)}
        >
          {t('Next')}
        </Button>
      </div>
      <ConfirmDialog
        open={confirm !== null}
        onOpenChange={(open) => {
          if (!open) setConfirm(null)
        }}
        title={t('Reset usage')}
        desc={t(
          'Reset this subject’s request, token and quota counters. Its API key balance is unchanged.'
        )}
        destructive
        isLoading={props.busy}
        handleConfirm={() => {
          if (confirm) props.onReset(confirm)
          setConfirm(null)
        }}
      />
    </SettingsCard>
  )
}
