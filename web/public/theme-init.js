// 首屏防闪：在样式生效前按记忆的主题挂 dark class（与 ThemeToggle 的 saig_theme 约定一致）。
// 独立成同源文件而非内联 <script>：站点 CSP 为 script-src 'self'，不放行内联脚本。
if (localStorage.getItem('saig_theme') === 'dark') document.documentElement.classList.add('dark')
