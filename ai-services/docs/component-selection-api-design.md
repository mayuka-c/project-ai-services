# Component Selection API Design

**Version:** 3.0
**Date:** May 4, 2026
**Status:** Design Proposal - Simplified & Validated

## Table of Contents

1. [Overview](#overview)
2. [Component Dependencies](#component-dependencies)
3. [API Endpoints](#api-endpoints)
4. [API Flow](#api-flow)
5. [Request/Response Examples](#requestresponse-examples)
6. [Database Schema](#database-schema)

---

## Overview

This document describes the dynamic API design for component selection when deploying AI services. The UI presents dropdown selections with intelligent dependencies - some components depend on parent selections.

### Terminology

- **Instance**: A deployed, running component that already exists in the system (e.g., "opensearch-instance-1", "vllm-instance-1")
- **Provider**: A blueprint/specification for creating a new component of a specific type and technology (e.g., "opensearch provider", "vllm instruct provider")
  - Providers define what **can be created**
  - They specify the provider technology, service type, and supported models
  - When a user selects a provider, they fill in configuration to create a new instance
- **Provider**: The underlying technology/service (e.g., "vllm", "watsonx", "opensearch", "milvus")
- **Service**: The specific AI service being deployed (e.g., "instruct-vllm", "embedding-watsonx")

### Key Principles

- **Separate ID Types**: Clear distinction between instance IDs, template IDs, and model IDs
- **No Fake Options**: "Create New" is handled separately, not as a dropdown option
- **Existing Instances Only**: Dropdowns show only real, existing instances
- **Provider Templates**: Available templates shown separately for new component creation
- **Component Dependencies**: Some components may have dependencies on other selections
- **Progressive Disclosure**: Dependent dropdowns only appear after parent is selected
- **Backend Validation**: Server enforces dependency rules and validates selections
- **Separation of Concerns**: Configuration (user settings) vs Wiring (backend_id references)

---

## Component Dependencies

### Dependency Hierarchy

```
vector_db (independent)
    ↓
llm (independent)
    ↓
embedding (independent)
    ↓
reranker (independent)
```

### Rules

1. **vector_db**: Independent, can be selected anytime
2. **llm**: Independent, can be selected anytime
   - Instances include `accelerator` metadata (e.g., "spyre", "cpu")
   - Instances include `capabilities` array (e.g., ["fp16", "int8"])
3. **embedding**: Independent, can be selected anytime
   - Instances include `accelerator` and `capabilities` metadata
4. **reranker**: Independent, optional component
   - Instances include `accelerator` and `capabilities` metadata

### Accelerator-Based Instance Filtering

Component instances (llm, embedding, reranker) include accelerator and capability metadata:

1. **Accelerator**: Hardware type the instance runs on (e.g., "spyre", "cpu")
2. **Capabilities**: Supported features (e.g., ["fp16", "int8"])

**Example:**
- LLM instance: "Granite 3.3 8B on Spyre" has accelerator="spyre", capabilities=["fp16", "int8"]
- Embedding instance: "BGE Base on CPU" has accelerator="cpu", capabilities=["fp16", "fp32"]

Users can filter instances based on their accelerator requirements.

---

## API Endpoints

### Architecture Level

#### 1. GET `/api/v1/architectures/{architecture_id}/options`

Get available providers and dependency rules for all services and their components.

**Purpose**: Fetch providers (blueprints) for creating new components and dependency rules. Does NOT include running instances.

**Response Structure:**
- `providers`: Array of available providers (for "Create New" flow)
- `dependencies`: Dependency rules and validation requirements
- `component_metadata`: Labels, descriptions, and requirements

#### 2. GET `/api/v1/architectures/{architecture_id}/params`

Get configuration schemas (JSON Schema) for all component types used in the architecture.

**Purpose**: Fetch configuration schemas for all component types (vector_db, llm, embedding, reranker) that are dependencies of services in this architecture. Returns schemas for all available providers within each component type.

**Response Structure:**
- Top-level keys are component types (vector_db, llm, embedding, reranker)
- Each component type contains a map of provider_id to JSON Schema
- Each provider's schema has the component name as a nested property
- Only includes component types that are used by services in the architecture
- Does NOT include service-level schemas (use service-specific endpoint for those)

### Service Level

#### 3. GET `/api/v1/services/{service_id}/options`

Get available providers and dependency rules for a specific service.

**Purpose**: Same as architecture-level options but scoped to a single service. Returns providers and dependency rules only.

#### 4. GET `/api/v1/components/{component_type}/instances`

Get all running instances for a specific component type.

**Purpose**: Fetch existing, deployed instances for a specific component type to populate dropdowns. Returns all instances of that component type across the system.

**Parameters:**
- `component_type`: The type of component (e.g., "vector_db", "llm", "embedding", "reranker")

**Response Structure:**
- Array of instances for the requested component type
- Each instance includes: `instance_id`, `label`, `provider`

#### 5. GET `/api/v1/services/{service_id}/params`

Get configuration form fields for a specific service.

**Purpose**: Fetch the configuration schema for a specific service type.

#### 6. GET `/api/v1/components/{component_type}/providers/{provider_id}/params`

Get configuration form fields for a specific provider within a component type.

**Purpose**: Fetch the configuration schema for a specific provider. This is used when creating a new component instance with a selected provider.

**Parameters:**
- `component_type`: The type of component (e.g., "vector_db", "llm", "embedding", "reranker")
- `provider_id`: The provider identifier (e.g., "opensearch", "vllm", "watsonx", "milvus")

**Response Structure:**
- JSON Schema defining the configuration fields for that specific provider
- Includes all configurable parameters like model, backend_id, storage, etc.

### Application Deployment

#### 7. POST `/api/v1/applications`

Deploy application with component selections and configurations.

**Validation**: Backend validates:
- All required components are selected
- Dependencies are satisfied (e.g., instruct has valid backend_id)
- Selected instances exist and are compatible
- Configuration is valid per service schema

---

## API Flow

### Architecture Flow

```
┌─────────────────────────────────────────────────────────────┐
│ 1. GET /api/v1/architectures/rag/options                    │
│    → Fetch providers and dependency rules                   │
│    → Returns providers for creating new components          │
│    → Returns dependency rules and metadata                  │
└─────────────────────────────────────────────────────────────┘
                          ↓
┌─────────────────────────────────────────────────────────────┐
│ 2. For each component: GET /api/v1/components/{type}/instances│
│    → Fetch running instances per component type             │
│    → GET /api/v1/components/vector_db/instances             │
│    → GET /api/v1/components/llm/instances                   │
│    → GET /api/v1/components/embedding/instances             │
│    → Returns array of instances for that component type     │
└─────────────────────────────────────────────────────────────┘
                          ↓
┌─────────────────────────────────────────────────────────────┐
│ 3. User selects or creates components                       │
│    → Select existing instances from dropdowns               │
│    → Or click "Create New" to configure new components      │
└─────────────────────────────────────────────────────────────┘
                          ↓
┌─────────────────────────────────────────────────────────────┐
│ 4. For "Create New": GET /api/v1/services/{service_id}/params│
│    → Get JSON Schema for that specific service              │
│    → User fills configuration form                          │
│    → User selects model and accelerator preferences         │
│    → Repeat for each new component                          │
└─────────────────────────────────────────────────────────────┘
                          ↓
┌─────────────────────────────────────────────────────────────┐
│ 5. User completes all selections and configurations         │
│    → All new components have complete configurations        │
│    → All existing instances are selected by instance_id     │
└─────────────────────────────────────────────────────────────┘
                          ↓
┌─────────────────────────────────────────────────────────────┐
│ 6. POST /api/v1/applications                                │
│    → Backend validates all selections                       │
│    → Deploy with all selections + configurations            │
└─────────────────────────────────────────────────────────────┘
```

---

## Request/Response Examples

### 1. GET `/api/v1/architectures/{id}/options`

Get providers and dependency rules (no instances).

**Response:**

```json
{
  "architecture_id": "rag",
  "architecture_name": "Digital Assistant",
  "services": [
    {
      "type": "service",
      "service_id": "digitize",
      "service_name": "Digitize documents",
      "components": [
        {
          "type": "vector_db",
          "label": "Vector store",
          "required": true,
          "providers": [
            {
              "id": "opensearch",
              "label": "OpenSearch",
              "description": "Distributed search and analytics engine"
            },
            {
              "id": "milvus",
              "label": "Milvus",
              "description": "Cloud-native vector database"
            }
          ]
        },
        {
          "type": "llm",
          "label": "LLM Model",
          "required": true,
          "providers": [
            {
              "id": "vllm",
              "label": "vLLM Instruct",
              "description": "Deploy new instruct model on vLLM",
              "supported_models": [
                "ibm-granite/granite-3.3-8b-instruct",
                "meta-llama/Llama-3.1-8B-Instruct"
              ]
            },
            {
              "id": "watsonx",
              "label": "IBM watsonx.ai Instruct",
              "description": "Configure watsonx.ai for instruct models",
              "supported_models": [
                "ibm/granite-13b-chat-v2",
                "meta-llama/llama-3-70b-instruct"
              ]
            }
          ]
        },
        {
          "type": "embedding",
          "label": "Embedding Model",
          "required": true,
          "providers": [
            {
              "id": "vllm",
              "label": "vLLM Embeddings",
              "supported_models": [
                "BAAI/bge-base-en-v1.5"
              ]
            },
            {
              "id": "watsonx",
              "label": "IBM watsonx.ai Embeddings",
              "supported_models": [
                "ibm/slate-125m-english-rtrvr"
              ]
            }
          ]
        },
        {
          "type": "reranker",
          "label": "Reranker Model",
          "required": false,
          "providers": [
            {
              "id": "vllm",
              "label": "vLLM Reranker",
              "supported_models": [
                "BAAI/bge-reranker-v2-m3"
              ]
            },
            {
              "id": "watsonx",
              "label": "IBM watsonx.ai Reranker",
              "supported_models": [
                "ibm/slate-125m-english-reranker"
              ]
            }
          ]
        }
      ]
    },
    {
      "type": "service",
      "service_id": "chat",
      "service_name": "Question and Answer",
      "components": [
        {
          "type": "vector_db",
          "label": "Vector store",
          "required": true,
          "providers": [
            {
              "provider_id": "opensearch",
              "provider": "opensearch",
              "label": "OpenSearch",
              "service_id": "opensearch"
            },
            {
              "provider_id": "milvus",
              "provider": "milvus",
              "label": "Milvus",
              "service_id": "milvus"
            }
          ]
        },
        {
          "type": "llm",
          "label": "LLM Model",
          "required": true,
          "providers": [
            {
              "provider_id": "vllm",
              "provider": "vllm",
              "label": "vLLM Instruct",
              "service_id": "instruct-vllm",
              "requires_backend": true,
              "supported_models": [
                {
                  "model_id": "granite-3.3-8b",
                  "name": "ibm-granite/granite-3.3-8b-instruct"
                }
              ]
            },
            {
              "provider_id": "watsonx",
              "provider": "watsonx",
              "label": "IBM watsonx.ai Instruct",
              "service_id": "instruct-watsonx",
              "requires_backend": true,
              "supported_models": [
                {
                  "model_id": "gpt-4",
                  "name": "gpt-4",
                  "provider": "openai"
                }
              ]
            }
          ]
        },
        {
          "type": "embedding",
          "label": "Embedding Model",
          "required": true,
          "providers": [
            {
              "provider_id": "vllm",
              "provider": "vllm",
              "label": "vLLM Embeddings",
              "service_id": "embedding-vllm",
              "requires_backend": true,
              "supported_models": [
                {
                  "model_id": "bge-base",
                  "name": "BAAI/bge-base-en-v1.5"
                }
              ]
            },
            {
              "provider_id": "watsonx",
              "provider": "watsonx",
              "label": "IBM watsonx.ai Embeddings",
              "service_id": "embedding-watsonx",
              "requires_backend": true,
              "supported_models": [
                {
                  "model_id": "text-embedding-ada-002",
                  "name": "text-embedding-ada-002",
                  "provider": "openai"
                }
              ]
            }
          ]
        },
        {
          "type": "reranker",
          "label": "Reranker Model",
          "required": false,
          "providers": [
            {
              "provider_id": "vllm",
              "provider": "vllm",
              "label": "vLLM Reranker",
              "service_id": "reranker-vllm",
              "requires_backend": true,
              "supported_models": [
                {
                  "model_id": "bge-reranker-v2-m3",
                  "name": "BAAI/bge-reranker-v2-m3"
                }
              ]
            },
            {
              "provider_id": "watsonx",
              "provider": "watsonx",
              "label": "IBM watsonx.ai Reranker",
              "service_id": "reranker-watsonx",
              "requires_backend": true,
              "supported_models": [
                {
                  "model_id": "cohere-rerank-english-v3",
                  "name": "rerank-english-v3.0",
                  "provider": "cohere"
                }
              ]
            }
          ]
        }
      ]
    }
  ]
}
```

### 2. GET `/api/v1/services/{id}/options`

Get components and providers for the digitize service (no instances).

**Purpose**: Fetch providers for creating new components.

**Response:**

```json
{
  "service_id": "digitize",
  "service_name": "Digitize documents",
  "components": [
    {
      "type": "vector_db",
      "label": "Vector store",
      "required": true,
      "providers": [
        {
          "id": "opensearch",
          "label": "OpenSearch",
          "description": "Distributed search and analytics engine"
        },
        {
          "id": "milvus",
          "label": "Milvus",
          "description": "Cloud-native vector database"
        }
      ]
    },
    {
      "type": "llm",
      "label": "LLM Model",
      "required": true,
      "providers": [
        {
          "id": "vllm",
          "label": "vLLM Instruct",
          "description": "Deploy new instruct model on vLLM",
          "supported_models": [
            "ibm-granite/granite-3.3-8b-instruct",
            "meta-llama/Llama-3.1-8B-Instruct"
          ]
        },
        {
          "id": "watsonx",
          "label": "IBM watsonx.ai Instruct",
          "description": "Configure watsonx.ai for instruct models",
          "supported_models": [
            "ibm/granite-13b-chat-v2",
            "meta-llama/llama-3-70b-instruct"
          ]
        }
      ]
    },
    {
      "type": "embedding",
      "label": "Embedding Model",
      "required": true,
      "providers": [
        {
          "id": "vllm",
          "label": "vLLM Embeddings",
          "supported_models": [
            "BAAI/bge-base-en-v1.5"
          ]
        },
        {
          "id": "watsonx",
          "label": "IBM watsonx.ai Embeddings",
          "supported_models": [
            "ibm/slate-125m-english-rtrvr"
          ]
        }
      ]
    },
    {
      "type": "reranker",
      "label": "Reranker Model",
      "required": false,
      "providers": [
        {
          "id": "vllm",
          "label": "vLLM Reranker",
          "supported_models": [
            "BAAI/bge-reranker-v2-m3"
          ]
        },
        {
          "id": "watsonx",
          "label": "IBM watsonx.ai Reranker",
          "supported_models": [
            "ibm/slate-125m-english-reranker"
          ]
        }
      ]
    }
  ]
}
```

### 3. GET `/api/v1/architectures/{architecture_id}/params`

Get configuration schemas (JSON Schema) for all component types used in the architecture.

**Purpose**: Fetch configuration schemas for all component types (vector_db, llm, embedding, reranker) that are dependencies of services in this architecture. Returns schemas for all available providers within each component type.

**Example Request:**
```
GET /api/v1/architectures/rag/params
```

**Example Response:**
```json
[
  {
    "type": "component",
    "component_type": "vector_db",
    "provider_id": "opensearch",
    "schema": {
      "$schema": "https://json-schema.org/draft-07/schema#",
      "type": "object",
      "properties": {
        "opensearch": {
          "type": "object",
          "properties": {
            "memoryLimit": {
              "type": "string",
              "description": "Sets the memory limit for the Opensearch service (Default: 8Gi). Override by passing a value with a unit suffix (e.g., Mi, Gi).",
              "default": "8Gi",
              "pattern": "^[0-9]+(Ki|Mi|Gi|Ti|Pi|Ei)$"
            },
            "auth": {
              "type": "object",
              "properties": {
                "username": {
                  "type": "string",
                  "default": "admin"
                },
                "password": {
                  "type": "string",
                  "minLength": 15,
                  "description": "Password for OpenSearch authentication. Must be at least 15 characters and contain at least one uppercase letter, one lowercase letter, one digit, and one special character. Avoid common words, predictable patterns, or dictionary terms.",
                  "allOf": [
                    {
                      "pattern": ".*[a-z].*",
                      "description": "Must contain at least one lowercase letter"
                    },
                    {
                      "pattern": ".*[A-Z].*",
                      "description": "Must contain at least one uppercase letter"
                    },
                    {
                      "pattern": ".*[0-9].*",
                      "description": "Must contain at least one digit"
                    },
                    {
                      "pattern": ".*[@$!%*?&#^()_+\\-=\\[\\]{};':\"\\\\|,.<>/`~].*",
                      "description": "Must contain at least one special character"
                    }
                  ]
                }
              },
              "required": ["password"]
            }
          }
        }
      }
    }
  },
  {
    "type": "component",
    "component_type": "llm",
    "provider_id": "vllm",
    "schema": {
      "$schema": "https://json-schema.org/draft-07/schema#",
      "type": "object",
      "properties": {
        "instruct": {
          "type": "object",
          "properties": {
            "apiSecretKey": {
              "type": "string",
              "title": "API Secret Key",
              "description": "Secret key for API authentication",
              "minLength": 16
            }
          }
        }
      }
    }
  },
  {
    "type": "component",
    "component_type": "llm",
    "provider_id": "watsonx",
    "schema": {
      "$schema": "https://json-schema.org/draft-07/schema#",
      "type": "object",
      "properties": {
        "instruct": {
          "type": "object",
          "properties": {
            "apiKey": {
              "type": "string",
              "title": "API Key",
              "description": "IBM watsonx.ai API key for authentication",
              "minLength": 1
            },
            "projectId": {
              "type": "string",
              "title": "Project ID",
              "description": "IBM watsonx.ai project ID",
              "minLength": 1
            },
            "endpoint": {
              "type": "string",
              "title": "Endpoint URL",
              "description": "IBM watsonx.ai endpoint URL",
              "format": "uri",
              "default": "https://us-south.ml.cloud.ibm.com"
            }
          },
          "required": ["apiKey", "projectId", "endpoint"]
        }
      }
    }
  },
  {
    "type": "component",
    "component_type": "embedding",
    "provider_id": "vllm",
    "schema": {
      "$schema": "https://json-schema.org/draft-07/schema#",
      "type": "object",
      "properties": {
        "embedding": {
          "type": "object",
          "properties": {}
        }
      }
    }
  },
  {
    "type": "component",
    "component_type": "reranker",
    "provider_id": "vllm",
    "schema": {
      "$schema": "https://json-schema.org/draft-07/schema#",
      "type": "object",
      "properties": {
        "reranker": {
          "type": "object",
          "properties": {}
        }
      }
    }
  }
]
```

### 4. GET `/api/v1/components/{component_type}/instances`

Get all running instances for a specific component type.

**Purpose**: Fetch existing, deployed instances for a specific component type to populate dropdowns. The `component_type` parameter specifies which component type to retrieve (e.g., "vector_db", "llm", "embedding", "reranker").

**Example 1: Get vector_db instances**

**Request**: `GET /api/v1/components/vector_db/instances`

**Response:**
```json
[
  {
    "instance_id": "opensearch-instance-1",
    "label": "OpenSearch (default)",
    "provider": "opensearch"
  },
  {
    "instance_id": "milvus-instance-1",
    "label": "Milvus Production",
    "provider": "milvus"
  }
]
```

**Example 2: Get llm instances**

**Request**: `GET /api/v1/components/llm/instances`

**Response:**
```json
[
  {
    "instance_id": "llm-vllm-granite",
    "label": "Granite 3.3 8B (vLLM on Spyre)",
    "provider": "vllm"
  },
  {
    "instance_id": "llm-vllm-llama",
    "label": "Llama 3.1 8B (vLLM on CPU)",
    "provider": "vllm"
  }
]
```

**Example 3: Get embedding instances**

**Request**: `GET /api/v1/components/embedding/instances`

**Response:**
```json
[]
```

**Example 4: Get reranker instances**

**Request**: `GET /api/v1/components/reranker/instances`

**Response:**
```json
[
  {
    "instance_id": "reranker-vllm-1",
    "label": "BGE Reranker (vLLM on Spyre)",
    "provider": "vllm"
  }
]
```

### 4. GET `/api/v1/services/{service_id}/params`

Get configuration schema for a specific service.

**Purpose**: Fetch the configuration schema for a specific service type.

**Example: Get params for digitize service**

**Request**: `GET /api/v1/services/digitize/params`

**Response:**
```json
{
  "$schema": "https://json-schema.org/draft-07/schema#",
  "type": "object",
  "title": "Digitize Service Configuration",
  "properties": {
    "chunk_size": {
      "type": "integer",
      "title": "Chunk Size",
      "description": "Size of text chunks for processing",
      "default": 512,
      "minimum": 128
    },
    "overlap": {
      "type": "integer",
      "title": "Chunk Overlap",
      "description": "Overlap between chunks",
      "default": 50,
      "minimum": 0
    }
  },
  "required": ["chunk_size"]
}
```

### 5. GET `/api/v1/components/{component_type}/providers/{provider_id}/params`

Get configuration schema for a specific provider within a component type.

**Purpose**: When user selects "Create New" for a component and chooses a provider, fetch the configuration schema for that specific provider.

**Example 1: Get params for Milvus vector store provider**

**Request**: `GET /api/v1/components/vector_db/providers/milvus/params`

**Response:**
```json
{
  "$schema": "https://json-schema.org/draft-07/schema#",
  "type": "object",
  "title": "Milvus Configuration",
  "properties": {
    "provider": {
      "type": "string",
      "title": "Provider",
      "const": "milvus",
      "default": "milvus"
    },
    "storage": {
      "type": "string",
      "title": "Storage Size",
      "description": "Persistent storage size for Milvus",
      "pattern": "^[0-9]+(Ki|Mi|Gi|Ti|Pi|Ei)$",
      "default": "20Gi"
    },
    "port": {
      "type": "integer",
      "title": "Service Port",
      "description": "Port for Milvus service",
      "default": 19530,
      "minimum": 1024,
      "maximum": 65535
    }
  },
  "required": ["provider", "storage"]
}
```

**Example 2: Get params for vLLM embedding provider**

**Request**: `GET /api/v1/components/embedding/providers/vllm/params`

**Response:**
```json
{
  "$schema": "https://json-schema.org/draft-07/schema#",
  "type": "object",
  "title": "vLLM Embeddings Configuration",
  "properties": {
    "provider": {
      "type": "string",
      "title": "Provider",
      "const": "vllm",
      "default": "vllm"
    },
    "model": {
      "type": "string",
      "title": "Model Path",
      "description": "HuggingFace model path",
      "default": "BAAI/bge-base-en-v1.5"
    },
    "backend_id": {
      "type": "string",
      "title": "Backend Instance",
      "description": "ID of the existing vLLM backend to use"
    },
    "max_model_len": {
      "type": "integer",
      "title": "Max Model Length",
      "description": "Maximum sequence length",
      "default": 512,
      "minimum": 128
    }
  },
  "required": ["provider", "model", "backend_id"]
}
```

**Example 3: Get params for watsonx instruct provider**

**Request**: `GET /api/v1/components/llm/providers/watsonx/params`

**Response:**
```json
{
  "$schema": "https://json-schema.org/draft-07/schema#",
  "type": "object",
  "title": "IBM watsonx.ai Instruct Configuration",
  "properties": {
    "provider": {
      "type": "string",
      "title": "Provider",
      "const": "watsonx",
      "default": "watsonx"
    },
    "model": {
      "type": "string",
      "title": "Model Name",
      "description": "watsonx.ai model identifier",
      "default": "ibm/granite-13b-chat-v2",
      "enum": ["ibm/granite-13b-chat-v2", "meta-llama/llama-3-70b-instruct", "ibm/granite-20b-multilingual"]
    },
    "api_key": {
      "type": "string",
      "title": "watsonx API Key",
      "description": "IBM Cloud API key for watsonx.ai",
      "format": "password"
    },
    "backend_id": {
      "type": "string",
      "title": "Backend Instance",
      "description": "ID of the existing watsonx backend to use"
    },
    "temperature": {
      "type": "number",
      "title": "Temperature",
      "description": "Sampling temperature",
      "default": 0.7,
      "minimum": 0,
      "maximum": 2
    }
  },
  "required": ["provider", "model", "api_key", "backend_id"]
}
```

### 6. POST `/api/v1/applications`

**Request Body:**

```json
{
  "name": "Production RAG System",
  "architecture": "rag",
  "params": [
    {
      "type": "component",
      "component_type": "vector_db",
      "provider_id": "opensearch",
      "config": {
        "memoryLimit": "4Gi",
        "auth": {
          "username": "admin",
          "password": "SecurePassword123!@#"
        }
      }
    }
  ],
  "services": [
    {
      "type": "service",
      "service_id": "digitize",
      "enabled": true,
      "version": "1.0.0",
      "params": {
        "chunk_size": 512,
        "overlap": 50
      },
      "components": [
        {
          "type": "component",
          "component_type": "vector_db",
          "provider_id": "opensearch",
          "params": {
            "memoryLimit": "8Gi",
            "auth": {
              "username": "admin",
              "password": "AnotherSecurePass456!@#"
            }
          }
        },
        {
          "type": "component",
          "component_type": "llm",
          "instance_id": "llm-vllm-granite",
          "provider_id": "vllm"
        },
        {
          "type": "component",
          "component_type": "embedding",
          "provider_id": "vllm",
          "params": {
            "model": "BAAI/bge-base-en-v1.5"
          }
        },
        {
          "type": "component",
          "component_type": "reranker",
          "instance_id": "reranker-vllm-1",
          "provider_id": "vllm"
        }
      ]
    },
    {
      "type": "service",
      "service_id": "chat",
      "enabled": true,
      "version": "1.0.0",
      "params": {
        "max_history": 10,
        "temperature": 0.7
      },
      "components": [
        {
          "type": "component",
          "component_type": "vector_db",
          "instance_id": "opensearch-instance-1",
          "provider_id": "opensearch"
        },
        {
          "type": "component",
          "component_type": "llm",
          "provider_id": "watsonx",
          "params": {
            "model": "ibm/granite-13b-chat-v2",
            "apiKey": "ibm-cloud-api-key-456",
            "projectId": "wx-project-123",
            "endpoint": "https://us-south.ml.cloud.ibm.com"
          }
        },
        {
          "type": "component",
          "component_type": "embedding",
          "provider_id": "watsonx",
          "params": {
            "model": "ibm/slate-125m-english-rtrvr",
            "apiKey": "ibm-cloud-api-key-456",
            "projectId": "wx-project-123",
            "endpoint": "https://us-south.ml.cloud.ibm.com"
          }
        }
      ]
    }
  ]
}
```

**Request Structure:**

The POST request body contains three main sections, all using array-based structures with type discriminators:

1. **Global Architecture Params** (`params`):
   - Array of architecture-level configurations
   - Each element has: `type: "component"`, `component_type`, `provider_id`, `config`
   - Structure: `params[].{type, component_type, provider_id, config}`
   - Example: `{ "type": "component", "component_type": "vector_db", "provider_id": "opensearch", "config": {...} }`
   - These params apply globally across all services using that component type and provider

2. **Services** (`services`):
   - Array of service configurations
   - Each element has: `type: "service"`, `service_id`, `enabled`, `version`, `params`, `components`
   - Service-level params are directly under `params` (no extra nesting)
   - Example: `{ "type": "service", "service_id": "digitize", "params": { "chunk_size": 512 } }`

3. **Components** (`services[].components`):
   - Array of component configurations for each service
   - Each element has: `type: "component"`, `component_type`, `provider_id`, and optionally `instance_id` or `params`
   - Components can be either reused (with `instance_id`) or newly created (with `params`)

**Component Selection Logic:**

Each component in the components array can be specified in one of two ways:

1. **Reuse Existing Component** - When `instance_id` field is present:
   - The backend will use an existing, already-deployed component instance
   - Required fields: `type: "component"`, `component_type`, `instance_id`, `provider_id`
   - Example: `{ "type": "component", "component_type": "vector_db", "instance_id": "opensearch-instance-1", "provider_id": "opensearch" }`

2. **Create New Component** - When `instance_id` field is absent:
   - The backend will create and deploy a new component instance
   - Required fields: `type: "component"`, `component_type`, `provider_id`, `params`
   - Example: `{ "type": "component", "component_type": "vector_db", "provider_id": "opensearch", "params": { "memoryLimit": "8Gi", "auth": {...} } }`

**Note:** The presence of `instance_id` indicates reusing an existing component. If `instance_id` is absent, a new component will be created using the provided `params`. The `type` field is always `"component"` and `component_type` identifies the specific component type (vector_db, llm, embedding, reranker).

---

## Implementation Notes

### Backend Implementation

1. **GET `/api/v1/architectures/{id}/options`**
   - Return providers and dependency rules only (no instances)
   - Include providers for all component types
   - Include dependency rules and metadata
   - Include `supported_models` arrays for all providers

2. **GET `/api/v1/architectures/{id}/instances`**
   - Return all running instances grouped by component type
   - Filter instances relevant to the architecture
   - Include wiring information (backend_id for dependent components)

3. **GET `/api/v1/services/{service_id}/options`**
   - Same as architecture-level but scoped to single service
   - Return providers and dependency rules only

4. **GET `/api/v1/services/{service_id}/instances`**
   - Return running instances for a specific service
   - Grouped by component type

5. **GET `/api/v1/services/{service_id}/params`**
   - Return JSON schema for the specified service
   - Schema includes service-level configurable fields
   - Return service-specific configuration schema

6. **GET `/api/v1/components/{component_type}/providers/{provider_id}/params`**
   - Return JSON schema for the specified provider within a component type
   - Schema includes all provider-specific configurable fields
   - User will fill in values like model, backend_id, storage, etc. in the form
   - Return provider-specific configuration schema

### UI Implementation

1. **Initial Load**
   - Call GET `/options` - receives all options including dependent component options for each backend
   - Show disabled/hidden state for dependent components with message "Select inference backend first"

2. **After Inference Backend Selection**
   - UI filters and displays llm/embedding/reranker options based on selected backend
   - Enable and populate dropdowns using the `options_by_backend[selected_backend]` data
   - No additional API call needed - all data already available from step 1

3. **Progressive Form Building (Per Component)**
   - When user selects "Create New" for a component:
     - User selects provider from available providers for that component type
     - Call GET `/api/v1/components/{component_type}/providers/{provider_id}/params`
     - Receive JSON schema for that specific provider
     - Render configuration form for that provider
     - User fills the form (including model, backend_id, storage, etc.)
   - Optionally call GET `/api/v1/services/{service_id}/params` for service-level configuration
   - Repeat for each "Create New" component
   - Forms are built incrementally as user makes selections

4. **Deployment**
   - Once all selections and configurations are complete
   - Submit to POST `/applications` with all selections and configs

---
