// 遵循project_guide.md
/** @type {import('tailwindcss').Config} */
module.exports = {
  content: [
    "./internal/web/templates/**/*.templ",
    "./internal/web/**/*.go"
  ],
  // Class-based dark mode: <html class="dark"> triggers .dark { --gb-* } overrides in input.css.
  // All semantic color tokens are defined as CSS custom properties — no template changes needed.
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        // ── Semantic color tokens ──────────────────────────────────────────────
        // Values are CSS custom properties defined in input.css (:root = light, .dark = dark).
        // The rgb(var(...) / <alpha-value>) format enables Tailwind's opacity utilities
        // (bg-primary/10, bg-background/50, etc.) to work with dynamic CSS variables.
        primary: {
          DEFAULT: 'rgb(var(--gb-primary) / <alpha-value>)',
          hover:   'rgb(var(--gb-primary-hover) / <alpha-value>)',
          soft:    'rgb(var(--gb-primary-soft) / <alpha-value>)',
          focus:   'rgb(var(--gb-primary-focus) / <alpha-value>)',
        },
        success: {
          DEFAULT: 'rgb(var(--gb-success) / <alpha-value>)',
          hover:   'rgb(var(--gb-success-hover) / <alpha-value>)',
          soft:    'rgb(var(--gb-success-soft) / <alpha-value>)',
          border:  'rgb(var(--gb-success-border) / <alpha-value>)',
          focus:   'rgb(var(--gb-success-focus) / <alpha-value>)',
        },
        warning: {
          DEFAULT: 'rgb(var(--gb-warning) / <alpha-value>)',
          hover:   'rgb(var(--gb-warning-hover) / <alpha-value>)',
          soft:    'rgb(var(--gb-warning-soft) / <alpha-value>)',
          border:  'rgb(var(--gb-warning-border) / <alpha-value>)',
          focus:   'rgb(var(--gb-warning-focus) / <alpha-value>)',
        },
        danger: {
          DEFAULT: 'rgb(var(--gb-danger) / <alpha-value>)',
          hover:   'rgb(var(--gb-danger-hover) / <alpha-value>)',
          soft:    'rgb(var(--gb-danger-soft) / <alpha-value>)',
          border:  'rgb(var(--gb-danger-border) / <alpha-value>)',
          focus:   'rgb(var(--gb-danger-focus) / <alpha-value>)',
        },
        background: {
          DEFAULT: 'rgb(var(--gb-background) / <alpha-value>)',
        },
        text: {
          DEFAULT: 'rgb(var(--gb-text) / <alpha-value>)',
          muted:   'rgb(var(--gb-text-muted) / <alpha-value>)',
          muted2:  'rgb(var(--gb-text-muted2) / <alpha-value>)',
          muted3:  'rgb(var(--gb-text-muted3) / <alpha-value>)',
        },
        border: {
          DEFAULT: 'rgb(var(--gb-border) / <alpha-value>)',
          input:   'rgb(var(--gb-border-input) / <alpha-value>)',
          subtle:  'rgb(var(--gb-border-subtle) / <alpha-value>)',
          danger:  'rgb(var(--gb-border-danger) / <alpha-value>)',
        },
        surface: {
          DEFAULT: 'rgb(var(--gb-surface) / <alpha-value>)',
        },
        onPrimary: 'rgb(var(--gb-on-primary) / <alpha-value>)',
        disabled: {
          bg:   'rgb(var(--gb-disabled-bg) / <alpha-value>)',
          text: 'rgb(var(--gb-disabled-text) / <alpha-value>)',
        },
        dangerText: {
          DEFAULT: 'rgb(var(--gb-danger-text) / <alpha-value>)',
        },
      },
      fontSize: {
        // Typography scale
        title:   ['1.5rem',   { lineHeight: '2rem' }],    // 24px
        section: ['1rem',     { lineHeight: '1.5rem' }],  // 16px
        body:    ['0.875rem', { lineHeight: '1.25rem' }], // 14px
        small:   ['0.75rem',  { lineHeight: '1rem' }],    // 12px
      },
    }
  },
  plugins: []
};
