import path from 'path';
import type { Core } from '@strapi/strapi';
import { isDatabaseClientKind } from '@strapi/database';

// CMS database configuration.
//
// The CMS owns its OWN Postgres database and its OWN schema — it is SEPARATE
// from the rule engine's config store. The CMS never reads/writes the engine
// DB directly; it reaches engine config only via the admin HTTP API.
//   * Different DATABASE than the engine (engine e.g. `matcha`; CMS `strapi_cms`).
//   * Different SCHEMA than the engine (engine `rule_engine`; CMS `strapi_cms`).
//     The CMS must NEVER create tables in `public` or in `rule_engine`.
// Postgres is the configured default. SQLite is a local dev opt-in only
// (DATABASE_CLIENT=sqlite).
const config = ({ env }: Core.Config.Shared.ConfigParams): Core.Config.Database => {
  const client = env('DATABASE_CLIENT', 'postgres');

  if (!isDatabaseClientKind(client)) {
    throw new Error(
      `Unsupported DATABASE_CLIENT: ${client}. Use "postgres", "mysql", or "sqlite".`
    );
  }

  const connections: Record<Core.Config.Database.ClientKind, Core.Config.Database['connection']> = {
    mysql: {
      client: 'mysql',
      connection: {
        host: env('CMS_DB_HOST', 'localhost'),
        port: env.int('CMS_DB_PORT', 3306),
        database: env('CMS_DB_NAME', 'strapi_cms'),
        user: env('CMS_DB_USER', 'strapi_cms'),
        password: env('CMS_DB_PASSWORD', 'strapi_cms'),
        ssl: env.bool('CMS_DB_SSL', false) && {
          key: env('CMS_DB_SSL_KEY', undefined),
          cert: env('CMS_DB_SSL_CERT', undefined),
          ca: env('CMS_DB_SSL_CA', undefined),
          capath: env('CMS_DB_SSL_CAPATH', undefined),
          cipher: env('CMS_DB_SSL_CIPHER', undefined),
          rejectUnauthorized: env.bool('CMS_DB_SSL_REJECT_UNAUTHORIZED', true),
        },
      },
      pool: { min: env.int('CMS_DB_POOL_MIN', 2), max: env.int('CMS_DB_POOL_MAX', 10) },
    },
    postgres: {
      client: 'postgres',
      connection: {
        host: env('CMS_DB_HOST', 'localhost'),
        port: env.int('CMS_DB_PORT', 5432),
        database: env('CMS_DB_NAME', 'strapi_cms'),
        user: env('CMS_DB_USER', 'strapi_cms'),
        password: env('CMS_DB_PASSWORD', 'strapi_cms'),
        ssl: env.bool('CMS_DB_SSL', false) && {
          key: env('CMS_DB_SSL_KEY', undefined),
          cert: env('CMS_DB_SSL_CERT', undefined),
          ca: env('CMS_DB_SSL_CA', undefined),
          capath: env('CMS_DB_SSL_CAPATH', undefined),
          cipher: env('CMS_DB_SSL_CIPHER', undefined),
          rejectUnauthorized: env.bool('CMS_DB_SSL_REJECT_UNAUTHORIZED', true),
        },
        // Dedicated CMS schema — NEVER `public`, NEVER the engine's `rule_engine`.
        // Mirrors the engine's pattern of pinning into its own schema so other
        // tables stay untouched.
        schema: env('CMS_DB_SCHEMA', 'strapi_cms'),
      },
      pool: { min: env.int('CMS_DB_POOL_MIN', 2), max: env.int('CMS_DB_POOL_MAX', 10) },
    },
    sqlite: {
      client: 'sqlite',
      connection: {
        filename: path.join(__dirname, '..', '..', env('DATABASE_FILENAME', '.tmp/data.db')),
      },
      useNullAsDefault: true,
    },
  };

  return {
    connection: {
      ...connections[client],
      acquireConnectionTimeout: env.int('DATABASE_CONNECTION_TIMEOUT', 60000),
    },
  };
};

export default config;
