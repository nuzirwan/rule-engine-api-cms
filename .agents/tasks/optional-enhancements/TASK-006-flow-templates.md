# TASK-006: Flow Templates

## Summary
Add pre-built flow templates (CRUD, webhook handler, data sync, etc.) that operators can instantiate and customize from the CMS.

## Context
- Currently operators build flows from scratch
- Common patterns repeat across flows
- Templates accelerate development and ensure best practices

## Requirements

### 1. Template Definition
```json
{
  "id": "crud-rest-postgres",
  "name": "CRUD REST → Postgres",
  "description": "Standard CRUD operations with REST API and Postgres backend",
  "category": "data-access",
  "variables": [
    {"name": "resourceName", "type": "string", "description": "Resource name (e.g., 'user', 'order')"},
    {"name": "tableName", "type": "string", "description": "Postgres table name"},
    {"name": "connectionKey", "type": "string", "description": "Postgres connection key"},
    {"name": "primaryKey", "type": "string", "default": "id", "description": "Primary key column"}
  ],
  "flows": [
    {
      "flowId": "{{resourceName}}-list",
      "method": "GET",
      "path": "/{{resourceName}}s",
      "tree": { /* template tree with {{variables}} */ }
    },
    {
      "flowId": "{{resourceName}}-get",
      "method": "GET",
      "path": "/{{resourceName}}s/{id}",
      "tree": { /* ... */ }
    },
    {
      "flowId": "{{resourceName}}-create",
      "method": "POST",
      "path": "/{{resourceName}}s",
      "tree": { /* ... */ }
    },
    {
      "flowId": "{{resourceName}}-update",
      "method": "PUT",
      "path": "/{{resourceName}}s/{id}",
      "tree": { /* ... */ }
    },
    {
      "flowId": "{{resourceName}}-delete",
      "method": "DELETE",
      "path": "/{{resourceName}}s/{id}",
      "tree": { /* ... */ }
    }
  ]
}
```

### 2. Built-in Templates
- **CRUD REST → Postgres**: Standard CRUD with list/get/create/update/delete
- **CRUD REST → REST**: Proxy CRUD to another REST API
- **Webhook Handler**: Receive webhook, validate signature, process payload
- **Data Sync**: Read from source, transform, write to target
- **Approval Workflow**: Multi-step approval with status tracking
- **Notification Dispatcher**: Send notifications via multiple channels

### 3. Template Instantiation
- Select template from gallery
- Fill in variable values
- Preview generated flows
- Create all flows at once
- Option to customize before creating

### 4. Template Gallery UI
- Browse templates by category
- Search templates
- View template details and required variables
- "Use Template" button → variable form → preview → create

### 5. Custom Templates (stretch goal)
- Save existing flow as template
- Define variables from flow values
- Share templates across environments

## Files to Modify/Create

### Engine (`engine/`)
- `internal/config/template.go` — Template struct, variable substitution
- `internal/httpapi/template_admin.go` — template CRUD endpoints (stretch)
- `templates/` — built-in template JSON files

### CMS (`cms/src/plugins/rule-engine/admin/`)
- `src/pages/TemplatesPage.tsx` — template gallery
- `src/components/TemplateGallery/index.tsx` — template browser
- `src/components/TemplateForm/index.tsx` — variable input form
- `src/components/TemplatePreview/index.tsx` — preview generated flows

### CMS Server
- `server/src/services/template-service.ts` — template loading, variable substitution
- `server/src/controllers/template.ts` — template endpoints

## Acceptance Criteria
- [ ] At least 3 built-in templates available
- [ ] Template gallery shows all templates
- [ ] Can fill in variables and preview flows
- [ ] "Create" generates all flows from template
- [ ] Generated flows are valid and publishable
- [ ] Variables with defaults are pre-filled

## Testing
- Unit tests for variable substitution
- Unit tests for template validation
- Manual test: instantiate CRUD template, publish, verify endpoints work
