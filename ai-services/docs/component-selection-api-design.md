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

Get configuration form fields for all services in the architecture.

**Purpose**: Fetch all configuration schemas for services that can be created in this architecture.

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
  "services": {
    "digitize": {
      "service_id": "digitize",
      "service_name": "Digitize documents",
      "components": {
        "vector_db": {
          "label": "Vector store",
          "required": true,
          "providers": [
            {
              "provider_id": "opensearch",
              "provider": "opensearch",
              "label": "OpenSearch",
              "description": "Distributed search and analytics engine"
            },
            {
              "provider_id": "milvus",
              "provider": "milvus",
              "label": "Milvus",
              "description": "Cloud-native vector database"
            }
          ]
        },
        "llm": {
          "label": "LLM Model",
          "required": true,
          "providers": [
            {
              "provider_id": "vllm",
              "provider": "vllm",
              "label": "vLLM Instruct",
              "description": "Deploy new instruct model on vLLM",
              "supported_models": [
                "ibm-granite/granite-3.3-8b-instruct",
                "meta-llama/Llama-3.1-8B-Instruct"
              ]
            },
            {
              "provider_id": "watsonx",
              "provider": "watsonx",
              "label": "IBM watsonx.ai Instruct",
              "description": "Configure watsonx.ai for instruct models",
              "supported_models": [
                "ibm/granite-13b-chat-v2",
                "meta-llama/llama-3-70b-instruct"
              ]
            }
          ]
        },
        "embedding": {
          "label": "Embedding Model",
          "required": true,
          "providers": [
            {
              "provider_id": "vllm",
              "provider": "vllm",
              "label": "vLLM Embeddings",
              "supported_models": [
                "BAAI/bge-base-en-v1.5"
              ]
            },
            {
              "provider_id": "watsonx",
              "provider": "watsonx",
              "label": "IBM watsonx.ai Embeddings",
              "supported_models": [
                "ibm/slate-125m-english-rtrvr"
              ]
            }
          ]
        },
        "reranker": {
          "label": "Reranker Model",
          "required": false,
          "providers": [
            {
              "provider_id": "vllm",
              "provider": "vllm",
              "label": "vLLM Reranker",
              "supported_models": [
                "BAAI/bge-reranker-v2-m3"
              ]
            },
            {
              "provider_id": "watsonx",
              "provider": "watsonx",
              "label": "IBM watsonx.ai Reranker",
              "supported_models": [
                "ibm/slate-125m-english-reranker"
              ]
            }
          ]
        }
      }
    },
    "chat": {
      "service_id": "chat",
      "service_name": "Question and Answer",
      "components": {
        "vector_db": {
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
        "llm": {
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
        "embedding": {
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
        "reranker": {
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
      }
    }
  }
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
  "components": {
    "vector_db": {
      "label": "Vector store",
      "required": true,
      "providers": [
        {
          "provider_id": "opensearch",
          "provider": "opensearch",
          "label": "OpenSearch",
          "description": "Distributed search and analytics engine"
        },
        {
          "provider_id": "milvus",
          "provider": "milvus",
          "label": "Milvus",
          "description": "Cloud-native vector database"
        }
      ]
    },
    "llm": {
      "label": "LLM Model",
      "required": true,
      "providers": [
        {
          "provider_id": "vllm",
          "provider": "vllm",
          "label": "vLLM Instruct",
          "description": "Deploy new instruct model on vLLM",
          "supported_models": [
            "ibm-granite/granite-3.3-8b-instruct",
            "meta-llama/Llama-3.1-8B-Instruct"
          ]
        },
        {
          "provider_id": "watsonx",
          "provider": "watsonx",
          "label": "IBM watsonx.ai Instruct",
          "description": "Configure watsonx.ai for instruct models",
          "supported_models": [
            "ibm/granite-13b-chat-v2",
            "meta-llama/llama-3-70b-instruct"
          ]
        }
      ]
    },
    "embedding": {
      "label": "Embedding Model",
      "required": true,
      "providers": [
        {
          "provider_id": "vllm",
          "provider": "vllm",
          "label": "vLLM Embeddings",
          "supported_models": [
            "BAAI/bge-base-en-v1.5"
          ]
        },
        {
          "provider_id": "watsonx",
          "provider": "watsonx",
          "label": "IBM watsonx.ai Embeddings",
          "supported_models": [
            "ibm/slate-125m-english-rtrvr"
          ]
        }
      ]
    },
    "reranker": {
      "label": "Reranker Model",
      "required": false,
      "providers": [
        {
          "provider_id": "vllm",
          "provider": "vllm",
          "label": "vLLM Reranker",
          "supported_models": [
            "BAAI/bge-reranker-v2-m3"
          ]
        },
        {
          "provider_id": "watsonx",
          "provider": "watsonx",
          "label": "IBM watsonx.ai Reranker",
          "supported_models": [
            "ibm/slate-125m-english-reranker"
          ]
        }
      ]
    }
  }
}
```

### 3. GET `/api/v1/components/{component_type}/instances`

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
  "services": {
    "digitize": {
      "enabled": true,
      "components": {
        "vector_db": {
          "id": "create-new",
          "type": "create_new",
          "provider": "milvus",
          "service_id": "milvus",
          "config": {
            "storage": "20Gi",
            "port": 19530
          }
        },
        "inference_backend": {
          "id": "vllm-instance-1",
          "type": "existing",
          "provider": "vllm"
        },
        "llm": {
          "id": "llm-vllm-granite",
          "type": "existing",
          "provider": "vllm",
          "backend_id": "vllm-instance-1"
        },
        "embedding": {
          "id": "create-new",
          "type": "create_new",
          "provider": "vllm",
          "service_id": "embedding-vllm",
          "backend_id": "vllm-instance-1",
          "config": {
            "model": "BAAI/bge-base-en-v1.5"
          }
        },
        "reranker": {
          "id": "reranker-vllm-1",
          "type": "existing",
          "provider": "vllm",
          "backend_id": "vllm-instance-1"
        }
      }
    },
    "chat": {
      "enabled": true,
      "components": {
        "vector_db": {
          "id": "opensearch-instance-1",
          "type": "existing"
        },
        "inference_backend": {
          "id": "watsonx-instance-1",
          "type": "existing",
          "provider": "watsonx"
        },
        "llm": {
          "id": "create-new",
          "type": "create_new",
          "provider": "watsonx",
          "service_id": "instruct-watsonx",
          "backend_id": "watsonx-instance-1",
          "config": {
            "model": "ibm/granite-13b-chat-v2",
            "api_key": "ibm-cloud-api-key-456"
          }
        },
        "embedding": {
          "id": "create-new",
          "type": "create_new",
          "provider": "watsonx",
          "service_id": "embedding-watsonx",
          "backend_id": "watsonx-instance-1",
          "config": {
            "model": "ibm/slate-125m-english-rtrvr",
            "api_key": "ibm-cloud-api-key-456"
          }
        }
      }
    }
  }
}
```

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
   - Render dropdowns for independent components (vector_db, inference_backend)
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

## Summary

This dependency-aware design provides:

✅ **Component Dependencies**: llm/embedding/reranker depend on inference_backend
✅ **Progressive Disclosure**: Dependent dropdowns only appear after parent selection
✅ **Filtered Options**: Options filtered client-side based on parent's provider
✅ **Backend Tracking**: backend_id links dependent components to their backend
✅ **Dynamic & Flexible**: Supports complex component relationships
✅ **Clean API**: Two GET APIs - `options`, `params` (service-specific, no request body)
✅ **Architecture & Service Level**: Works for both deployment modes
✅ **Efficient**: Single options call, then individual params calls per service
✅ **Service-Specific Schemas**: Each service gets its own tailored configuration schema