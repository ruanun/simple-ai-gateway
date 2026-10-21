import { useState } from 'react'
import { Check, ChevronDown } from 'lucide-react'
import { Popover, PopoverContent, PopoverTrigger } from './popover'
import { Command, CommandEmpty, CommandInput, CommandItem, CommandList } from './command'
import { cn } from '../../lib/utils'

// Combobox：可搜索下拉（shadcn Popover+Command 组合），受控 API 对齐 SimpleSelect，
// 适合选项多需要过滤的场景（如几十个模型里选一个）；选项少的场景继续用 SimpleSelect。

export type ComboboxOption = { value: string; label?: string; disabled?: boolean }

export function Combobox({ value, onChange, options, placeholder = '选择…', searchPlaceholder = '搜索…', emptyText = '无匹配项', className }: {
  value: string
  onChange: (v: string) => void
  options: ComboboxOption[]
  placeholder?: string
  searchPlaceholder?: string
  emptyText?: string
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const current = options.find((o) => o.value === value)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        role="combobox"
        aria-expanded={open}
        className={cn(
          'inline-flex cursor-pointer items-center justify-between gap-1.5 whitespace-nowrap rounded-md border border-[var(--border)] bg-[var(--card)] px-2 py-1 text-sm outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)]',
          !current && 'text-[var(--muted-foreground)]',
          className,
        )}
      >
        <span className="truncate">{current ? (current.label ?? current.value) : placeholder}</span>
        <ChevronDown className="h-4 w-4 shrink-0 opacity-50" />
      </PopoverTrigger>
      <PopoverContent className="w-[var(--radix-popover-trigger-width)] min-w-48 p-0">
        <Command>
          <CommandInput placeholder={searchPlaceholder} />
          <CommandList>
            <CommandEmpty>{emptyText}</CommandEmpty>
            {options.map((o) => (
              <CommandItem
                key={o.value}
                value={o.value}
                keywords={o.label ? [o.label] : undefined}
                disabled={o.disabled}
                onSelect={(v) => { onChange(v); setOpen(false) }}
              >
                <Check className={cn('h-4 w-4 shrink-0 text-[var(--primary)]', o.value === value ? 'opacity-100' : 'opacity-0')} />
                <span className="truncate">{o.label ?? o.value}</span>
              </CommandItem>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
