// TemplatesPage — Flow template gallery with a 4-step wizard:
//   1. gallery — browse and select a template
//   2. form — fill in template variables
//   3. preview — review generated flows before creation
//   4. done — success screen with links to created flows
//
// Follows SyncPage/EnvironmentsPage patterns:
//   * @strapi/design-system components for layout
//   * Fetch via useFetchClient
//   * Layouts.Root/Header/Content for page structure

import * as React from 'react';
import { useFetchClient, Link as RouterLink } from '@strapi/admin/strapi-admin';
import {
  Box,
  Button,
  Flex,
  Loader,
  Typography,
  Card,
  CardBody,
  CardContent,
  CardTitle,
  Grid,
  TextInput,
  Badge,
  Table,
  Thead,
  Tbody,
  Tr,
  Th,
  Td,
  IconButton,
  Tabs,
} from '@strapi/design-system';
import { Layouts } from '@strapi/admin/strapi-admin';
import { ArrowLeft, Check, Plus } from '@strapi/icons';

import { useEnvironment } from '../contexts/EnvironmentContext';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface TemplateVariable {
  name: string;
  type: string;
  description: string;
  default?: string;
  optional?: boolean;
}

interface TemplateFlowDef {
  flowId: string;
  method: string;
  path: string;
  tree: unknown;
}

interface TemplateDefinition {
  id: string;
  name: string;
  description: string;
  category: string;
  variables: TemplateVariable[];
  flows: TemplateFlowDef[];
}

interface InstantiatedFlow {
  flowId: string;
  method: string;
  path: string;
  tree: unknown;
}

interface CreatedFlowRef {
  flowId: string;
  documentId: string;
}

interface CreateFlowError {
  flowId: string;
  message: string;
}

// ---------------------------------------------------------------------------
// Step state machine
// ---------------------------------------------------------------------------

type Step =
  | { name: 'gallery' }
  | { name: 'form'; template: TemplateDefinition }
  | { name: 'preview'; template: TemplateDefinition; vars: Record<string, string>; flows: InstantiatedFlow[] }
  | { name: 'done'; created: CreatedFlowRef[]; errors?: CreateFlowError[] };

// ---------------------------------------------------------------------------
// TemplateGallery component
// ---------------------------------------------------------------------------

interface TemplateGalleryProps {
  templates: TemplateDefinition[];
  loading: boolean;
  onSelect: (template: TemplateDefinition) => void;
}

const TemplateGallery: React.FC<TemplateGalleryProps> = ({ templates, loading, onSelect }) => {
  const [categoryFilter, setCategoryFilter] = React.useState<string>('all');

  if (loading) {
    return (
      <Flex justifyContent="center" padding={8}>
        <Loader>Loading templates...</Loader>
      </Flex>
    );
  }

  // Get unique categories
  const categories = ['all', ...new Set(templates.map((t) => t.category))];
  const filtered = categoryFilter === 'all' 
    ? templates 
    : templates.filter((t) => t.category === categoryFilter);

  return (
    <Box>
      <Flex gap={2} marginBottom={4}>
        {categories.map((cat) => (
          <Button
            key={cat}
            variant={categoryFilter === cat ? 'default' : 'tertiary'}
            onClick={() => setCategoryFilter(cat)}
            size="S"
          >
            {cat === 'all' ? 'All' : cat.charAt(0).toUpperCase() + cat.slice(1)}
          </Button>
        ))}
      </Flex>
      <Grid.Root gap={4}>
        {filtered.map((template) => (
          <Grid.Item key={template.id} col={4} s={6} xs={12}>
            <Card
              style={{ cursor: 'pointer', height: '100%' }}
              onClick={() => onSelect(template)}
            >
              <CardBody>
                <CardContent>
                  <Flex justifyContent="space-between" alignItems="flex-start">
                    <CardTitle>{template.name}</CardTitle>
                    <Badge>{template.category}</Badge>
                  </Flex>
                  <Box marginTop={2}>
                    <Typography variant="pi" textColor="neutral600">
                      {template.description}
                    </Typography>
                  </Box>
                  <Box marginTop={3}>
                    <Typography variant="sigma" textColor="neutral500">
                      {template.variables.length} variable{template.variables.length !== 1 ? 's' : ''} · {template.flows.length} flow{template.flows.length !== 1 ? 's' : ''}
                    </Typography>
                  </Box>
                </CardContent>
              </CardBody>
            </Card>
          </Grid.Item>
        ))}
      </Grid.Root>
      {filtered.length === 0 && (
        <Box padding={8} textAlign="center">
          <Typography variant="omega" textColor="neutral600">
            No templates found in this category.
          </Typography>
        </Box>
      )}
    </Box>
  );
};

// ---------------------------------------------------------------------------
// TemplateForm component
// ---------------------------------------------------------------------------

interface TemplateFormProps {
  template: TemplateDefinition;
  onSubmit: (vars: Record<string, string>) => void;
  onBack: () => void;
  loading: boolean;
}

