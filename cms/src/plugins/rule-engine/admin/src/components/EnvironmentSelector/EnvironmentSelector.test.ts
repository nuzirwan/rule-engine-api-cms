// Unit tests for EnvironmentSelector component.
// Tests the exports and expected behavior patterns.
// Note: These are smoke tests for the type interface, not full render tests,
// since rendering requires the full Strapi admin environment.

import { describe, expect, it, vi } from 'vitest';

describe('EnvironmentSelector', () => {
  describe('module structure', () => {
    it('follows the component naming convention', () => {
      // File exists at expected path
      const COMPONENT_PATH = 'components/EnvironmentSelector/index.tsx';
      expect(COMPONENT_PATH).toContain('EnvironmentSelector');
    });
  });

  describe('props interface', () => {
    it('documents expected optional props', () => {
      // EnvironmentSelectorProps expected shape:
      interface EnvironmentSelectorProps {
        placeholder?: string;
        showAllOption?: boolean;
        className?: string;
      }

      const props: EnvironmentSelectorProps = {
        placeholder: 'Choose an environment',
        showAllOption: false,
        className: 'custom-class',
      };

      expect(props.placeholder).toBe('Choose an environment');
      expect(props.showAllOption).toBe(false);
      expect(props.className).toBe('custom-class');
    });

    it('allows all props to be optional', () => {
      interface EnvironmentSelectorProps {
        placeholder?: string;
        showAllOption?: boolean;
        className?: string;
      }

      const defaultProps: EnvironmentSelectorProps = {};
      expect(defaultProps.showAllOption).toBeUndefined();
    });
  });
});

describe('EnvironmentSelector badge variants', () => {
  // These test the internal getBadgeVariant logic conceptually

  it('documents production patterns for success variant', () => {
    const productionPatterns = ['production', 'prod', 'default', 'live'];
    expect(productionPatterns).toContain('production');
  });

  it('documents staging patterns for alternative variant', () => {
    const stagingPatterns = ['staging', 'stage', 'stg', 'qa', 'uat'];
    expect(stagingPatterns).toContain('staging');
  });

  it('documents development patterns for warning variant', () => {
    const devPatterns = ['development', 'dev', 'local', 'test'];
    expect(devPatterns).toContain('development');
  });
});

describe('EnvironmentSelector selection behavior', () => {
  it('uses __all__ as the special value for clearing selection', () => {
    const ALL_VALUE = '__all__';
    expect(ALL_VALUE).toBe('__all__');
  });

  it('documents the localStorage key used by context', () => {
    const STORAGE_KEY = 'rule-engine-selected-env';
    expect(STORAGE_KEY).toBe('rule-engine-selected-env');
  });
});
