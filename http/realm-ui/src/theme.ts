import { createTheme } from 'flowbite-react';

/**
 * Flowbite overrides that give realm its look; merged into the defaults. Colors
 * come from the palette variables in index.css. Surfaces are flat and outlined
 * rather than shadowed, and dark modals sit on the same panel color as the page.
 */
export const realmTheme = createTheme({
  button: {
    base: 'rounded-md focus:ring-2',
  },
  badge: {
    root: {
      color: {
        info: 'bg-primary-100 text-primary-800 hover:bg-primary-200 dark:bg-primary-900 dark:text-primary-200 dark:hover:bg-primary-800',
      },
    },
  },
  card: {
    root: {
      base: 'rounded-xl shadow-none',
    },
  },
  modal: {
    content: {
      inner: 'rounded-xl shadow-xl dark:bg-gray-800',
    },
    header: {
      base: 'border-gray-200 dark:border-gray-700',
      title: 'text-lg font-semibold',
    },
    footer: {
      base: 'border-t border-gray-200 px-6 py-4 dark:border-gray-700',
    },
  },
  drawer: {
    root: {
      base: 'border-l border-gray-200 dark:border-gray-700',
    },
  },
});
