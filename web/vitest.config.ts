import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

export default defineConfig({
  // NOTE: this file lives alongside vite.config.ts and is what vitest picks up.
  // It must redeclare the react plugin (vitest.config.ts fully overrides
  // vite.config.ts for the test runner).
  plugins: [react()],
  test: {
    // React resolves console.error dynamically at call time; vitest's default
    // console intercept installs its own wrapper before test spies can see
    // React warnings. Keep the real console so JsonEditor.test.tsx can spy on
    // the "Cannot update a component while rendering" warning that the
    // render-phase onValid bug used to trigger.
    disableConsoleIntercept: true,
  },
});
