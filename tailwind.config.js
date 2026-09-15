/** @type {import('tailwindcss').Config} */
module.exports = {
  // The admin panel is the only thing Tailwind styles, and it uses nothing but
  // the default theme, so this file stays deliberately small. If a custom
  // colour or font is ever needed, add it under `theme.extend` — then re-run
  // `npm run build:panel` so the committed stylesheet picks it up.
  content: ['./public/admin.html', './public/admin.js'],
  theme: { extend: {} },
  plugins: [],
};
