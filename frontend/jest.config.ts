import type { Config } from 'jest';
import nextJest from 'next/jest.js';

const createJestConfig = nextJest({
  // Provide the path to your Next.js app to load next.config.js and .env files in your test environment
  dir: './',
});

// Add any custom config to be passed to Jest
const config: Config = {
  coverageProvider: 'v8',
  testEnvironment: 'jsdom',
  // Add more setup options before each test is run
  setupFilesAfterEnv: ['<rootDir>/jest.setup.ts'],
  moduleNameMapper: {
    // Handle module aliases (this will match the paths defined in tsconfig.json)
    '^@/(.*)$': '<rootDir>/$1',
    // Handle CSS imports (with CSS modules)
    '^.+\\.module\\.(css|sass|scss)$': 'identity-obj-proxy',
    // Handle CSS imports (without CSS modules)
    '^.+\\.(css|sass|scss)$': '<rootDir>/__mocks__/styleMock.js',
    // Handle image imports
    '^.+\\.(png|jpg|jpeg|gif|webp|avif|ico|bmp|svg)$/i': '<rootDir>/__mocks__/fileMock.js',
  },
  testPathIgnorePatterns: ['<rootDir>/node_modules/', '<rootDir>/.next/', '<rootDir>/e2e/'],
  transformIgnorePatterns: [
    '/node_modules/',
    '^.+\\.module\\.(css|sass|scss)$',
  ],
  collectCoverageFrom: [
    '**/*.{js,jsx,ts,tsx}',
    '!**/*.d.ts',
    '!**/node_modules/**',
    '!**/.next/**',
    '!**/e2e/**',
    '!**/coverage/**',
    '!**/jest.config.ts',
    '!**/jest.setup.ts',
    '!**/next.config.js',
    '!**/postcss.config.js',
    '!**/tailwind.config.ts',
    '!**/__tests__/**',
    '!**/__mocks__/**',
    // Exclude pages (tested via E2E)
    '!**/app/**/page.tsx',
    '!**/app/**/layout.tsx',
    '!**/app/**/route.ts',
    // Exclude complex audio/video components (require specialized testing)
    '!**/audio-uploader.tsx',
    '!**/audio-waveform.tsx',
    '!**/stem-mixer.tsx',
    // Streaming lab browser glue (AudioContext/AudioWorklet/fetch, verified in Chrome)
    '!**/lib/lab-stream/engine.ts',
    '!**/lib/lab-stream/stem-stream.ts',
    '!**/lib/lab-stream/memory.ts',
    '!**/lib/lab-stream/testing/**',
    '!**/app/lab/**',
    // Exclude config file
    '!**/playwright.config.ts',
  ],
  coverageThreshold: {
    global: {
      branches: 85,
      functions: 85,
      lines: 85,
      statements: 85,
    },
  },
};

// createJestConfig is exported this way to ensure that next/jest can load the Next.js config which is async
export default createJestConfig(config);
