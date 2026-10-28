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
import type { ReactNode } from 'react'
import { useFormContext, type FieldPath } from 'react-hook-form'

import { MultiSelect, type Option } from '@/components/multi-select'
import {
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { SettingsSwitchField } from '../components/settings-form-layout'
import type { GuardConfig } from './api'

type FieldProps = {
  name: FieldPath<GuardConfig>
  label: string
  disabled?: boolean
}
const modelOptions: Option[] = []
export function GuardNumberField(
  props: FieldProps & { min?: number; max?: number; divisor?: number }
) {
  const form = useFormContext<GuardConfig>()
  const divisor = props.divisor ?? 1
  return (
    <FormField
      control={form.control}
      name={props.name}
      render={({ field }) => (
        <FormItem>
          <FormLabel>{props.label}</FormLabel>
          <FormControl>
            <Input
              type='number'
              min={props.min ?? 0}
              max={props.max}
              step={divisor === 1 ? 1 : 'any'}
              disabled={props.disabled}
              name={field.name}
              ref={field.ref}
              onBlur={field.onBlur}
              value={
                typeof field.value === 'number' && Number.isFinite(field.value)
                  ? field.value / divisor
                  : ''
              }
              onChange={(event) => {
                const value = event.target.valueAsNumber
                field.onChange(
                  Number.isFinite(value)
                    ? Math.round(value * divisor)
                    : Number.NaN
                )
              }}
            />
          </FormControl>
          <FormMessage />
        </FormItem>
      )}
    />
  )
}
export function GuardSwitchField(props: FieldProps & { description?: string }) {
  const form = useFormContext<GuardConfig>()
  return (
    <FormField
      control={form.control}
      name={props.name}
      render={({ field }) => (
        <FormItem>
          <SettingsSwitchField
            controlId={props.name}
            checked={field.value === true}
            onCheckedChange={field.onChange}
            label={props.label}
            description={props.description}
            disabled={props.disabled}
          />
          <FormMessage />
        </FormItem>
      )}
    />
  )
}
export function GuardSelectField(props: FieldProps & { children: ReactNode }) {
  const form = useFormContext<GuardConfig>()
  return (
    <FormField
      control={form.control}
      name={props.name}
      render={({ field }) => (
        <FormItem>
          <FormLabel>{props.label}</FormLabel>
          <FormControl>
            <NativeSelect
              disabled={props.disabled}
              name={field.name}
              ref={field.ref}
              onBlur={field.onBlur}
              value={String(field.value)}
              onChange={field.onChange}
            >
              {props.children}
            </NativeSelect>
          </FormControl>
          <FormMessage />
        </FormItem>
      )}
    />
  )
}
export { NativeSelectOption }
export function GuardTextField(
  props: FieldProps & { placeholder?: string; models?: boolean }
) {
  const form = useFormContext<GuardConfig>()
  return (
    <FormField
      control={form.control}
      name={props.name}
      render={({ field }) => (
        <FormItem>
          <FormLabel>{props.label}</FormLabel>
          <FormControl>
            {props.models ? (
              <MultiSelect
                options={modelOptions}
                selected={
                  Array.isArray(field.value)
                    ? field.value.filter(
                        (value): value is string => typeof value === 'string'
                      )
                    : []
                }
                onChange={field.onChange}
                placeholder={props.placeholder}
                disabled={props.disabled}
                allowCreate
                maxVisibleChips={6}
              />
            ) : (
              <Input
                disabled={props.disabled}
                placeholder={props.placeholder}
                name={field.name}
                ref={field.ref}
                onBlur={field.onBlur}
                value={String(field.value ?? '')}
                onChange={field.onChange}
              />
            )}
          </FormControl>
          <FormMessage />
        </FormItem>
      )}
    />
  )
}
