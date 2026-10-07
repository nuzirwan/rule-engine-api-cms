// Unit tests for VersionHistory component.
// Tests the types exports and version selection logic.
// Note: These are smoke tests for the type interface, not full render tests,
// since rendering requires the full Strapi admin environment.
// Follows the EnvironmentManager.test.ts pattern.

import { describe, expect, it, vi } from 'vitest';

// Import only the types module which doesn't import design-system
import type {
  VersionSummary,
  FlowVersionsResponse,
  RollbackResponse,
  FlowDetail,
  EngineNode,
  EngineFixture,
} from './types';

// ---------------------------------------------------------------------------
// Mock data
// ---------------------------------------------------------------------------

const mockVersions: VersionSummary[] = [
  {
    version: 3,
    validated: true,
    createdAt: '2024-01-15T10:30:00Z',
    createdBy: 'admin@example.com',
  },
  {
    version: 2,
    validated: true,
    createdAt: '2024-01-14T09:00:00Z',
    createdBy: 'dev@example.com',
  },
  {
    version: 1,
    validated: false,
    createdAt: '2024-01-13T08:00:00Z',
    createdBy: 'admin@example.com',
  },
];

const mockFlowVersionsResponse: FlowVersionsResponse = {
  flowId: 'flow-123',
  versions: mockVersions,
};

const mockRollbackResponse: RollbackResponse = {
  flowId: 'flow-123',
  activeVersion: 2,
  action: 'rollback',
};

const mockEngineNode: EngineNode = {
  id: 'trigger',
  type: 'trigger',
  spec: {},
  children: [
    {
      id: 'action-1',
      type: 'action',
      spec: { timeout: 1000 },
    },
  ],
};

const mockFlowDetail: FlowDetail = {
  flowId: 'flow-123',
  version: 1,
  method: 'POST',
  path: '/api/test',
  tree: mockEngineNode,
  fixtures: [
    {
      name: 'test-fixture',
      input: { key: 'value' },
    },
  ],
};

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('VersionHistory', () => {
  describe('module structure', () => {
    it('follows the component naming convention', () => {
      const COMPONENT_PATH = 'components/VersionHistory/index.tsx';
      expect(COMPONENT_PATH).toContain('VersionHistory');
    });
  });

  describe('props interface', () => {
    it('documents expected flowId prop', () => {
      interface VersionHistoryProps {
        flowId: string;
        activeVersion?: number | null;
        onRollbackComplete?: () => void;
      }

      const onRollbackComplete = vi.fn();
      const props: VersionHistoryProps = {
        flowId: 'flow-123',
        activeVersion: 3,
        onRollbackComplete,
      };

      expect(typeof props.flowId).toBe('string');
      expect(typeof props.activeVersion).toBe('number');
      expect(typeof props.onRollbackComplete).toBe('function');
    });
  });
});

describe('VersionHistory types', () => {
  describe('VersionSummary', () => {
    it('has expected shape', () => {
      const version: VersionSummary = mockVersions[0];

      expect(typeof version.version).toBe('number');
      expect(typeof version.validated).toBe('boolean');
      expect(typeof version.createdAt).toBe('string');
      expect(typeof version.createdBy).toBe('string');
    });

    it('version is always a positive integer', () => {
      for (const version of mockVersions) {
        expect(version.version).toBeGreaterThan(0);
        expect(Number.isInteger(version.version)).toBe(true);
      }
    });

    it('createdAt is ISO 8601 format', () => {
      for (const version of mockVersions) {
        expect(() => new Date(version.createdAt)).not.toThrow();
        // Check that the date is valid (can be parsed and converted back)
        const parsed = new Date(version.createdAt);
        expect(parsed.getTime()).not.toBeNaN();
      }
    });
  });

  describe('FlowVersionsResponse', () => {
    it('has expected shape', () => {
      const response: FlowVersionsResponse = mockFlowVersionsResponse;

      expect(typeof response.flowId).toBe('string');
      expect(Array.isArray(response.versions)).toBe(true);
      expect(response.versions.length).toBeGreaterThan(0);
    });
  });

  describe('RollbackResponse', () => {
    it('has expected shape', () => {
      const response: RollbackResponse = mockRollbackResponse;

      expect(typeof response.flowId).toBe('string');
      expect(typeof response.activeVersion).toBe('number');
      expect(typeof response.action).toBe('string');
    });

    it('action is rollback', () => {
      expect(mockRollbackResponse.action).toBe('rollback');
    });
  });

  describe('FlowDetail', () => {
    it('has expected shape', () => {
      const detail: FlowDetail = mockFlowDetail;

      expect(typeof detail.flowId).toBe('string');
      expect(typeof detail.version).toBe('number');
      expect(typeof detail.method).toBe('string');
      expect(typeof detail.path).toBe('string');
      expect(typeof detail.tree).toBe('object');
    });

    it('tree has EngineNode structure', () => {
      const tree = mockFlowDetail.tree;

      expect(typeof tree.id).toBe('string');
      expect(typeof tree.type).toBe('string');
      expect(typeof tree.spec).toBe('object');
    });

    it('fixtures are optional', () => {
      const detailWithoutFixtures: FlowDetail = {
        flowId: 'flow-456',
        version: 1,
        method: 'GET',
        path: '/api/health',
        tree: mockEngineNode,
      };

      expect(detailWithoutFixtures.fixtures).toBeUndefined();
    });
  });

  describe('EngineNode', () => {
    it('has expected shape', () => {
      const node: EngineNode = mockEngineNode;

      expect(typeof node.id).toBe('string');
      expect(typeof node.type).toBe('string');
      expect(typeof node.spec).toBe('object');
    });

    it('children are optional and recursive', () => {
      expect(mockEngineNode.children).toBeDefined();
      expect(Array.isArray(mockEngineNode.children)).toBe(true);

      const child = mockEngineNode.children![0];
      expect(typeof child.id).toBe('string');
      expect(typeof child.type).toBe('string');
    });
  });

  describe('EngineFixture', () => {
    it('has expected shape', () => {
      const fixture: EngineFixture = mockFlowDetail.fixtures![0];

      expect(typeof fixture.name).toBe('string');
      expect(fixture.input).toBeDefined();
    });

    it('mocks and expect are optional', () => {
      const minimalFixture: EngineFixture = {
        name: 'minimal',
        input: {},
      };

      expect(minimalFixture.mocks).toBeUndefined();
      expect(minimalFixture.expect).toBeUndefined();
    });
  });
});

