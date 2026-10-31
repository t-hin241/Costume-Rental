/// <reference types="vite/client" />

declare const __USE_MOCKS__: boolean;

interface ImportMetaEnv {
  /** Empty = same origin. Otherwise an origin of the same site as the API. */
  readonly VITE_API_BASE_URL?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
