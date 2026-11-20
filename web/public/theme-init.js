// Applies the stored theme before first paint so a reload does not flash. It is a
// separate file, not an inline script, so the Content-Security-Policy needs no
// 'unsafe-inline' for scripts.
try {
  var t = localStorage.getItem('ledger.theme')
  if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t
} catch (e) {
  // Storage is unavailable: follow the system preference.
}
