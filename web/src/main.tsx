import '@fontsource/be-vietnam-pro/400.css';
import '@fontsource/be-vietnam-pro/500.css';
import '@fontsource/be-vietnam-pro/600.css';
import '@fontsource/be-vietnam-pro/700.css';
import '@fontsource/be-vietnam-pro/800.css';
import './styles/app.css';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';

async function boot() {
  // Removed from production builds: __USE_MOCKS__ is false there, so the
  // mocks chunk is never emitted.
  if (__USE_MOCKS__) {
    const { startMockApi } = await import('./mocks/browser');
    await startMockApi();
  }
  const root = document.getElementById('root');
  if (!root) throw new Error('missing #root');
  createRoot(root).render(<StrictMode><App /></StrictMode>);
}

void boot();
