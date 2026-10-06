// EnvironmentManager — full CRUD panel for managing environments.
// Lists all environments in a table with add/edit/delete actions.
// Includes Test Connection button that verifies engine reachability.
//
// Follows the SyncPanel pattern:
//   * @strapi/design-system components for layout
//   * Fetch-on-mount via useFetchClient
//   * Loading / error / success states with FetchState union

import * as React from 'react';
import { useFetchClient } from '@strapi/admin/strapi-admin';
import {
  Box,
  Button,
  Flex,
  Loader,
  Typography,
  Table,
  Thead,
  Tbody,
  Tr,
  Th,
  Td,
  IconButton,
  Dialog,
  Field,
  TextInput,
  Badge,
} from '@strapi/design-system';
import { Pencil, Trash, Check, Cross, Plus, Play } from '@strapi/icons';

import { useEnvironment, type Environment } from '../../contexts/EnvironmentContext';
import {
  type EnvironmentFormData,
  type EnvironmentFormErrors,
  type TestConnectionResult,
  environmentToFormData,
  emptyFormData,
  validateFormData,
} from './types';

export type { EnvironmentFormData, EnvironmentFormErrors, TestConnectionResult };

// ---------------------------------------------------------------------------
// State types
// ---------------------------------------------------------------------------

type ModalMode = 'closed' | 'create' | 'edit';

interface ModalState {
  mode: ModalMode;
  editingId?: string;
  formData: EnvironmentFormData;
  errors: EnvironmentFormErrors;
  isSubmitting: boolean;
}

interface TestState {
  /** documentId currently being tested */
  testingId: string | null;
  /** Results keyed by documentId */
  results: Record<string, TestConnectionResult>;
}

interface DeleteState {
  /** documentId pending deletion confirmation */
  pendingId: string | null;
  /** documentId currently being deleted */
  deletingId: string | null;
}

// ---------------------------------------------------------------------------
// Initial states
// ---------------------------------------------------------------------------

const initialModalState: ModalState = {
  mode: 'closed',
  formData: emptyFormData(),
  errors: {},
  isSubmitting: false,
};

const initialTestState: TestState = {
  testingId: null,
  results: {},
};

const initialDeleteState: DeleteState = {
  pendingId: null,
  deletingId: null,
};

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

interface EnvironmentRowProps {
  env: Environment;
  onEdit: (env: Environment) => void;
  onDelete: (env: Environment) => void;
  onTest: (env: Environment) => void;
  isTesting: boolean;
  isDeleting: boolean;
  testResult?: TestConnectionResult;
}

function EnvironmentRow({
  env,
  onEdit,
  onDelete,
  onTest,
  isTesting,
  isDeleting,
  testResult,
}: EnvironmentRowProps): React.JSX.Element {
  return (
    <Tr>
      <Td>
        <Typography variant="omega" fontWeight="bold">
          {env.name}
        </Typography>
      </Td>
      <Td>
        <Typography variant="omega" textColor={env.adminApiBaseUrl ? 'neutral800' : 'neutral500'}>
          {env.adminApiBaseUrl || '(default)'}
        </Typography>
      </Td>
      <Td>
        <Typography variant="omega" textColor={env.operatorTokenRef ? 'neutral800' : 'neutral500'}>
          {env.operatorTokenRef || '(default)'}
        </Typography>
      </Td>
      <Td>
        <Typography variant="omega">{env.payloadEnv || '(empty)'}</Typography>
      </Td>
      <Td>
        {testResult && (
          <Badge variant={testResult.success ? 'success' : 'danger'}>
            {testResult.success ? 'Connected' : 'Failed'}
          </Badge>
        )}
      </Td>
      <Td>
        <Flex direction="row" gap={1}>
          <IconButton
            onClick={() => onTest(env)}
            label="Test Connection"
            disabled={isTesting || isDeleting}
          >
            {isTesting ? <Loader small /> : <Play />}
          </IconButton>
          <IconButton
            onClick={() => onEdit(env)}
            label="Edit"
            disabled={isTesting || isDeleting}
          >
            <Pencil />
          </IconButton>
          <IconButton
            onClick={() => onDelete(env)}
            label="Delete"
            disabled={isTesting || isDeleting}
          >
            {isDeleting ? <Loader small /> : <Trash />}
          </IconButton>
        </Flex>
      </Td>
    </Tr>
  );
}

