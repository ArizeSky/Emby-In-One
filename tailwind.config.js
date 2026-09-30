/** @type {import('tailwindcss').Config} */
module.exports = {
  // Both clients use the shared stylesheet and default theme. If a custom
  // colour or font is ever needed, add it under `theme.extend` — then re-run
  // `npm run build:panel` so the committed stylesheet picks it up.
  content: ['./public/admin.html', './public/admin.js', './public/web/*.html', './public/web/*.js'],
  theme: { extend: {} },
  plugins: [],
};
