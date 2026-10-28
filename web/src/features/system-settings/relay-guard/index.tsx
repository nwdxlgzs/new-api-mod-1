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
import { zodResolver } from '@hookform/resolvers/zod'
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseQueryResult,
} from '@tanstack/react-query'
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Form } from '@/components/ui/form'
import { handleServerError } from '@/lib/handle-server-error'

import { SettingsPageFormActions } from '../components/settings-page-context'
import {
  clearGuardCache,
  getGuardConfig,
  getGuardStats,
  guardSchema,
  resetGuardUsage,
  saveGuardConfig,
  type GuardConfig,
  type GuardStats,
} from './api'
import { CacheSettings } from './cache-settings'
import { LimitSettings } from './limit-settings'
import { GuardRuntimeStats } from './runtime-stats'

export function RelayGuardSection(props: { view: 'cache' | 'limits' }) {
  const { t } = useTranslation()
  const config = useQuery({
    queryKey: ['relay-guard-config'],
    queryFn: getGuardConfig,
  })
  const stats = useQuery({
    queryKey: ['relay-guard-stats'],
    queryFn: getGuardStats,
    refetchInterval: 15000,
  })
  if (config.isPending) return <LoadingState />
  if (config.isError) {
    return (
      <ErrorState
        title={t('Failed to load settings')}
        onRetry={() => void config.refetch()}
      />
    )
  }
  return (
    <GuardEditor
      key={props.view}
      view={props.view}
      config={config.data}
      stats={stats}
    />
  )
}

function GuardEditor(props: {
  view: 'cache' | 'limits'
  config: GuardConfig
  stats: UseQueryResult<GuardStats>
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const form = useForm<GuardConfig>({
    defaultValues: props.config,
    resolver: zodResolver(guardSchema, {
      error: () => t('Invalid request settings'),
    }),
  })
  // Keep unsaved edits when navigating within this page or refreshing settings.
  const { dirtyFields } = form.formState
  useEffect(() => {
    form.reset(props.config, { keepDirtyValues: true })
  }, [form, props.config])
  const save = useMutation({
    mutationFn: saveGuardConfig,
    onSuccess: (data) => {
      queryClient.setQueryData(['relay-guard-config'], data)
      form.reset(data)
      toast.success(t('Settings saved'))
      void queryClient.invalidateQueries({ queryKey: ['relay-guard-stats'] })
    },
    onError: (error) => handleServerError(error),
  })
  const action = useMutation({
    mutationFn: (key: string) =>
      key === 'cache' ? clearGuardCache() : resetGuardUsage(key),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['relay-guard-stats'] })
    },
    onError: (error) => handleServerError(error),
  })
  return (
    <Form {...form}>
      <div className='flex min-w-0 flex-col gap-4'>
        <SettingsPageFormActions
          onSave={form.handleSubmit(
            (values) =>
              save
                .mutateAsync(values)
                .then(() => undefined)
                .catch(() => undefined),
            () => toast.error(t('Invalid request settings'))
          )}
          isSaving={save.isPending}
          isSaveDisabled={Object.keys(dirtyFields).length === 0}
          onReset={() => form.reset(props.config)}
        />
        {props.view === 'cache' ? (
          <CacheSettings />
        ) : (
          <LimitSettings
            quotaPerUnit={props.stats.data?.quota_per_unit ?? 500000}
          />
        )}
        {props.stats.isError && (
          <ErrorState
            title={t('Failed to load usage')}
            onRetry={() => void props.stats.refetch()}
          />
        )}
        {props.stats.data && (
          <GuardRuntimeStats
            view={props.view}
            config={props.config}
            data={props.stats.data}
            busy={action.isPending}
            onClear={() => action.mutate('cache')}
            onReset={(key) => action.mutate(key)}
          />
        )}
      </div>
    </Form>
  )
}