interface EnvironmentFormProps {
  formData: EnvironmentFormData;
  errors: EnvironmentFormErrors;
  onChange: (field: keyof EnvironmentFormData, value: string) => void;
  isEditing: boolean;
}

function EnvironmentForm({
  formData,
  errors,
  onChange,
  isEditing,
}: EnvironmentFormProps): React.JSX.Element {
  return (
    <Flex direction="column" gap={4}>
      <Field.Root error={errors.name} required>
        <Field.Label>Name</Field.Label>
        <TextInput
          name="name"
          value={formData.name}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) => onChange('name', e.target.value)}
          placeholder="production, staging, development..."
          disabled={isEditing}
        />
        {errors.name && <Field.Error>{errors.name}</Field.Error>}
        <Field.Hint>
          Unique identifier for this environment. Cannot be changed after creation.
        </Field.Hint>
      </Field.Root>

      <Field.Root error={errors.adminApiBaseUrl}>
        <Field.Label>Admin API Base URL</Field.Label>
        <TextInput
          name="adminApiBaseUrl"
          value={formData.adminApiBaseUrl}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
            onChange('adminApiBaseUrl', e.target.value)
          }
          placeholder="https://api.example.com (leave empty to use default)"
        />
        {errors.adminApiBaseUrl && <Field.Error>{errors.adminApiBaseUrl}</Field.Error>}
        <Field.Hint>Override the engine admin API URL for this environment.</Field.Hint>
      </Field.Root>

      <Field.Root error={errors.operatorTokenRef}>
        <Field.Label>Operator Token Ref</Field.Label>
        <TextInput
          name="operatorTokenRef"
          value={formData.operatorTokenRef}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
            onChange('operatorTokenRef', e.target.value)
          }
          placeholder="MY_TOKEN_ENV_VAR (leave empty to use default)"
        />
        {errors.operatorTokenRef && <Field.Error>{errors.operatorTokenRef}</Field.Error>}
        <Field.Hint>
          Name of the environment variable containing the bearer token for this environment.
        </Field.Hint>
      </Field.Root>

      <Field.Root error={errors.payloadEnv}>
        <Field.Label>Payload Env</Field.Label>
        <TextInput
          name="payloadEnv"
          value={formData.payloadEnv}
          onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
            onChange('payloadEnv', e.target.value)
          }
          placeholder="prod, staging, etc."
        />
        {errors.payloadEnv && <Field.Error>{errors.payloadEnv}</Field.Error>}
        <Field.Hint>Value sent in the admin body's `env` field when publishing.</Field.Hint>
      </Field.Root>
    </Flex>
  );
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export interface EnvironmentManagerProps {
  /** Called after any CRUD operation completes (for parent to refresh). */
  onUpdate?: () => void;
}

