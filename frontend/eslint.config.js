import pluginJs from '@eslint/js'
import eslintConfigPrettier from 'eslint-config-prettier'
import react from 'eslint-plugin-react'
import globals from 'globals'
import tsEslint from 'typescript-eslint'

export default [
  pluginJs.configs.recommended,
  ...tsEslint.configs.recommended,
  eslintConfigPrettier,
  {
    files: ['**/*.{js,mjs,cjs,ts,jsx,tsx}'],
    languageOptions: {
      globals: globals.browser,
    },
    plugins: {
      react,
    },
    settings: {
      react: {
        version: 'detect',
      },
    },
    rules: {
      'react/react-in-jsx-scope': 0,
      'react/function-component-definition': [
        2,
        {
          namedComponents: 'arrow-function',
          unnamedComponents: 'arrow-function',
        },
      ],
      'react/button-has-type': [
        2,
        {
          button: true,
          submit: true,
          reset: true,
        },
      ],
      'react/no-array-index-key': 2,
      'react/jsx-key': 2,
      'no-console': 2,
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
      ],
    },
  },
]
