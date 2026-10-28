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
import { useFormContext, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'

import { SettingsCard } from '../components/settings-card'
import { SettingsFormGrid } from '../components/settings-form-layout'
import type { GuardConfig } from './api'
import {
  GuardNumberField,
  GuardSelectField,
  GuardSwitchField,
  GuardTextField,
  NativeSelectOption,
} from './fields'

export function CacheSettings() {
  const { t } = useTranslation()
  const form = useFormContext<GuardConfig>()
  const enabled = useWatch({ control: form.control, name: 'cache.enabled' })
  const match = useWatch({ control: form.control, name: 'cache.match' })
  return (
    <div className='flex min-w-0 flex-col gap-4'>
      <SettingsCard
        title={t('Response reuse')}
        description={t(
          'Reuse successful text responses and share identical requests while they are generating.'
        )}
        className='shadow-none'
      >
        <GuardSwitchField
          name='cache.enabled'
          label={t('Enable response reuse')}
        />
        <GuardSwitchField
          name='cache.stream'
          label={t('Reuse SSE responses')}
          description={t(
            'Replay complete event streams with their original event order.'
          )}
          disabled={!enabled}
        />
        <GuardSwitchField
          name='cache.coalesce'
          label={t('Share concurrent requests')}
          description={t(
            'One upstream generation serves matching clients; each client keeps its own limits.'
          )}
          disabled={!enabled}
        />
        <SettingsFormGrid className='mt-4'>
          <GuardSelectField
            name='cache.scope'
            label={t('Reuse scope')}
            disabled={!enabled}
          >
            <NativeSelectOption value='key'>
              {t('Same API key')}
            </NativeSelectOption>
            <NativeSelectOption value='user'>
              {t('Same user')}
            </NativeSelectOption>
          </GuardSelectField>
          <GuardSelectField
            name='cache.match'
            label={t('Request matching')}
            disabled={!enabled}
          >
            <NativeSelectOption value='exact'>
              {t('Full request match')}
            </NativeSelectOption>
            <NativeSelectOption value='prefix'>
              {t('Fixed-template prefix match')}
            </NativeSelectOption>
          </GuardSelectField>
          <GuardTextField
            name='cache.models'
            label={t('Model patterns')}
            placeholder='gpt-*, claude-*'
            models
            disabled={!enabled}
          />
          <GuardNumberField
            name='cache.charge_percent'
            label={t('Replay charge (%)')}
            min={0}
            max={100}
            disabled={!enabled}
          />
          {match === 'prefix' && (
            <GuardNumberField
              name='cache.prefix_chars'
              label={t('Prefix length (characters)')}
              min={128}
              max={100000}
              disabled={!enabled}
            />
          )}
        </SettingsFormGrid>
        {match === 'prefix' && (
          <Alert className='mt-4' variant='destructive'>
            <AlertDescription>
              {t(
                'Prefix reuse ignores text after the configured prefix. Enable only for fixed-answer templates and selected models; ordinary conversations should use full request matching.'
              )}
            </AlertDescription>
          </Alert>
        )}
        <p className='text-muted-foreground mt-4 text-xs'>
          {t(
            'Keys and users remain isolated. Tools, media, stateful conversations and failed responses bypass reuse. Clients can send Cache-Control: no-cache to request a new generation.'
          )}
        </p>
      </SettingsCard>
      <SettingsCard
        title={t('Cache capacity and playback')}
        className='shadow-none'
      >
        <SettingsFormGrid>
          <GuardNumberField
            name='cache.ttl_seconds'
            label={t('Cache lifetime (seconds)')}
            min={1}
            max={86400}
            disabled={!enabled}
          />
          <GuardNumberField
            name='cache.max_entries'
            label={t('Maximum cached responses')}
            min={1}
            max={100000}
            disabled={!enabled}
          />
          <GuardNumberField
            name='cache.max_memory_mb'
            label={t('Cache memory (MiB)')}
            min={1}
            max={2048}
            disabled={!enabled}
          />
          <GuardNumberField
            name='cache.max_response_kb'
            label={t('Maximum response size (KiB)')}
            min={1}
            max={65536}
            disabled={!enabled}
          />
          <GuardNumberField
            name='cache.max_subscribers'
            label={t('Clients per shared generation')}
            min={1}
            max={128}
            disabled={!enabled}
          />
          <GuardNumberField
            name='cache.timeout_seconds'
            label={t('Generation timeout (seconds)')}
            min={10}
            max={3600}
            disabled={!enabled}
          />
          <GuardNumberField
            name='cache.replay_delay_ms'
            label={t('SSE replay delay (milliseconds)')}
            min={0}
            max={1000}
            disabled={!enabled}
          />
        </SettingsFormGrid>
      </SettingsCard>
    </div>
  )
}