const TemplateForm: React.FC<TemplateFormProps> = ({ template, onSubmit, onBack, loading }) => {
  const [values, setValues] = React.useState<Record<string, string>>(() => {
    // Initialize with defaults
    const initial: Record<string, string> = {};
    for (const v of template.variables) {
      initial[v.name] = v.default ?? '';
    }
    return initial;
  });

  const handleChange = (name: string, value: string) => {
    setValues((prev) => ({ ...prev, [name]: value }));
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    onSubmit(values);
  };

  // Check if all required variables are filled
  const isValid = template.variables.every(
    (v) => v.optional || values[v.name]?.trim()
  );

  return (
    <form onSubmit={handleSubmit}>
      <Box marginBottom={4}>
        <Button variant="tertiary" startIcon={<ArrowLeft />} onClick={onBack}>
          Back to Templates
        </Button>
      </Box>

      <Box marginBottom={4}>
        <Typography variant="beta">{template.name}</Typography>
        <Typography variant="pi" textColor="neutral600">
          {template.description}
        </Typography>
      </Box>

      <Box marginBottom={4}>
        {template.variables.map((v) => (
          <Box key={v.name} marginBottom={3}>
            <TextInput
              label={v.name}
              name={v.name}
              hint={v.description}
              value={values[v.name] ?? ''}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => handleChange(v.name, e.target.value)}
              placeholder={v.default ? `Default: ${v.default}` : undefined}
              required={!v.optional}
            />
          </Box>
        ))}
      </Box>

      <Flex gap={2}>
        <Button type="submit" disabled={!isValid || loading} loading={loading}>
          Preview Flows
        </Button>
      </Flex>
    </form>
  );
};

// ---------------------------------------------------------------------------
// TemplatePreview component
// ---------------------------------------------------------------------------

interface TemplatePreviewProps {
  template: TemplateDefinition;
  flows: InstantiatedFlow[];
  vars: Record<string, string>;
  onConfirm: () => void;
  onBack: () => void;
  loading: boolean;
}

const TemplatePreview: React.FC<TemplatePreviewProps> = ({
  template,
  flows,
  vars,
  onConfirm,
  onBack,
  loading,
}) => {
  return (
    <Box>
      <Box marginBottom={4}>
        <Button variant="tertiary" startIcon={<ArrowLeft />} onClick={onBack}>
          Back to Variables
        </Button>
      </Box>

      <Box marginBottom={4}>
        <Typography variant="beta">Preview: {template.name}</Typography>
        <Typography variant="pi" textColor="neutral600">
          Review the {flows.length} flow{flows.length !== 1 ? 's' : ''} that will be created.
        </Typography>
      </Box>

      {/* Variable summary */}
      <Box marginBottom={4} padding={3} background="neutral100" hasRadius>
        <Typography variant="sigma" textColor="neutral600">Variables</Typography>
        <Box marginTop={2}>
          {Object.entries(vars).map(([key, value]) => (
            <Typography key={key} variant="pi">
              <strong>{key}:</strong> {value || <em>(empty)</em>}
            </Typography>
          ))}
        </Box>
      </Box>

      {/* Flows table */}
      <Table>
        <Thead>
          <Tr>
            <Th><Typography variant="sigma">Flow ID</Typography></Th>
            <Th><Typography variant="sigma">Method</Typography></Th>
            <Th><Typography variant="sigma">Path</Typography></Th>
          </Tr>
        </Thead>
        <Tbody>
          {flows.map((flow) => (
            <Tr key={flow.flowId}>
              <Td><Typography>{flow.flowId}</Typography></Td>
              <Td><Badge>{flow.method}</Badge></Td>
              <Td><Typography variant="pi">{flow.path}</Typography></Td>
            </Tr>
          ))}
        </Tbody>
      </Table>

      <Box marginTop={4}>
        <Flex gap={2}>
          <Button onClick={onConfirm} disabled={loading} loading={loading} startIcon={<Plus />}>
            Create {flows.length} Flow{flows.length !== 1 ? 's' : ''}
          </Button>
        </Flex>
      </Box>
    </Box>
  );
};

// ---------------------------------------------------------------------------
// TemplateDone component
// ---------------------------------------------------------------------------

interface TemplateDoneProps {
  created: CreatedFlowRef[];
  errors?: CreateFlowError[];
  onReset: () => void;
}

const TemplateDone: React.FC<TemplateDoneProps> = ({ created, errors, onReset }) => {
  return (
    <Box>
      <Box marginBottom={4}>
        <Flex alignItems="center" gap={2}>
          <Check fill="success600" />
          <Typography variant="beta">
            {created.length} Flow{created.length !== 1 ? 's' : ''} Created
          </Typography>
        </Flex>
      </Box>

      {errors && errors.length > 0 && (
        <Box marginBottom={4} padding={3} background="danger100" hasRadius>
          <Typography variant="sigma" textColor="danger600">
            {errors.length} flow{errors.length !== 1 ? 's' : ''} failed to create:
          </Typography>
          {errors.map((err) => (
            <Typography key={err.flowId} variant="pi" textColor="danger600">
              • {err.flowId}: {err.message}
            </Typography>
          ))}
        </Box>
      )}

      <Box marginBottom={4}>
        <Typography variant="omega" textColor="neutral600">
          The flows have been created as drafts. You can edit and publish them from the Content Manager.
        </Typography>
      </Box>

      <Table>
        <Thead>
          <Tr>
            <Th><Typography variant="sigma">Flow ID</Typography></Th>
            <Th><Typography variant="sigma">Actions</Typography></Th>
          </Tr>
        </Thead>
        <Tbody>
          {created.map((ref) => (
            <Tr key={ref.documentId}>
              <Td><Typography>{ref.flowId}</Typography></Td>
              <Td>
                <RouterLink to={`/content-manager/collection-types/api::flow.flow/${ref.documentId}`}>
                  Edit in Content Manager
                </RouterLink>
              </Td>
            </Tr>
          ))}
        </Tbody>
      </Table>

      <Box marginTop={4}>
        <Button onClick={onReset} variant="tertiary">
          Create More Flows
        </Button>
      </Box>
    </Box>
  );
};

