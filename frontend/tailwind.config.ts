import containerQueries from '@tailwindcss/container-queries'
// @ts-expect-error Plugin does not export types
import clipPath from 'tailwind-clip-path'
import { Config } from 'tailwindcss'

export const colorSystem = {
  neutral: {
    1: '#fff',
    2: '#fdfdfd',
    3: '#f6f6f6',
    4: '#f1f1f2',
    5: '#dcddde',
    6: '#c5c6c7',
    7: '#97989b',
    8: '#686a6e',
    9: '#56585c',
    10: '#3a3c41',
    11: '#33353b',
    12: '#2a2c32',
    13: '#202228',
    14: '#171a20',
    15: '#0B0E14',
  },
  red: {
    1: '#ffecec',
    2: '#ffd0d0',
    3: '#ffacac',
    4: '#ff8585',
    5: '#ff6060',
    6: '#ff3d3d',
    7: '#d93434',
    8: '#b52b2b',
    9: '#912323',
    10: '#731b1b',
  },
  pink: {
    1: '#ffecf1',
    2: '#ffd0de',
    3: '#ffacc5',
    4: '#ff85a9',
    5: '#ff608f',
    6: '#ff3d77',
    7: '#d93465',
    8: '#b52b54',
    9: '#912344',
    10: '#731b36',
  },
  purple: {
    1: '#f9ecff',
    2: '#f0d0ff',
    3: '#e4acff',
    4: '#d885ff',
    5: '#cc60ff',
    6: '#c13dff',
    7: '#a434d9',
    8: '#892bb5',
    9: '#6e2391',
    10: '#571b73',
  },
  violet: {
    1: '#f1ecff',
    2: '#ded2ff',
    3: '#c4afff',
    4: '#a88aff',
    5: '#8e66ff',
    6: '#7545ff',
    7: '#633bd9',
    8: '#5331b5',
    9: '#432791',
    10: '#351f73',
  },
  blue: {
    1: '#edf4ff',
    2: '#d4e5ff',
    3: '#b2d1ff',
    4: '#8ebbff',
    5: '#6ba6ff',
    6: '#4b93ff',
    7: '#407dd9',
    8: '#3568b5',
    9: '#2b5491',
    10: '#224273',
  },
  lightBlue: {
    1: '#ecffff',
    2: '#d0ffff',
    3: '#acffff',
    4: '#85ffff',
    5: '#60ffff',
    6: '#3dffff',
    7: '#34d9d9',
    8: '#2bb5b5',
    9: '#239191',
    10: '#1b7373',
  },
  green: {
    1: '#ecffec',
    2: '#d1ffd0',
    3: '#adffac',
    4: '#87ff85',
    5: '#62ff60',
    6: '#40ff3d',
    7: '#36d934',
    8: '#2db52b',
    9: '#249123',
    10: '#1d731b',
  },
  lime: {
    1: '#faffec',
    2: '#f3ffd0',
    3: '#e9ffac',
    4: '#dfff85',
    5: '#d5ff60',
    6: '#ccff3d',
    7: '#add934',
    8: '#91b52b',
    9: '#749123',
    10: '#5c731b',
    11: '#4e6217',
    12: '#3c4b12',
  },
  yellow: {
    1: '#fffeec',
    2: '#fffdd0',
    3: '#fffcac',
    4: '#fffa85',
    5: '#fff860',
    6: '#fff73d',
    7: '#d9d234',
    8: '#b5af2b',
    9: '#918d23',
    10: '#736f1b',
  },
  amber: {
    1: '#fffaec',
    2: '#fff2d0',
    3: '#ffe8ac',
    4: '#ffdd85',
    5: '#ffd260',
    6: '#ffc93d',
    7: '#d9ab34',
    8: '#b58e2b',
    9: '#917323',
    10: '#735a1b',
  },
  orange: {
    1: '#fff5ec',
    2: '#ffe7d0',
    3: '#ffd4ac',
    4: '#ffbf85',
    5: '#ffac60',
    6: '#ff9a3d',
    7: '#d98334',
    8: '#b56d2b',
    9: '#915823',
    10: '#73451b',
  },
} as const

const config: Config = {
  content: ['./index.html', './src/**/*.{js,ts,jsx,tsx}'],
  theme: {
    extend: {
      fontSize: {
        xxs: '10px',
      },
      colors: {
        base: {
          dark: '#0b0e14',
          light: '#0b0e14',
        },
        ...colorSystem,
      },
      spacing: {
        sidebar: '480px',
        'col-md': '300px',
        'col-actions': '185px',
        'col-actions-xs': '50px',
        'auth-block': '448px',
      },
      borderRadius: {
        'node-card': '56px', // This doesn't match designs pixel-perfect, but is nice considering we only needed custom classes
      },
    },
    clipPath: {
      'node-card':
        'polygon(0 0, 100% 0, 100% calc(100% - 40px), calc(100% - 40px) 100%, 0 100%)',
    },
    fontFamily: {
      mono: [
        'JetBrains Mono',
        'ui-monospace',
        'SFMono-Regular',
        'Menlo',
        'Monaco',
        'Consolas',
        'Liberation Mono',
        'Courier New',
        'monospace',
      ],
    },
    keyframes: {
      // The identify blink: alternate between the lit blue and the node's
      // ACTUAL status colour, so a blinking node still reports whether it is
      // online or in recovery. Both halves come from CSS variables the row
      // sets, because the resting colour is per node and a keyframe cannot be
      // handed a Tailwind class. Hard cut, not a fade — this is a beacon.
      identifyBlink: {
        '0%, 49%': {
          backgroundColor: 'var(--tube-lit)',
          boxShadow: 'var(--tube-lit-glow)',
        },
        '50%, 100%': {
          backgroundColor: 'var(--tube-rest)',
          boxShadow: 'var(--tube-rest-glow)',
        },
      },
      sizeWiggle: {
        '0%': { transform: 'scale(100%)' },
        '16%': { transform: 'scale(115%)' },
        '28%': { transform: 'scale(95%)' },
        '55%': { transform: 'scale(129%)' },
        '80%': { transform: 'scale(92%)' },
        '100%': { transform: 'scale(100%)' },
      },
    },
    animation: {
      identifyBlink: 'identifyBlink 1.1s infinite',
      sizeWiggle: 'sizeWiggle 2.5s infinite',
    },
  },
  plugins: [clipPath, containerQueries],
}

export default config
