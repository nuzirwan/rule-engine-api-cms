# Load Node — loading JSON data mid-flow

The `load` node type loads JSON data from three possible sources: inline data,
local files, or URLs. The loaded data is stored in context (`Ctx.Data`) for use
by subsequent nodes like `filter`, `find`, `map`, and `reduce`.

**Related architecture:** [control_handlers.go](../../engine/internal/flow/control_handlers.go)

## Node Structure

```json
{
  "id": "load-data",
  "type": "load",
  "spec": {
    "data": [...],           // OR path OR url (exactly one required)
    "path": "/data/file.json",
    "url": "https://api.example.com/data.json",
    "jsonPath": "$.items",   // optional: extract a subset
    "saveAs": "ctx.items"    // required: context variable name
  }
}
```

| Field      | Type   | Required | Description                                        |
|------------|--------|----------|----------------------------------------------------|
| `data`     | any    | one of   | Inline JSON data (array, object, or primitive)     |
| `path`     | string | one of   | Local file path to read JSON from                  |
| `url`      | string | one of   | HTTP(S) URL to fetch JSON from                     |
| `jsonPath` | string | no       | JSONPath expression to extract a subset            |
| `saveAs`   | string | yes      | Context variable name to store the loaded data     |

**Note:** Exactly ONE of `data`, `path`, or `url` must be provided.

---

## Source Modes

### 1. Inline Data

Use inline data when the values are known at flow-definition time or are small
lookup tables:

```json
{
  "id": "load-rates",
  "type": "load",
  "spec": {
    "data": {
      "standard": 5.99,
      "express": 12.99,
      "overnight": 24.99
    },
    "saveAs": "shippingRates"
  }
}
```

Arrays work too:

```json
{
  "id": "load-tiers",
  "type": "load",
  "spec": {
    "data": ["bronze", "silver", "gold", "platinum"],
    "saveAs": "validTiers"
  }
}
```

### 2. Local File

Load JSON from a file on the local filesystem:

```json
{
  "id": "load-products",
  "type": "load",
  "spec": {
    "path": "/data/config/products.json",
    "saveAs": "products"
  }
}
```

### 3. URL

Fetch JSON from an HTTP(S) endpoint:

```json
{
  "id": "load-remote-config",
  "type": "load",
  "spec": {
    "url": "https://config.example.com/rules.json",
    "saveAs": "rules"
  }
}
```

The URL fetch uses a 30-second timeout and validates that the response is valid JSON.

---

## JSONPath Extraction

Use `jsonPath` to extract a subset of the loaded data. The syntax follows
JSONPath conventions (internally converted to GJSON syntax):

```json
{
  "id": "load-items",
  "type": "load",
  "spec": {
    "path": "/data/catalog.json",
    "jsonPath": "$.products",
    "saveAs": "products"
  }
}
```

If the catalog.json contains:

```json
{
  "version": "1.0",
  "products": [
    {"id": 1, "name": "Widget"},
    {"id": 2, "name": "Gadget"}
  ],
  "metadata": {...}
}
```

Then `ctx.products` will contain just the products array.

### Nested Extraction

Access nested values with dot notation:

```json
{
  "id": "load-first-product",
  "type": "load",
  "spec": {
    "data": {
      "items": [
        {"name": "first"},
        {"name": "second"}
      ]
    },
    "jsonPath": "$.items.0.name",
    "saveAs": "firstName"
  }
}
```

Result: `ctx.firstName` = `"first"`

---

## Examples with Collection Nodes

### Load + Filter

Load products and filter to those above a price threshold:

```json
[
  {
    "id": "load-products",
    "type": "load",
    "spec": {
      "data": [
        {"id": 1, "name": "Widget", "price": 50},
        {"id": 2, "name": "Gadget", "price": 150},
        {"id": 3, "name": "Gizmo", "price": 75}
      ],
      "saveAs": "products"
    }
  },
  {
    "id": "filter-expensive",
    "type": "filter",
    "spec": {
      "over": "products",
      "jdmId": "price-above-100",
      "input": ["price"],
      "saveAs": "expensive",
      "maxItems": 100
    }
  }
]
```

### Load + Find

Load user tiers and find a matching tier:

