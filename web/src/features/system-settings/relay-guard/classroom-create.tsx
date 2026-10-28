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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { MultiSelect } from '@/components/multi-select'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { getApiKeys } from '@/features/keys/api'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { SettingsFormGrid } from '../components/settings-form-layout'
import type { LimitRule } from './api'
import {
  classroomSchema,
  startClassroom,
  type ClassroomInput,
} from './classroom-api'

export function CreateClassroomDialog(props: {
  rules: LimitRule[]
  onClose: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const keys = useQuery({
    queryKey: ['classroom-api-keys'],
    queryFn: async () =>
      requireServerSuccess(await getApiKeys({ p: 1, size: 100 })).data?.items ??
      [],
  })
  const form = useForm<ClassroomInput>({
    resolver: zodResolver(classroomSchema, {
      error: () => t('Invalid classroom settings'),
    }),
    defaultValues: {
      name: '',
      course: '',
      teacher: '',
      note: '',
      key_id: Number.NaN,
      ips: [],
      rule_ids: props.rules
        .filter((r) => r.enabled && r.subject !== 'key' && r.match === '*')
        .map((r) => r.id),
    },
  })
  const create = useMutation({
    mutationFn: startClassroom,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['relay-classrooms'] })
      props.onClose()
    },
    onError: (error) => handleServerError(error),
  })
  const keyID = useWatch({ control: form.control, name: 'key_id' })
  const textFields = [
    { name: 'name', label: t('Classroom name'), max: 200 },
    { name: 'course', label: t('Course'), max: 200 },
    { name: 'teacher', label: t('Teacher'), max: 100 },
    { name: 'note', label: t('Notes'), max: 2000 },
  ] as const
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !create.isPending) props.onClose()
      }}
      title={t('Start classroom')}
      description={t(
        'Run multiple classrooms with separate IP ranges on one key, or use separate keys when IP ranges overlap.'
      )}
      footer={
        <Button
          type='submit'
          form='create-classroom'
          disabled={create.isPending}
        >
          {t('Start classroom')}
        </Button>
      }
    >
      <Form {...form}>
        <form
          id='create-classroom'
          onSubmit={form.handleSubmit((input) => create.mutate(input))}
        >
          <SettingsFormGrid>
            <div className='min-w-0 space-y-2'>
              <Label htmlFor='classroom-key-picker'>{t('API Key')}</Label>
              <Combobox
                id='classroom-key-picker'
                options={(keys.data ?? []).map((key) => ({
                  value: String(key.id),
                  label: `#${key.id} ${key.name}`,
                }))}
                value={Number.isFinite(keyID) ? String(keyID) : ''}
                onValueChange={(value) =>
                  form.setValue('key_id', Number(value), {
                    shouldValidate: true,
                  })
                }
                disabled={keys.isPending || create.isPending}
              />
            </div>
            {textFields.map((item) => (
              <FormField
                key={item.name}
                control={form.control}
                name={item.name}
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{item.label}</FormLabel>
                    <FormControl>
                      <Input
                        {...field}
                        maxLength={item.max}
                        disabled={create.isPending}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            ))}
            <FormField
              control={form.control}
              name='key_id'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('API key ID')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={1}
                      step={1}
                      name={field.name}
                      ref={field.ref}
                      onBlur={field.onBlur}
                      value={Number.isFinite(field.value) ? field.value : ''}
                      onChange={(event) =>
                        field.onChange(event.target.valueAsNumber)
                      }
                      disabled={create.isPending}
                    />
                  </FormControl>
                  <FormDescription>
                    {t('Enter the numeric key ID from API key management.')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='ips'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Classroom IP addresses')}</FormLabel>
                  <FormControl>
                    <MultiSelect
                      options={[]}
                      selected={field.value}
                      onChange={field.onChange}
                      allowCreate
                      maxVisibleChips={4}
                      placeholder='192.168.1.0/24'
                      disabled={create.isPending}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Use gateway-visible IPs or CIDRs, separated by commas. Use * alone for a dedicated classroom key.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='rule_ids'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Independent limit rules')}</FormLabel>
                  <FormControl>
                    <MultiSelect
                      options={props.rules
                        .filter((r) => r.enabled)
                        .map((r) => ({ value: r.id, label: r.name || r.id }))}
                      selected={field.value}
                      onChange={field.onChange}
                      disabled={create.isPending}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Selected rules have separate counters for this classroom. Other rules still enforce shared key and IP budgets.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </SettingsFormGrid>
        </form>
      </Form>
    </Dialog>
  )
}
