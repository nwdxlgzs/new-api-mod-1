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

export const classroomSchema = z
  .object({
    name: z.string().trim().min(1).max(200),
    course: z.string().max(200),
    teacher: z.string().max(100),
    note: z.string().max(2000),
    key_id: z.number().int().positive(),
    ips: z.array(z.string().trim().min(1)).min(1).max(256),
    rule_ids: z.array(z.string().min(1)).min(1).max(200),
  })
  .refine((value) => !value.ips.includes('*') || value.ips.length === 1, {
    path: ['ips'],
    message: 'Use * alone to match all classroom IP addresses',
  })
export type ClassroomInput = z.infer<typeof classroomSchema>
export type Classroom = ClassroomInput & {
  id: string
  started_at: number
  ended_at: number
  concurrent: number
  reset_count: number
}
export type TeachingUsage = {
  classroom_id: string
  date: string
  ip: string
  model: string
  group: string
  source: 'UPSTREAM' | 'HIT' | 'SHARED'
  billing_status: 'settled' | 'failed' | 'unbilled'
  quota_per_unit: number
  prompt_tokens: number
  completion_tokens: number
  requests: number
  tokens: number
  quota: number
  first_at: number
  last_at: number
}
export type ClassroomBill = {
  classroom: Classroom
  rows: TeachingUsage[]
  summary: {
    requests: number
    tokens: number
    quota: number
    amount_usd: number
    reused_requests: number
    unsettled_requests: number
    ips: number
  }
  resets: { classroom_id: string; at: number; subjects: number }[]
  timezone: string
}
type Result<T> = { success: boolean; message?: string; data: T }
const classroomPath = '/api/option/relay_guard/classrooms'

export async function getClassrooms(): Promise<Classroom[]> {
  const response = await api.get<Result<Classroom[]>>(classroomPath)
  return requireServerSuccess(response.data).data
}
export async function startClassroom(
  input: ClassroomInput
): Promise<Classroom> {
  const response = await api.post<Result<Classroom>>(classroomPath, input)
  return requireServerSuccess(response.data).data
}
export async function changeClassroom(
  id: string,
  action: 'reset' | 'end'
): Promise<void> {
  const response = await api.post<Result<unknown>>(
    `${classroomPath}/${encodeURIComponent(id)}/${action}`
  )
  requireServerSuccess(response.data)
}
export async function getClassroomBill(
  id: string,
  from: string,
  to: string
): Promise<ClassroomBill> {
  const response = await api.get<Result<ClassroomBill>>(
    `${classroomPath}/${encodeURIComponent(id)}/bill`,
    { params: { from, to } }
  )
  return requireServerSuccess(response.data).data
}
