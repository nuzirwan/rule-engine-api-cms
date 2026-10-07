import { mergeConfig, type UserConfig } from 'vite';

export default (config: UserConfig) => {
  return mergeConfig(config, {
    resolve: {
      alias: {
        '@': '/src',
      },
    },
    // Optimize heavy dependencies for the Flow/JDM editors
    optimizeDeps: {
      include: [
        '@xyflow/react',
        '@xyflow/system',
        '@gorules/jdm-editor',
        'react',
        'react-dom',
        'styled-components',
      ],
    },
    build: {
      // Increase chunk size warning limit for heavy editor bundles
      chunkSizeWarningLimit: 1500,
      rollupOptions: {
        output: {
          // Split heavy editor deps into separate chunks for better caching
          manualChunks: {
            'xyflow': ['@xyflow/react', '@xyflow/system'],
            'jdm-editor': ['@gorules/jdm-editor'],
          },
        },
      },
    },
  });
};