// ---------------------------------------------------------------------------
// TemplatesPage main component
// ---------------------------------------------------------------------------

export interface TemplatesPageProps {}

export const TemplatesPage: React.FC<TemplatesPageProps> = () => {
  const { get, post } = useFetchClient();
  const { selectedEnv } = useEnvironment();

  const [step, setStep] = React.useState<Step>({ name: 'gallery' });
  const [templates, setTemplates] = React.useState<TemplateDefinition[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [actionLoading, setActionLoading] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  // Fetch templates on mount
  React.useEffect(() => {
    const fetchTemplates = async () => {
      try {
        setLoading(true);
        const res = await get('/rule-engine/templates');
        setTemplates(res.data.templates);
        setError(null);
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to load templates');
      } finally {
        setLoading(false);
      }
    };
    fetchTemplates();
  }, [get]);

  // Handle template selection
  const handleSelectTemplate = (template: TemplateDefinition) => {
    setStep({ name: 'form', template });
  };

  // Handle form submission (preview)
  const handleFormSubmit = async (vars: Record<string, string>) => {
    if (step.name !== 'form') return;
    setActionLoading(true);
    try {
      const res = await post(`/rule-engine/templates/${step.template.id}/preview`, {
        variables: vars,
      });
      setStep({
        name: 'preview',
        template: step.template,
        vars,
        flows: res.data.flows,
      });
      setError(null);
    } catch (err: any) {
      setError(err?.response?.data?.error || err?.message || 'Preview failed');
    } finally {
      setActionLoading(false);
    }
  };

  // Handle flow creation
  const handleConfirmCreate = async () => {
    if (step.name !== 'preview') return;
    setActionLoading(true);
    try {
      const body: Record<string, unknown> = { variables: step.vars };
      if (selectedEnv?.documentId) {
        body.environmentDocumentId = selectedEnv.documentId;
      }
      const res = await post(`/rule-engine/templates/${step.template.id}/instantiate`, body);
      setStep({
        name: 'done',
        created: res.data.created,
        errors: res.data.errors,
      });
      setError(null);
    } catch (err: any) {
      setError(err?.response?.data?.error || err?.message || 'Creation failed');
    } finally {
      setActionLoading(false);
    }
  };

  // Reset to gallery
  const handleReset = () => {
    setStep({ name: 'gallery' });
    setError(null);
  };

  // Determine subtitle based on step
  const getSubtitle = () => {
    switch (step.name) {
      case 'gallery':
        return 'Browse and create flows from pre-built templates';
      case 'form':
        return `Configure: ${step.template.name}`;
      case 'preview':
        return `Review ${step.flows.length} flow${step.flows.length !== 1 ? 's' : ''}`;
      case 'done':
        return 'Flows created successfully';
    }
  };

  return (
    <Layouts.Root>
      <Layouts.Header
        title="Flow Templates"
        subtitle={getSubtitle()}
        navigationAction={
          step.name !== 'gallery' && step.name !== 'done' ? (
            <Button variant="ghost" startIcon={<ArrowLeft />} onClick={handleReset}>
              Templates
            </Button>
          ) : undefined
        }
      />
      <Layouts.Content>
        <Box padding={4}>
          {error && (
            <Box marginBottom={4} padding={3} background="danger100" hasRadius>
              <Typography textColor="danger600">{error}</Typography>
            </Box>
          )}

          {step.name === 'gallery' && (
            <TemplateGallery
              templates={templates}
              loading={loading}
              onSelect={handleSelectTemplate}
            />
          )}

          {step.name === 'form' && (
            <TemplateForm
              template={step.template}
              onSubmit={handleFormSubmit}
              onBack={handleReset}
              loading={actionLoading}
            />
          )}

          {step.name === 'preview' && (
            <TemplatePreview
              template={step.template}
              flows={step.flows}
              vars={step.vars}
              onConfirm={handleConfirmCreate}
              onBack={() => setStep({ name: 'form', template: step.template })}
              loading={actionLoading}
            />
          )}

          {step.name === 'done' && (
            <TemplateDone
              created={step.created}
              errors={step.errors}
              onReset={handleReset}
            />
          )}
        </Box>
      </Layouts.Content>
    </Layouts.Root>
  );
};

export default TemplatesPage;
