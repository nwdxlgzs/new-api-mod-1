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
import { Plus, Trash2 } from 'lucide-react'
import { useFieldArray, useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { Button } from '@/components/ui/button'

import { SettingsCard } from '../components/settings-card'
import { SettingsFormGrid } from '../components/settings-form-layout'
import { newLimitRule, type GuardConfig } from './api'
import {
  GuardNumberField,
  GuardSelectField,
  GuardSwitchField,
  GuardTextField,
  NativeSelectOption,
} from './fields'

export function LimitSettings(props: { quotaPerUnit: number }) {
  const { t } = useTranslation()
  const form = useFormContext<GuardConfig>()
  const rules = useFieldArray({
    control: form.control,
    name: 'limits.rules',
    keyName: 'fieldId',
  })
  const enabled = useWatch({ control: form.control, name: 'limits.enabled' })
  return (
    <div className='flex min-w-0 flex-col gap-4'>
      <SettingsCard
        title={t('IP and API key limits')}
        description={t(
          'Every matching rule must pass. An IP budget is shared across its keys; a key budget is shared across its IPs.'
        )}
        className='shadow-none'
      >
        <GuardSwitchField
          name='limits.enabled'
          label={t('Enable joint request limits')}
        />
        <GuardSwitchField
          name='limits.count_cached_tokens'
          label={t('Count replayed tokens toward TPM')}
          description={t(
            'RPM and concurrency always include cached requests. Quota follows the replay charge.'
          )}
          disabled={!enabled}
        />
        <div className='mt-4 max-w-sm'>
          <GuardNumberField
            name='limits.default_output_tokens'
            label={t('Output reservation when unspecified')}
            min={1}
            max={1000000}
            disabled={!enabled}
          />
        </div>
        <p className='text-muted-foreground mt-4 text-xs'>
          {t(
            'Token and quota limits reserve an estimate, then reconcile actual usage. A response can exceed its estimate; subsequent requests are blocked. Daily and monthly quotas reset in UTC. Quota state survives restarts; this local policy applies to one gateway process.'
          )}
        </p>
      </SettingsCard>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Use * for all subjects. Model patterns are comma-separated; zero disables that limit.'
          )}
        </p>
        <Button
          type='button'
          variant='outline'
          disabled={rules.fields.length >= 200}
          onClick={() => rules.append(newLimitRule('ip'))}
        >
          <Plus data-icon='inline-start' />
          {t('Add limit rule')}
        </Button>
      </div>
      {rules.fields.length === 0 && (
        <EmptyState
          title={t('No limit rules')}
          description={t(
            'Add an IP, API key or per-key IP rule to start controlling usage.'
          )}
        />
      )}
      {rules.fields.map((rule, index) => (
        <SettingsCard
          key={rule.fieldId}
          title={`${t('Limit rule')} ${index + 1}`}
          className='shadow-none'
        >
          <div className='flex items-center justify-between gap-4'>
            <GuardSwitchField
              name={`limits.rules.${index}.enabled`}
              label={t('Rule enabled')}
              disabled={!enabled}
            />
            <Button
              type='button'
              variant='ghost'
              size='icon'
              aria-label={t('Remove limit rule')}
              onClick={() => rules.remove(index)}
            >
              <Trash2 />
            </Button>
          </div>
          <SettingsFormGrid className='mt-4'>
            <GuardTextField
              name={`limits.rules.${index}.name`}
              label={t('Rule name')}
            />
            <GuardSelectField
              name={`limits.rules.${index}.subject`}
              label={t('Limit subject')}
            >
              <NativeSelectOption value='ip'>
                {t('IP address')}
              </NativeSelectOption>
              <NativeSelectOption value='key'>
                {t('API key')}
              </NativeSelectOption>
              <NativeSelectOption value='key_ip'>
                {t('Each IP within an API key')}
              </NativeSelectOption>
            </GuardSelectField>
            <GuardTextField
              name={`limits.rules.${index}.match`}
              label={t('IP, CIDR or API key ID')}
              placeholder='*'
            />
            <GuardTextField
              name={`limits.rules.${index}.models`}
              label={t('Model patterns')}
              placeholder='gpt-*, claude-*'
              models
            />
            <GuardNumberField
              name={`limits.rules.${index}.rpm`}
              label={t('Requests per minute (RPM)')}
              max={1000000}
            />
            <GuardNumberField
              name={`limits.rules.${index}.tpm`}
              label={t('Tokens per minute (TPM)')}
              max={1000000000}
            />
            <GuardNumberField
              name={`limits.rules.${index}.concurrent`}
              label={t('Concurrent requests')}
              max={10000}
            />
            <GuardNumberField
              name={`limits.rules.${index}.quota`}
              label={t('Quota limit (USD)')}
              divisor={props.quotaPerUnit}
            />
            <GuardSelectField
              name={`limits.rules.${index}.period`}
              label={t('Quota period')}
            >
              <NativeSelectOption value='day'>{t('Daily')}</NativeSelectOption>
              <NativeSelectOption value='month'>
                {t('Monthly')}
              </NativeSelectOption>
              <NativeSelectOption value='total'>
                {t('Lifetime')}
              </NativeSelectOption>
            </GuardSelectField>
          </SettingsFormGrid>
        </SettingsCard>
      ))}
    </div>
  )
}
