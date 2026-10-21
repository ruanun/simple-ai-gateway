import { Moon, Sun } from 'lucide-react'
import { useEffect, useState } from 'react'

// 主题持久化：localStorage 记忆选择，首屏防闪由 index.html 内联脚本负责
const KEY = 'saig_theme'

export function ThemeToggle() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains('dark'))
  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark)
    localStorage.setItem(KEY, dark ? 'dark' : 'light')
  }, [dark])
  return <button className="rounded-md p-2 hover:bg-[var(--muted)] cursor-pointer" onClick={() => setDark(v => !v)} aria-label="切换主题">
    {dark ? <Sun size={18} /> : <Moon size={18} />}
  </button>
}
