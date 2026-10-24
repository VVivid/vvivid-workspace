// Force dark mode everywhere, even if an older browser session saved light mode.
(function () {
  document.documentElement.classList.add('dark', 'darkmode');
  document.documentElement.classList.remove('light');
  localStorage.setItem('theme', 'dark');
})();