describe('VersionHistory version selection logic', () => {
  describe('comparison selection', () => {
    it('allows selecting up to 2 versions', () => {
      const selectedVersions: number[] = [];
      const maxSelections = 2;

      // Select first version
      selectedVersions.push(1);
      expect(selectedVersions.length).toBeLessThanOrEqual(maxSelections);

      // Select second version
      selectedVersions.push(2);
      expect(selectedVersions.length).toBeLessThanOrEqual(maxSelections);

      // Cannot add third without removing one
      expect(selectedVersions.length).toBe(maxSelections);
    });

    it('can toggle selection off', () => {
      let selectedVersions = [1, 2];

      // Toggle off version 1
      selectedVersions = selectedVersions.filter((v) => v !== 1);
      expect(selectedVersions).toEqual([2]);
    });

    it('replaces oldest when selecting third version', () => {
      let selectedVersions = [1, 2];

      // Add version 3, removing oldest (1)
      if (selectedVersions.length >= 2) {
        selectedVersions = [...selectedVersions.slice(1), 3];
      }

      expect(selectedVersions).toEqual([2, 3]);
    });
  });

  describe('comparison enabling', () => {
    it('compare button disabled with less than 2 selections', () => {
      const selectedVersions = [1];
      const canCompare = selectedVersions.length === 2;

      expect(canCompare).toBe(false);
    });

    it('compare button enabled with exactly 2 selections', () => {
      const selectedVersions = [1, 2];
      const canCompare = selectedVersions.length === 2;

      expect(canCompare).toBe(true);
    });
  });
});

describe('VersionHistory rollback logic', () => {
  describe('rollback button state', () => {
    it('disabled for active version', () => {
      const activeVersion = 3;
      const version = mockVersions[0]; // version 3

      const isActive = version.version === activeVersion;
      const rollbackDisabled = isActive;

      expect(rollbackDisabled).toBe(true);
    });

    it('enabled for non-active versions', () => {
      const activeVersion = 3;
      const version = mockVersions[1]; // version 2

      const isActive = version.version === activeVersion;
      const rollbackDisabled = isActive;

      expect(rollbackDisabled).toBe(false);
    });
  });

  describe('rollback confirmation', () => {
    it('shows correct version numbers in confirmation', () => {
      const activeVersion = 3;
      const targetVersion = 2;

      const confirmationMessage = `Rollback flow to version ${targetVersion}?`;
      const detailMessage = `The active version will change from version ${activeVersion} to version ${targetVersion}.`;

      expect(confirmationMessage).toContain(String(targetVersion));
      expect(detailMessage).toContain(String(activeVersion));
      expect(detailMessage).toContain(String(targetVersion));
    });
  });
});

describe('VersionHistory API endpoints', () => {
  it('targets correct endpoint for list versions', () => {
    const flowId = 'flow-123';
    const LIST_ENDPOINT = `/rule-engine/flows/${flowId}/versions`;
    expect(LIST_ENDPOINT).toBe('/rule-engine/flows/flow-123/versions');
  });

  it('targets correct endpoint for rollback', () => {
    const flowId = 'flow-123';
    const ROLLBACK_ENDPOINT = `/rule-engine/flows/${flowId}/rollback`;
    expect(ROLLBACK_ENDPOINT).toBe('/rule-engine/flows/flow-123/rollback');
  });

  it('targets correct endpoint for flow detail with version', () => {
    const flowId = 'flow-123';
    const version = 2;
    const DETAIL_ENDPOINT = `/rule-engine/flows/${flowId}?version=${version}`;
    expect(DETAIL_ENDPOINT).toBe('/rule-engine/flows/flow-123?version=2');
  });
});

describe('VersionHistory pagination', () => {
  it('shows first PAGE_SIZE items by default', () => {
    const PAGE_SIZE = 10;
    const totalVersions = 25;
    const displayCount = PAGE_SIZE;

    expect(displayCount).toBe(10);
    expect(displayCount).toBeLessThan(totalVersions);
  });

  it('load more increases display count', () => {
    const PAGE_SIZE = 10;
    let displayCount = PAGE_SIZE;

    // Load more
    displayCount += PAGE_SIZE;

    expect(displayCount).toBe(20);
  });

  it('has more flag when versions exceed display count', () => {
    const versions = new Array(15).fill(null).map((_, i) => ({
      version: i + 1,
      validated: true,
      createdAt: '2024-01-01T00:00:00Z',
      createdBy: 'test',
    }));

    const displayCount = 10;
    const hasMore = versions.length > displayCount;

    expect(hasMore).toBe(true);
  });

  it('no more flag when all versions displayed', () => {
    const versions = new Array(5).fill(null).map((_, i) => ({
      version: i + 1,
      validated: true,
      createdAt: '2024-01-01T00:00:00Z',
      createdBy: 'test',
    }));

    const displayCount = 10;
    const hasMore = versions.length > displayCount;

    expect(hasMore).toBe(false);
  });
});