```json
[
  {
    "id": "load-tiers",
    "type": "load",
    "spec": {
      "path": "/data/tiers.json",
      "saveAs": "tiers"
    }
  },
  {
    "id": "find-tier",
    "type": "find",
    "spec": {
      "over": "tiers",
      "jdmId": "match-user-tier",
      "input": ["name", "minSpend"],
      "saveAs": "userTier",
      "maxItems": 10
    }
  }
]
```

### Load + Map

Load items and transform each with pricing rules:

```json
[
  {
    "id": "load-items",
    "type": "load",
    "spec": {
      "url": "https://inventory.example.com/items.json",
      "jsonPath": "$.items",
      "saveAs": "items"
    }
  },
  {
    "id": "apply-pricing",
    "type": "map",
    "spec": {
      "over": "items",
      "jdmId": "calculate-price",
      "input": ["basePrice", "category"],
      "saveAs": "pricedItems",
      "maxItems": 1000
    }
  }
]
```

### Load + Reduce

Load order items and calculate total:

```json
[
  {
    "id": "load-order",
    "type": "load",
    "spec": {
      "data": {
        "items": [
          {"qty": 2, "price": 10.00},
          {"qty": 1, "price": 25.00},
          {"qty": 3, "price": 5.00}
        ]
      },
      "jsonPath": "$.items",
      "saveAs": "orderItems"
    }
  },
  {
    "id": "calculate-total",
    "type": "reduce",
    "spec": {
      "over": "orderItems",
      "jdmId": "sum-line-total",
      "input": ["qty", "price"],
      "saveAs": "orderTotal",
      "maxItems": 100,
      "initialValue": {"total": 0}
    }
  }
]
```

---

## Complete Flow Example

A pricing flow that loads configuration, filters eligible products, and applies
discounts:

```json
{
  "id": "root",
  "type": "trigger",
  "spec": {
    "method": "POST",
    "path": "/api/pricing/calculate"
  },
  "children": [
    {
      "id": "load-config",
      "type": "load",
      "spec": {
        "path": "/data/pricing-config.json",
        "saveAs": "config"
      }
    },
    {
      "id": "load-products",
      "type": "load",
      "spec": {
        "url": "https://catalog.internal/products.json",
        "jsonPath": "$.products",
        "saveAs": "allProducts"
      }
    },
    {
      "id": "filter-active",
      "type": "filter",
      "spec": {
        "over": "allProducts",
        "jdmId": "is-product-active",
        "input": ["status", "inventory"],
        "saveAs": "activeProducts",
        "maxItems": 5000
      }
    },
    {
      "id": "apply-discounts",
      "type": "map",
      "spec": {
        "over": "activeProducts",
        "jdmId": "calculate-discount",
        "input": ["basePrice", "category", "tier"],
        "saveAs": "pricedProducts",
        "maxItems": 5000
      }
    },
    {
      "id": "respond",
      "type": "response",
      "spec": {
        "status": 200,
        "bodyFrom": "pricedProducts"
      }
    }
  ]
}
```

---

## Error Handling

The load node produces validation errors for:

- Missing `saveAs` field
- No source provided (none of `data`, `path`, or `url`)
- Multiple sources provided (more than one of `data`, `path`, or `url`)
- File not found (local path)
- Invalid JSON in file or URL response
- HTTP errors (4xx, 5xx) when fetching from URL
- Invalid URL scheme (only `http://` and `https://` are allowed)

For URL fetches, upstream errors (server errors, network issues) are classified
as `ClassUpstream` and can trigger retry/circuit-breaker behavior at the flow level.

---

## Best Practices

1. **Prefer inline data for small, static lookups** — Configuration that rarely
   changes and is small enough to embed in the flow definition.

2. **Use local files for larger reference data** — Product catalogs, pricing
   tables, or other data that changes infrequently.

3. **Use URLs for dynamic data** — Data that must be fresh or comes from an
   external service.

4. **Extract with JSONPath** — Only load what you need; extracting a subset with
   `jsonPath` is more efficient than loading the entire document and filtering
   in subsequent nodes.

5. **Combine with collection nodes** — The load node is designed to feed data
   into `filter`, `find`, `map`, and `reduce` nodes for querying and transformation.
