import js from '@eslint/js'
import reactHooks from 'eslint-plugin-react-hooks'
import globals from 'globals'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  { ignores: ['node_modules', 'public', '../internal/web/dist', 'playwright-report', 'test-results'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ['**/*.{ts,tsx}'],
    languageOptions: { globals: { ...globals.browser } },
    plugins: { 'react-hooks': reactHooks },
    rules: { ...reactHooks.configs.recommended.rules },
  },
  {
    // Colours come from the semantic tokens in src/index.css only (F8, docs/design-system.md).
    files: ['src/**/*.{ts,tsx}'],
    ignores: ['src/**/*.test.{ts,tsx}'],
    rules: {
      'no-restricted-syntax': [
        'error',
        {
          selector: 'Literal[value=/#[0-9a-fA-F]{3,8}\\b|\\b(rgb|rgba|hsl|hsla|oklch|oklab|hwb)\\(/]',
          message: 'No one-off colours: use a semantic token (see docs/design-system.md).',
        },
        {
          selector:
            'Literal[value=/\\b(bg|text|border|ring|outline|fill|stroke|from|to|via|divide|shadow|decoration|caret|accent)-((slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-[0-9]{2,3}|white|black)\\b/]',
          message: 'Tailwind default-palette colours are not part of the theme: use a semantic token.',
        },
        {
          selector: 'TemplateElement[value.raw=/#[0-9a-fA-F]{3,8}\\b|\\b(rgb|rgba|hsl|hsla|oklch|oklab|hwb)\\(/]',
          message: 'No one-off colours: use a semantic token (see docs/design-system.md).',
        },
      ],
    },
  },
  { files: ['*.config.{js,ts}', 'e2e/**'], languageOptions: { globals: { ...globals.node } } },
)
