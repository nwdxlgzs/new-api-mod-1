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
import { z } from 'zod'

import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

const modelPatterns = z.array(z.string().trim().min(1).max(200)).max(100)
export const limitRuleSchema = z.object({
  id: z.string().min(1).max(64),
  name: z.string().max(100),
  enabled: z.boolean(),
  subject: z.enum(['ip', 'key', 'key_ip']),
  match: z.string().min(1),
  models: modelPatterns,
  rpm: z.number().int().min(0).max(1_000_000),
  tpm: z.number().int().min(0).max(1_000_000_000),
  quota: z.number().int().min(0).max(Number.MAX_SAFE_INTEGER),
  period: z.enum(['day', 'month', 'total']),
  concurrent: z.number().int().min(0).max(10_000),
})
export const guardSchema = z
  .object({
    cache: z.object({
      enabled: z.boolean(),
      stream: z.boolean(),
      coalesce: z.boolean(),
      scope: z.enum(['key', 'user']),
      match: z.enum(['exact', 'prefix']),
      prefix_chars: z.number().int().min(128).max(100_000),
      ttl_seconds: z.number().int().min(1).max(86_400),
      max_entries: z.number().int().min(1).max(100_000),
      max_memory_mb: z.number().int().min(1).max(2048),
      max_response_kb: z.number().int().min(1).max(65_536),
      max_subscribers: z.number().int().min(1).max(128),
      timeout_seconds: z.number().int().min(10).max(3600),
      replay_delay_ms: z.number().int().min(0).max(1000),
      charge_percent: z.number().int().min(0).max(100),
      models: modelPatterns,
    }),
    limits: z.object({
      enabled: z.boolean(),
      count_cached_tokens: z.boolean(),
      default_output_tokens: z.number().int().min(1).max(1_000_000),
      rules: z.array(limitRuleSchema).max(200),
    }),
  })
  .superRefine((value, ctx) => {
    if (value.cache.max_response_kb > value.cache.max_memory_mb * 1024) {
      ctx.addIssue({
        code: 'custom',
        path: ['cache', 'max_response_kb'],
        message: 'Invalid request settings',
      })
    }
    if (value.cache.match === 'prefix' && value.cache.models.length === 0) {
      ctx.addIssue({
        code: 'custom',
        path: ['cache', 'models'],
        message: 'Select models before enabling prefix reuse',
      })
    }
  })
export type GuardConfig = z.infer<typeof guardSchema>
export type LimitRule = z.infer<typeof limitRuleSchema>
export type GuardUsage = {
  key: string
  rule_id: string
  classroom_id?: string
  subject: string
  period: string
  quota: number
  requests: number
  tokens: number
  concurrent: number
}
export type GuardStats = {
  classroom_names?: Record<string, string>
  cache: {
    entries: number
    bytes: number
    active: number
    hits: number
    misses: number
    shared: number
    bypassed: number
  }
  usage: GuardUsage[]
  storage_error: string
  quota_per_unit: number
}
type Result<T> = { success: boolean; message?: string; data: T }
export async function getGuardConfig() {
  const response = await api.get<Result<GuardConfig>>('/api/option/relay_guard')
  return guardSchema.parse(requireServerSuccess(response.data).data)
}
export async function saveGuardConfig(value: GuardConfig) {
  const response = await api.put<Result<GuardConfig>>(
    '/api/option/relay_guard',
    value
  )
  return guardSchema.parse(requireServerSuccess(response.data).data)
}
export async function getGuardStats() {
  const response = await api.get<Result<GuardStats>>(
    '/api/option/relay_guard/stats'
  )
  return requireServerSuccess(response.data).data
}
export async function clearGuardCache() {
  const response = await api.delete<Result<never>>(
    '/api/option/relay_guard/cache'
  )
  requireServerSuccess(response.data)
}
export async function resetGuardUsage(key: string) {
  const response = await api.post<Result<never>>(
    '/api/option/relay_guard/reset',
    { key }
  )
  requireServerSuccess(response.data)
}
export function newLimitRule(subject: LimitRule['subject']): LimitRule {
  return {
    id: crypto.randomUUID(),
    name: '',
    enabled: true,
    subject,
    match: '*',
    models: [],
    rpm: 60,
    tpm: 100_000,
    quota: 0,
    period: 'day',
    concurrent: 4,
  }
}
