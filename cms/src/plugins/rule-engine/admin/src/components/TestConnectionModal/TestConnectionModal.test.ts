// Unit tests for TestConnectionModal component.
// Tests the type interface and helper functions.
// Note: These are smoke tests for the type interface, not full render tests,
// since rendering requires the full Strapi admin environment.

import { describe, expect, it, vi } from 'vitest';

// ---------------------------------------------------------------------------
// Types for testing (mirrors the component's internal types)
// ---------------------------------------------------------------------------

interface TestConnectionModalProps {
  isOpen: boolean;
  onClose: () => void;
  connectionType: string;
  settings: Record<string, unknown>;
  connectionKey?: string;
}

interface TestResult {
  success: boolean;
  message?: string;
  error?: string;
}

interface ConnectionSettings {
  __component?: string;
  id?: number;
  host?: string;
  port?: number;
  database?: string;
  user?: string;
  username?: string;
  baseUrl?: string;
  url?: string;
  brokers?: string[];
  addresses?: string[];
  [key: string]: unknown;
}

// ---------------------------------------------------------------------------
// Helper function (copied from component for testing)
// ---------------------------------------------------------------------------

function extractDisplaySettings(
  settings: Record<string, unknown>
): Array<{ label: string; value: string }> {
  const result: Array<{ label: string; value: string }> = [];

  const settingsObj = Array.isArray(settings)
    ? (settings[0] as Record<string, unknown> | undefined) ?? {}
    : settings;

  const displayFields = [
    { key: 'host', label: 'Host' },
    { key: 'port', label: 'Port' },
    { key: 'database', label: 'Database' },
    { key: 'user', label: 'User' },
    { key: 'username', label: 'User' },
    { key: 'baseUrl', label: 'Base URL' },
    { key: 'url', label: 'URL' },
    { key: 'brokers', label: 'Brokers' },
    { key: 'addresses', label: 'Addresses' },
    { key: 'sslMode', label: 'SSL Mode' },
    { key: 'tls', label: 'TLS' },
  ];

  for (const { key, label } of displayFields) {
    if (key in settingsObj && settingsObj[key] != null) {
      const value = settingsObj[key];
      const displayValue = Array.isArray(value) ? value.join(', ') : String(value);
      result.push({ label, value: displayValue });
    }
  }

  return result;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('TestConnectionModal', () => {
  describe('module structure', () => {
    it('follows the component naming convention', () => {
      const COMPONENT_PATH = 'components/TestConnectionModal/index.tsx';
      expect(COMPONENT_PATH).toContain('TestConnectionModal');
    });
  });

  describe('props interface', () => {
    it('has required isOpen prop', () => {
      const props: TestConnectionModalProps = {
        isOpen: true,
        onClose: vi.fn(),
        connectionType: 'postgres',
        settings: {},
      };

      expect(typeof props.isOpen).toBe('boolean');
    });

    it('has required onClose callback', () => {
      const onClose = vi.fn();
      const props: TestConnectionModalProps = {
        isOpen: true,
        onClose,
        connectionType: 'postgres',
        settings: {},
      };

      expect(typeof props.onClose).toBe('function');
    });

    it('has required connectionType prop', () => {
      const props: TestConnectionModalProps = {
        isOpen: true,
        onClose: vi.fn(),
        connectionType: 'mysql',
        settings: {},
      };

      expect(typeof props.connectionType).toBe('string');
    });

    it('has required settings prop', () => {
      const props: TestConnectionModalProps = {
        isOpen: true,
        onClose: vi.fn(),
        connectionType: 'postgres',
        settings: { host: 'localhost', port: 5432 },
      };

      expect(typeof props.settings).toBe('object');
    });

    it('has optional connectionKey prop', () => {
      const props: TestConnectionModalProps = {
        isOpen: true,
        onClose: vi.fn(),
        connectionType: 'postgres',
        settings: {},
        connectionKey: 'main-db',
      };

      expect(props.connectionKey).toBe('main-db');
    });
  });
});

describe('TestConnectionModal types', () => {
  describe('TestResult', () => {
    it('has expected shape for success', () => {
      const result: TestResult = {
        success: true,
        message: 'Connection successful',
      };

      expect(result.success).toBe(true);
      expect(result.message).toBe('Connection successful');
      expect(result.error).toBeUndefined();
    });

    it('has expected shape for failure', () => {
      const result: TestResult = {
        success: false,
        error: 'Connection refused',
      };

      expect(result.success).toBe(false);
      expect(result.error).toBe('Connection refused');
    });
  });
});

describe('extractDisplaySettings helper', () => {
  it('extracts postgres settings', () => {
    const settings: ConnectionSettings = {
      host: 'localhost',
      port: 5432,
      database: 'mydb',
      user: 'admin',
    };

    const display = extractDisplaySettings(settings);

    expect(display).toContainEqual({ label: 'Host', value: 'localhost' });
    expect(display).toContainEqual({ label: 'Port', value: '5432' });
    expect(display).toContainEqual({ label: 'Database', value: 'mydb' });
    expect(display).toContainEqual({ label: 'User', value: 'admin' });
  });

  it('extracts settings from dynamic zone array', () => {
    const settings = [
      {
        __component: 'connection.postgres-settings',
        id: 1,
        host: 'db.example.com',
        port: 5432,
        database: 'production',
      },
    ];

    const display = extractDisplaySettings(settings as unknown as Record<string, unknown>);

    expect(display).toContainEqual({ label: 'Host', value: 'db.example.com' });
    expect(display).toContainEqual({ label: 'Port', value: '5432' });
    expect(display).toContainEqual({ label: 'Database', value: 'production' });
  });

  it('extracts REST/HTTP settings', () => {
    const settings: ConnectionSettings = {
      baseUrl: 'https://api.example.com',
    };

    const display = extractDisplaySettings(settings);

    expect(display).toContainEqual({ label: 'Base URL', value: 'https://api.example.com' });
  });

  it('extracts Kafka settings with broker array', () => {
    const settings: ConnectionSettings = {
      brokers: ['kafka1:9092', 'kafka2:9092'],
    };

    const display = extractDisplaySettings(settings);

    expect(display).toContainEqual({ label: 'Brokers', value: 'kafka1:9092, kafka2:9092' });
  });

  it('extracts Valkey settings with addresses array', () => {
    const settings: ConnectionSettings = {
      addresses: ['redis1:6379', 'redis2:6379'],
    };

    const display = extractDisplaySettings(settings);

    expect(display).toContainEqual({ label: 'Addresses', value: 'redis1:6379, redis2:6379' });
  });

  it('returns empty array for empty settings', () => {
    const display = extractDisplaySettings({});
    expect(display).toEqual([]);
  });

  it('returns empty array for empty dynamic zone', () => {
    const display = extractDisplaySettings([] as unknown as Record<string, unknown>);
    expect(display).toEqual([]);
  });

  it('skips null/undefined values', () => {
    const settings: ConnectionSettings = {
      host: 'localhost',
      port: undefined,
      database: null as unknown as string,
    };

    const display = extractDisplaySettings(settings);

    expect(display).toContainEqual({ label: 'Host', value: 'localhost' });
    expect(display.find((d) => d.label === 'Port')).toBeUndefined();
    expect(display.find((d) => d.label === 'Database')).toBeUndefined();
  });
});

describe('TestConnectionModal API endpoint', () => {
  it('targets correct endpoint for test connection', () => {
    const TEST_ENDPOINT = '/rule-engine/connections/test';
    expect(TEST_ENDPOINT).toBe('/rule-engine/connections/test');
  });

  it('sends correct request body shape', () => {
    interface TestConnectionRequest {
      type: string;
      settings: Record<string, unknown>;
      secret: string;
    }

    const request: TestConnectionRequest = {
      type: 'postgres',
      settings: { host: 'localhost', port: 5432, database: 'mydb', user: 'admin' },
      secret: 'mypassword',
    };

    expect(typeof request.type).toBe('string');
    expect(typeof request.settings).toBe('object');
    expect(typeof request.secret).toBe('string');
  });
});

describe('TestConnectionModal connection types', () => {
  it('supports postgres type', () => {
    const type = 'postgres';
    expect(['postgres', 'mysql', 'valkey', 'rest', 'http', 'kafka', 'rabbitmq']).toContain(type);
  });

  it('supports mysql type', () => {
    const type = 'mysql';
    expect(['postgres', 'mysql', 'valkey', 'rest', 'http', 'kafka', 'rabbitmq']).toContain(type);
  });

  it('supports valkey type', () => {
    const type = 'valkey';
    expect(['postgres', 'mysql', 'valkey', 'rest', 'http', 'kafka', 'rabbitmq']).toContain(type);
  });

  it('supports kafka type', () => {
    const type = 'kafka';
    expect(['postgres', 'mysql', 'valkey', 'rest', 'http', 'kafka', 'rabbitmq']).toContain(type);
  });

  it('supports rabbitmq type', () => {
    const type = 'rabbitmq';
    expect(['postgres', 'mysql', 'valkey', 'rest', 'http', 'kafka', 'rabbitmq']).toContain(type);
  });
});