export const EnvironmentManager: React.FC<EnvironmentManagerProps> = ({ onUpdate }) => {
  const { environments, isLoading, error, refresh } = useEnvironment();
  const { post, put, del } = useFetchClient();

  const [modal, setModal] = React.useState<ModalState>(initialModalState);
  const [testState, setTestState] = React.useState<TestState>(initialTestState);
  const [deleteState, setDeleteState] = React.useState<DeleteState>(initialDeleteState);
  const [apiError, setApiError] = React.useState<string | null>(null);

  // --- Modal handlers ---

  const openCreateModal = () => {
    setModal({
      mode: 'create',
      formData: emptyFormData(),
      errors: {},
      isSubmitting: false,
    });
    setApiError(null);
  };

  const openEditModal = (env: Environment) => {
    setModal({
      mode: 'edit',
      editingId: env.documentId,
      formData: environmentToFormData(env),
      errors: {},
      isSubmitting: false,
    });
    setApiError(null);
  };

  const closeModal = () => {
    setModal(initialModalState);
    setApiError(null);
  };

  const handleFormChange = (field: keyof EnvironmentFormData, value: string) => {
    setModal((prev) => ({
      ...prev,
      formData: { ...prev.formData, [field]: value },
      errors: { ...prev.errors, [field]: undefined },
    }));
  };

  const handleSubmit = async () => {
    const existingNames = environments.map((e) => e.name.toLowerCase());
    const editingName = modal.mode === 'edit' 
      ? environments.find((e) => e.documentId === modal.editingId)?.name 
      : undefined;
    const errors = validateFormData(modal.formData, existingNames, editingName);

    if (Object.keys(errors).length > 0) {
      setModal((prev) => ({ ...prev, errors }));
      return;
    }

    setModal((prev) => ({ ...prev, isSubmitting: true }));
    setApiError(null);

    try {
      if (modal.mode === 'create') {
        await post('/rule-engine/environments', modal.formData);
      } else if (modal.mode === 'edit' && modal.editingId) {
        await put(`/rule-engine/environments/${modal.editingId}`, modal.formData);
      }

      closeModal();
      refresh();
      onUpdate?.();
    } catch (err: unknown) {
      const body = (err as any)?.response?.data;
      const message = body?.error ?? (err instanceof Error ? err.message : 'Operation failed');
      setApiError(message);
      setModal((prev) => ({ ...prev, isSubmitting: false }));
    }
  };

  // --- Delete handlers ---

  const openDeleteConfirm = (env: Environment) => {
    setDeleteState({ pendingId: env.documentId, deletingId: null });
  };

  const closeDeleteConfirm = () => {
    setDeleteState(initialDeleteState);
  };

  const handleDelete = async () => {
    if (!deleteState.pendingId) return;

    setDeleteState((prev) => ({ ...prev, deletingId: prev.pendingId }));

    try {
      await del(`/rule-engine/environments/${deleteState.pendingId}`);
      closeDeleteConfirm();
      refresh();
      onUpdate?.();
    } catch (err: unknown) {
      const body = (err as any)?.response?.data;
      const message = body?.error ?? (err instanceof Error ? err.message : 'Delete failed');
      setApiError(message);
      setDeleteState(initialDeleteState);
    }
  };

  // --- Test connection handler ---

  const handleTestConnection = async (env: Environment) => {
    setTestState((prev) => ({ ...prev, testingId: env.documentId }));

    try {
      const { data } = await post<{ success: boolean; message: string; responseTimeMs?: number }>(
        `/rule-engine/environments/${env.documentId}/test`,
        {}
      );
      setTestState((prev) => ({
        testingId: null,
        results: {
          ...prev.results,
          [env.documentId]: {
            success: data.success,
            message: data.message,
            responseTimeMs: data.responseTimeMs,
          },
        },
      }));
    } catch (err: unknown) {
      const body = (err as any)?.response?.data;
      const message = body?.error ?? (err instanceof Error ? err.message : 'Test failed');
      setTestState((prev) => ({
        testingId: null,
        results: {
          ...prev.results,
          [env.documentId]: { success: false, message },
        },
      }));
    }
  };

  // --- Render ---

  const pendingEnv = environments.find((e) => e.documentId === deleteState.pendingId);

  return (
    <Box padding={4} background="neutral100" hasRadius>
      <Flex direction="column" alignItems="stretch" gap={4}>
        {/* Header */}
        <Flex direction="row" justifyContent="space-between" alignItems="center">
          <Typography variant="delta">Environments</Typography>
          <Flex direction="row" gap={2}>
            <Button variant="tertiary" onClick={refresh} disabled={isLoading}>
              Refresh
            </Button>
            <Button variant="default" startIcon={<Plus />} onClick={openCreateModal}>
              Add Environment
            </Button>
          </Flex>
        </Flex>

        {/* API Error */}
        {apiError && (
          <Box padding={3} background="danger100" hasRadius>
            <Typography variant="omega" textColor="danger600">
              {apiError}
            </Typography>
          </Box>
        )}

        {/* Loading */}
        {isLoading && (
          <Flex justifyContent="center" padding={4}>
            <Loader small />
          </Flex>
        )}

        {/* Error */}
        {error && !isLoading && (
          <Box padding={3} background="danger100" hasRadius>
            <Typography variant="omega" textColor="danger600">
              {error}
            </Typography>
          </Box>
        )}

        {/* Table */}
        {!isLoading && !error && environments.length > 0 && (
          <Table>
            <Thead>
              <Tr>
                <Th>
                  <Typography variant="sigma">Name</Typography>
                </Th>
                <Th>
                  <Typography variant="sigma">Admin URL</Typography>
                </Th>
                <Th>
                  <Typography variant="sigma">Token Ref</Typography>
                </Th>
                <Th>
                  <Typography variant="sigma">Payload Env</Typography>
                </Th>
                <Th>
                  <Typography variant="sigma">Status</Typography>
                </Th>
                <Th>
                  <Typography variant="sigma">Actions</Typography>
                </Th>
              </Tr>
            </Thead>
            <Tbody>
              {environments.map((env) => (
                <EnvironmentRow
                  key={env.documentId}
                  env={env}
                  onEdit={openEditModal}
                  onDelete={openDeleteConfirm}
                  onTest={handleTestConnection}
                  isTesting={testState.testingId === env.documentId}
                  isDeleting={deleteState.deletingId === env.documentId}
                  testResult={testState.results[env.documentId]}
                />
              ))}
            </Tbody>
          </Table>
        )}

        {/* Empty state */}
        {!isLoading && !error && environments.length === 0 && (
          <Box padding={4} background="neutral100" hasRadius>
            <Flex direction="column" alignItems="center" gap={2}>
              <Typography variant="omega" textColor="neutral600">
                No environments configured yet.
              </Typography>
              <Typography variant="pi" textColor="neutral500">
                Add an environment to start managing multiple engine deployments.
              </Typography>
            </Flex>
          </Box>
        )}

        {/* Create/Edit Modal */}
        <Dialog.Root open={modal.mode !== 'closed'} onOpenChange={(open) => !open && closeModal()}>
          <Dialog.Content>
            <Dialog.Header>
              {modal.mode === 'create' ? 'Add Environment' : 'Edit Environment'}
            </Dialog.Header>
            <Dialog.Body>
              <EnvironmentForm
                formData={modal.formData}
                errors={modal.errors}
                onChange={handleFormChange}
                isEditing={modal.mode === 'edit'}
              />
            </Dialog.Body>
            <Dialog.Footer>
              <Dialog.Cancel>
                <Button variant="tertiary" onClick={closeModal}>
                  Cancel
                </Button>
              </Dialog.Cancel>
              <Dialog.Action>
                <Button
                  variant="default"
                  onClick={handleSubmit}
                  disabled={modal.isSubmitting}
                  startIcon={modal.isSubmitting ? <Loader small /> : <Check />}
                >
                  {modal.mode === 'create' ? 'Create' : 'Save'}
                </Button>
              </Dialog.Action>
            </Dialog.Footer>
          </Dialog.Content>
        </Dialog.Root>

        {/* Delete Confirmation Modal */}
        <Dialog.Root
          open={deleteState.pendingId !== null}
          onOpenChange={(open) => !open && closeDeleteConfirm()}
        >
          <Dialog.Content>
            <Dialog.Header>Delete Environment</Dialog.Header>
            <Dialog.Body>
              <Typography variant="omega">
                Are you sure you want to delete the environment "{pendingEnv?.name}"?
              </Typography>
              <Typography variant="pi" textColor="danger600">
                This action cannot be undone. Any flows, JDMs, or connections associated with this
                environment will lose their environment reference.
              </Typography>
            </Dialog.Body>
            <Dialog.Footer>
              <Dialog.Cancel>
                <Button variant="tertiary" onClick={closeDeleteConfirm}>
                  Cancel
                </Button>
              </Dialog.Cancel>
              <Dialog.Action>
                <Button
                  variant="danger"
                  onClick={handleDelete}
                  disabled={deleteState.deletingId !== null}
                  startIcon={deleteState.deletingId ? <Loader small /> : <Cross />}
                >
                  Delete
                </Button>
              </Dialog.Action>
            </Dialog.Footer>
          </Dialog.Content>
        </Dialog.Root>
      </Flex>
    </Box>
  );
};

export default EnvironmentManager;
