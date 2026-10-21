import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      globals: globals.browser,
    },
    rules: {
      // 异步数据加载函数在 useEffect 中调用是标准模式（setState 在 await 后执行，非同步）
      'react-hooks/set-state-in-effect': 'warn',
      // useAuth 作为自定义 hook 导出与组件同文件是合理设计，降为警告
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
    },
  },
  // react-refresh 豁免（按文件精准关闭，不全局关）：
  // - main.tsx：应用入口 + 懒加载路由表，本就不参与 HMR
  // - components/ui/**：shadcn 惯例，组件文件同时导出 buttonVariants/toast/confirmDialog 等工具
  // - lib/auth.tsx：AuthProvider 与 useAuth hook 同文件是刻意设计
  {
    files: ['src/main.tsx', 'src/components/ui/**/*.{ts,tsx}', 'src/lib/auth.tsx'],
    rules: { 'react-refresh/only-export-components': 'off' },
  },
])
