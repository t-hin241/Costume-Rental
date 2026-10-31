import { setupWorker } from 'msw/browser';
import { handlers } from './handlers';

export async function startMockApi() {
  await setupWorker(...handlers).start({ quiet: true });
}
