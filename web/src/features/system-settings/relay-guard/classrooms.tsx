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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  getCoreRowModel,
  getPaginationRowModel,
  useReactTable,
} from '@tanstack/react-table'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { DataTablePagination, StaticDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'

import { SettingsCard } from '../components/settings-card'
import { getGuardConfig } from './api'
import { changeClassroom, getClassrooms, type Classroom } from './classroom-api'
import { ClassroomBillDialog } from './classroom-bill'
import { CreateClassroomDialog } from './classroom-create'

export function ClassroomsSection() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const queryClient = useQueryClient()
  const [create, setCreate] = useState(false)
  const [report, setReport] = useState<Classroom | null>(null)
  const [confirm, setConfirm] = useState<{
    classroom: Classroom
    action: 'reset' | 'end'
  } | null>(null)
  const [search, setSearch] = useState('')
  const [status, setStatus] = useState('active')
  const config = useQuery({
    queryKey: ['relay-guard-config'],
    queryFn: getGuardConfig,
  })
  const classrooms = useQuery({
    queryKey: ['relay-classrooms'],
    queryFn: getClassrooms,
    refetchInterval: 15000,
  })
  const rows = useMemo(
    () =>
      (classrooms.data ?? []).filter(
        (row) =>
          (status === 'all' ||
            (row.ended_at === 0) === (status === 'active')) &&
          `${row.name} ${row.course} ${row.teacher} ${row.key_id} ${row.ips.join(' ')}`
            .toLowerCase()
            .includes(search.toLowerCase())
      ),
    [classrooms.data, search, status]
  )
  const table = useReactTable({
    data: rows,
    columns: [],
    getCoreRowModel: getCoreRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
    initialState: { pagination: { pageSize: 20, pageIndex: 0 } },
  })
  const change = useMutation({
    mutationFn: (value: { classroom: Classroom; action: 'reset' | 'end' }) =>
      changeClassroom(value.classroom.id, value.action),
    onSuccess: () => {
      setConfirm(null)
      toast.success(t('Classroom updated'))
      void queryClient.invalidateQueries({ queryKey: ['relay-classrooms'] })
      void queryClient.invalidateQueries({ queryKey: ['relay-classroom-bill'] })
      void queryClient.invalidateQueries({ queryKey: ['relay-guard-stats'] })
    },
    onError: (error) => handleServerError(error),
  })
  if (config.isPending || classrooms.isPending) return <LoadingState />
  if (config.isError || classrooms.isError) {
    return (
      <ErrorState
        title={t('Failed to load settings')}
        onRetry={() => {
          void config.refetch()
          void classrooms.refetch()
        }}
      />
    )
  }
  const enabled =
    config.data.limits.enabled &&
    config.data.limits.rules.some((r) => r.enabled)
  return (
    <SettingsCard
      title={t('Classroom billing')}
      description={t(
        'Run multiple classrooms with separate IP ranges on one key, or use separate keys when IP ranges overlap.'
      )}
      className='shadow-none'
    >
      <div className='flex min-w-0 flex-col gap-4'>
        {!enabled && (
          <Alert>
            <AlertDescription>
              {t('Enable IP and API key limits before starting a classroom')}
            </AlertDescription>
          </Alert>
        )}
        <p className='text-muted-foreground text-sm'>
          {t(
            'Classroom resets restore selected counters for every student. Bills, other classrooms, shared budgets and key balances are preserved.'
          )}
        </p>
        <div className='flex flex-wrap items-center gap-3'>
          <Input
            className='min-w-40 flex-1'
            aria-label={t('Search classrooms')}
            placeholder={t('Search classrooms')}
            value={search}
            onChange={(event) => {
              setSearch(event.target.value)
              table.resetPageIndex()
            }}
          />
          <NativeSelect
            aria-label={t('Classroom status')}
            value={status}
            onChange={(event) => {
              setStatus(event.target.value)
              table.resetPageIndex()
            }}
          >
            <NativeSelectOption value='active'>
              {t('Active')}
            </NativeSelectOption>
            <NativeSelectOption value='archived'>
              {t('Archived')}
            </NativeSelectOption>
            <NativeSelectOption value='all'>{t('All')}</NativeSelectOption>
          </NativeSelect>
          <Button disabled={!enabled} onClick={() => setCreate(true)}>
            {t('Start classroom')}
          </Button>
        </div>
        <StaticDataTable
          data={table.getRowModel().rows.map((row) => row.original)}
          getRowKey={(row) => row.id}
          emptyContent={t('No classrooms found')}
          columns={[
            {
              id: 'classroom',
              header: t('Classroom name'),
              cell: (row) => (
                <div className='max-w-56 break-words'>
                  <p className='font-medium'>{row.name}</p>
                  <p className='text-muted-foreground text-xs'>
                    {row.course} {row.teacher}
                  </p>
                </div>
              ),
            },
            {
              id: 'key',
              header: t('API key ID'),
              cell: (row) => `#${row.key_id}`,
            },
            {
              id: 'ips',
              header: t('Classroom IP addresses'),
              cell: (row) => (
                <span className='block max-w-56 font-mono text-xs break-words'>
                  {row.ips.join(', ')}
                </span>
              ),
            },
            {
              id: 'status',
              header: t('Status'),
              cell: (row) => t(row.ended_at ? 'Archived' : 'Active'),
            },
            {
              id: 'concurrent',
              header: t('Concurrent requests'),
              cell: (row) => formatNumber(row.concurrent, locale),
            },
            {
              id: 'resets',
              header: t('Reset count'),
              cell: (row) => formatNumber(row.reset_count, locale),
            },
            {
              id: 'actions',
              header: t('Actions'),
              cell: (row) => (
                <div className='flex flex-wrap gap-1'>
                  <Button
                    size='sm'
                    variant='outline'
                    onClick={() => setReport(row)}
                  >
                    {t('View bill')}
                  </Button>
                  {!row.ended_at && (
                    <>
                      <Button
                        size='sm'
                        variant='ghost'
                        disabled={change.isPending || row.concurrent > 0}
                        onClick={() =>
                          setConfirm({ classroom: row, action: 'reset' })
                        }
                      >
                        {t('Reset class limits')}
                      </Button>
                      <Button
                        size='sm'
                        variant='ghost'
                        disabled={change.isPending || row.concurrent > 0}
                        onClick={() =>
                          setConfirm({ classroom: row, action: 'end' })
                        }
                      >
                        {t('End classroom')}
                      </Button>
                    </>
                  )}
                </div>
              ),
            },
          ]}
        />
        <DataTablePagination table={table} compact />
        {create && (
          <CreateClassroomDialog
            rules={config.data.limits.rules}
            onClose={() => setCreate(false)}
          />
        )}
        {report && (
          <ClassroomBillDialog
            key={report.id}
            classroom={report}
            onClose={() => setReport(null)}
          />
        )}
        <ConfirmDialog
          open={confirm !== null}
          onOpenChange={(open) => {
            if (!open && !change.isPending) setConfirm(null)
          }}
          title={`${t(confirm?.action === 'end' ? 'End classroom' : 'Reset class limits')} · ${confirm?.classroom.name ?? ''}`}
          desc={t(
            confirm?.action === 'end'
              ? 'Archive this classroom and keep its bills. The API key stays enabled and subsequent requests follow the remaining rules.'
              : 'Reset all selected classroom counters, including RPM, TPM and quota. Historical bills and other classrooms are preserved.'
          )}
          destructive
          isLoading={change.isPending}
          handleConfirm={() => {
            if (confirm) change.mutate(confirm)
          }}
        />
      </div>
    </SettingsCard>
  )
}
