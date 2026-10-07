// EditorSkeleton — a lightweight loading placeholder for the heavy editor fields
// (FlowCanvasField, JdmEditorField). Used as Suspense fallback and initial load state.

import * as React from 'react';
import { Box, Flex, Typography, Loader } from '@strapi/design-system';

export interface EditorSkeletonProps {
  /** Height of the skeleton container. Defaults to 520px to match editor heights. */
  height?: number;
  /** Optional label to display above the skeleton. */
  label?: string;
}

/**
 * A skeleton/loading state for editor fields. Shows a subtle pulsing background
 * with a centered loader to indicate the editor is initializing.
 */
export const EditorSkeleton: React.FC<EditorSkeletonProps> = ({
  height = 520,
  label,
}) => {
  return (
    <Box
      background="neutral100"
      hasRadius
      borderColor="neutral200"
      style={{
        height,
        width: '100%',
        minWidth: 480,
        display: 'flex',
        flexDirection: 'column',
      }}
    >
      {label && (
        <Box padding={2} borderColor="neutral200" style={{ borderBottomWidth: 1 }}>
          <Typography variant="sigma" textColor="neutral600">
            {label}
          </Typography>
        </Box>
      )}
      <Flex
        justifyContent="center"
        alignItems="center"
        style={{ flex: 1, minHeight: 0 }}
      >
        <Flex direction="column" alignItems="center" gap={2}>
          <Loader small />
          <Typography variant="pi" textColor="neutral500">
            Loading editor…
          </Typography>
        </Flex>
      </Flex>
    </Box>
  );
};

export default EditorSkeleton;
