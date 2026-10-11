// Apply the saved theme before first paint to avoid a flash. A separate file (not an inline
// script) so the Content-Security-Policy can forbid inline scripts.
try {
  var m = localStorage.getItem('graphly:theme') || 'system'
  var dark = m === 'dark' || (m === 'system' && matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  document.documentElement.style.colorScheme = dark ? 'dark' : 'only light'
} catch (e) {}
