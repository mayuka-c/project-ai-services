# Centralized Secret Management with HashiCorp Vault — Design Proposal

**Version:** 1.0  
**Date:** July 2026  
**Status:** Draft / Proposal

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Background and Motivation](#2-background-and-motivation)
   - [2.1 Current State](#21-current-state)
   - [2.2 Problems](#22-problems)
   - [2.3 Goals](#23-goals)
3. [Architecture Overview](#3-architecture-overview)
4. [Deployment Lifecycle](#4-deployment-lifecycle)
   - [4.1 Phase 1 — Configure (one-time setup)](#41-phase-1--configure-one-time-setup)
   - [4.2 Phase 2 — Runtime (steady state)](#42-phase-2--runtime-steady-state)
5. [Vault Server Deployment](#5-vault-server-deployment)
   - [5.1 Pod Manifest](#51-pod-manifest)
   - [5.2 Vault Configuration (HCL)](#52-vault-configuration-hcl)
6. [Vault Bootstrap Pod](#6-vault-bootstrap-pod)
7. [Vault Configuration Steps](#7-vault-configuration-steps)
   - [7.1 Enable KV v2](#71-enable-kv-v2)
   - [7.2 Enable AppRole Auth](#72-enable-approle-auth)
   - [7.3 Catalog Admin Policy](#73-catalog-admin-policy)
   - [7.4 Catalog AppRole Identity](#74-catalog-approle-identity)
8. [Service Credential Provisioning](#8-service-credential-provisioning)
   - [8.1 Component Deployment — PostgreSQL](#81-component-deployment--postgresql)
   - [8.2 Catalog Backend Pod (`catalog.yaml.tmpl`)](#82-catalog-backend-pod-catalogyamltmpl)
   - [8.3 OpenSearch Component](#83-opensearch-component)
9. [Vault Agent Sidecar Pattern](#9-vault-agent-sidecar-pattern)
   - [9.1 Agent Configuration (HCL)](#91-agent-configuration-hcl)
   - [9.2 Service Pod Manifest](#92-service-pod-manifest)
   - [9.3 Secret Object](#93-secret-object)
10. [Unseal Architecture](#10-unseal-architecture)
11. [Key Design Decisions](#11-key-design-decisions)
12. [Security Considerations](#12-security-considerations)
13. [Disaster Recovery](#13-disaster-recovery)
14. [Future Considerations](#14-future-considerations)

---

## 1. Executive Summary

This proposal introduces **HashiCorp Vault** as the centralized secret management layer for the AI-Services Catalog platform. Today, every service manages its own credentials in an ad-hoc fashion — API keys and passwords live in container environment variables, config files, or Podman secrets without any consistent access-control, audit, or rotation policy.

The proposed architecture:

- Deploys Vault as a **long-lived Podman pod** alongside the existing Catalog stack.
- Authenticates every service to Vault via **AppRole** — each service gets its own role, its own policy, and access to only its own secrets.
- Injects tokens into service containers through a **Vault Agent sidecar** pattern so that service code never handles raw credentials.
- Handles automatic unsealing after a VM reboot through a **bootstrap pod** that reads the unseal key from a Podman secret — the key never leaves the Vault container.
- Stores all Vault data (policies, KV secrets, AppRoles, audit logs) in a Raft-based file backend; only the unseal key requires separate offline backup.

---

## 2. Background and Motivation

### 2.1 Current State

The Catalog platform deploys application services (chatbot, digitize, similarity, summarize, LiteLLM gateway, etc.) as Podman pods. Credentials that those services need — database passwords, API keys, model provider tokens — are currently passed at deploy time through one or more of the following mechanisms:

- Hard-coded or templated environment variables in pod YAML manifests.
- Per-service Podman secrets created manually during installation.
- Plain-text values stored in on-disk configuration files.

There is no central store, no audit trail, no per-service access control, and no rotation capability.

### 2.2 Problems

- **No least-privilege model.** Any process that can read a Podman secret can read every other secret in the same namespace.
- **No audit trail.** It is impossible to know which service read which secret, or when.
- **No rotation.** Rotating a credential requires re-deploying the pod; there is no out-of-band rotation path.
- **Credential sprawl.** Secrets exist in multiple places (manifests, host filesystem, environment variables), making a full audit impractical.
- **Multi-VM secret synchronisation.** Podman secrets are local to a single host. In a remote or multi-VM deployment — where the same service must run on several nodes — each Podman secret (`role_id`, `secret_id`, API keys, database passwords) must be manually replicated to every host. There is no synchronisation mechanism: a rotation on one VM does not propagate to the others, leaving nodes with divergent credential state. Adding a new VM to the fleet requires a full, error-prone re-provisioning of every secret by hand.

### 2.3 Goals

1. Centralize all service credentials in a single, auditable secret store.
2. Enforce strict least-privilege: each service can only read its own secrets.
3. Remove credentials from pod manifests, environment variables, and on-disk config files.
4. Enable automated unseal after a VM reboot without operator intervention.
5. Provide a clear provisioning path when Catalog deploys a new service.
6. Keep the operational footprint minimal — one additional pod, no external dependencies.

---

## 3. Architecture Overview

```mermaid
flowchart TD
    subgraph Host["Host VM"]

        subgraph VaultPod["vault pod (always running)"]
            V["HashiCorp Vault :8200\nvault.hcl — Raft/file backend"]
        end

        subgraph BootstrapPod["vault-bootstrap pod (OnFailure)"]
            B["vault operator unseal\nReads /run/secrets/vault-unseal-key"]
        end

        subgraph ServicePod["tenant-service-pod"]
            VA["vault-agent sidecar\nAppRole auto_auth\nWrites /vault/token"]
            SVC["application container\nReads /vault/token"]
        end

        PS["Podman Secret\nvault-unseal-key"]
        PS2["Podman Secret\ntenant-vault-credentials\n(role_id + secret_id)"]

        V -->|"AppRole login"| VA
        B -->|"reads"| PS
        B -->|"unseals"| V
        VA -->|"reads"| PS2
        VA -->|"writes token"| SVC
    end
```

**Component responsibilities:**

| Component | What it does |
|---|---|
| `vault` pod | Stores and serves all secrets; enforces policies; logs every access |
| `vault-bootstrap` pod | Runs at boot (`OnFailure`) to unseal Vault; exits immediately after |
| `vault-agent` sidecar | Per-service sidecar; authenticates to Vault via AppRole; writes a short-lived token to a shared memory volume |
| `catalog-admin` AppRole | Identity used by Catalog during installation to create per-service policies and AppRoles |
| Per-service AppRole | Unique identity for each deployed service; scoped to that service's KV path only |
| Podman secret `vault-unseal-key` | Holds the single unseal key; mounted read-only inside the bootstrap pod |
| Podman secret `<service>-vault-credentials` | Holds `role_id` and `secret_id` for a service; mounted read-only inside that service's pod |

---

## 4. Deployment Lifecycle

### 4.1 Phase 1 — Configure (one-time setup)

`catalog configure` orchestrates the following steps in order:

```
1. Deploy vault.yaml (Vault server pod)
    ↓
Wait until Vault API is reachable
    ↓
vault operator init -key-shares=1 -key-threshold=1
    ↓ emits: Unseal Key + Initial Root Token
podman secret create vault-unseal-key <unseal-key> (Use kube play)
    ↓
2. Deploy vault-bootrap.yaml (bootstrap pod)
    ↓  (reads vault-unseal-key Podman secret → vault operator unseal)
Vault is unsealed
    ↓
3. Use Initial Root Token to configure Vault for Catalog Service:
    a. Enable KV v2 (Global)
    b. Enable AppRole
    c. Create catalog-admin policy
    d. Create catalog AppRole → fetch RoleID + SecretID
    ↓
podman secret create catalog-vault-credentials (role_id + secret_id)
    ↓
4. Deploy Catalog assets (UI pod, backend API pod, database pod)
```

> **Note:** `vault operator init` is the only moment the Initial Root Token is ever issued. It is held in memory by `catalog configure` for the duration of the Vault configuration steps and is **never written to disk**. Once the catalog AppRole credentials are stored, all subsequent Catalog operations use the `catalog-admin` AppRole token instead.

See Sections 5–7 for the detailed manifests and commands for each step.

### 4.2 Phase 2 — Runtime (steady state)

When Catalog deploys a new service/component (e.g., `chat`):

```
catalog AppRole login  →  Vault token
    ↓
Create policy  chat-policy:
    - read access  → secret/data/application/<app-id>/chat/*  (service's own secrets)
    - read access  → secret/data/component/<comp-id>/*        (shared component secrets,
                     e.g. OpenSearch password, PostgreSQL password,
                     only for components this service consumes)
    ↓
Create AppRole  chat
    ↓
Read  chat  RoleID
    ↓
Generate  chat  SecretID
    ↓
podman secret create chat-vault-credentials  (role_id + secret_id)
    ↓
Deploy service pod (vault-agent sidecar + chat container)
```

The policy grants each service read access to its own secret path **and** read access to the paths of the shared components it depends on. Component secrets (passwords, connection strings) are written into Vault once when the component is deployed and are never duplicated across service manifests.

Each service authenticates independently. The Catalog API server holds the `catalog-admin` token in memory (refreshed automatically); it never persists the token to disk.

---

## 5. Vault Server Deployment

### 5.1 Pod Manifest

`vault.yaml`:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: vault
spec:
  restartPolicy: Always

  volumes:
    - name: vault-data
      hostPath:
        path: /opt/catalog/vault/data
        type: DirectoryOrCreate

    - name: vault-config
      hostPath:
        path: /opt/catalog/vault/config
        type: Directory

  containers:
    - name: vault
      image: icr.io/ppc64le-oss/vault-ppc64le:v1.14.8
      env:
        - name: VAULT_ADDR
          value: http://127.0.0.1:8200
      command:
        - vault
      args:
        - server
        - -config=/vault/config/vault.hcl
      ports:
        - containerPort: 8200
          hostPort: 8200
      volumeMounts:
        - mountPath: /vault/data:z
          name: vault-data
        - mountPath: /vault/config:z
          name: vault-config
```

**Key points:**

- `restartPolicy: Always` — Podman restarts the pod after a crash or reboot.
- `hostPort: 8200` — Vault is accessible from the host at `http://127.0.0.1:8200`. No external exposure is needed.
- Data and config are on host-path volumes so the Vault data survives container replacement.

After the configure phase completes, the manifest is updated to mount the Podman secret and redeployed:

```yaml
    - name: vault
      ...
      secrets:
        - vault-unseal-key
```

```bash
podman kube play --replace vault.yaml
```

### 5.2 Vault Configuration (HCL)

`vendor/vault/vault.hcl`:

```hcl
ui = false

storage "file" {
  path = "/vault/data"
}

listener "tcp" {
  address     = "0.0.0.0:8200"
  tls_disable = "true"
}

disable_mlock = true
```

**Notes:**

- `ui = false` — the browser UI is disabled; all access is through the CLI or API.
- `storage "file"` — uses Vault's built-in Raft-compatible file backend. All state (KV, policies, AppRoles, audit log) is stored under `/vault/data`.
- `tls_disable = "true"` — TLS is disabled for the initial implementation because all communication is intra-host (loopback or pod-to-pod). Enabling TLS is addressed in [Section 14](#14-future-considerations).
- `disable_mlock = false` — memory locking is **enabled**; this requires the `IPC_LOCK` capability granted in the pod manifest.

---

## 6. Vault Bootstrap Pod

`vendor/vault/vault-bootrap.yaml`:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: vault-bootstrap
spec:
  restartPolicy: OnFailure

  volumes:
    - name: vault-secret-source
      secret:
        secretName: vault-unseal-key

  containers:
    - name: vault-bootstrap
      image: icr.io/ppc64le-oss/vault-ppc64le:v1.14.8
      env:
        - name: VAULT_ADDR
          value: http://vault:8200
      command:
        - /bin/sh
        - -c
        - |
          echo "Waiting for Vault API..."
          until vault status -format=json > /dev/null 2>&1 || [ $? -eq 2 ]; do
            sleep 2
          done

          INITIALIZED=$(vault status -format=json | grep '"initialized":' | awk '{print $2}' | tr -d ',')
          SEALED=$(vault status -format=json      | grep '"sealed":'      | awk '{print $2}' | tr -d ',')

          if [ "$INITIALIZED" != "true" ]; then
            echo "Vault not initialized — nothing to do."
            exit 1
          fi

          if [ "$SEALED" != "true" ]; then
            echo "Vault already unsealed."
            exit 0
          fi

          echo "Unsealing..."
          vault operator unseal "$(cat /run/secrets/vault-unseal-key)"
          echo "Done."

      volumeMounts:
        - name: vault-secret-source
          mountPath: /run/secrets
          readOnly: true
```

**Behaviour:**

| Vault state | Bootstrap action | Exit code |
|---|---|---|
| Not yet initialized | Log and exit — first-time configure has not run yet | 1 (OnFailure retries) |
| Initialized, unsealed | Log and exit — nothing to do | 0 |
| Initialized, sealed | Read key → `vault operator unseal` → exit | 0 |

`restartPolicy: OnFailure` means the bootstrap pod will retry if Vault is not yet reachable at startup (e.g., it starts before the vault pod). Once it exits 0 it will not run again until the pod is explicitly restarted (e.g., after a reboot via the systemd service).

---

## 7. Vault Configuration Steps

All steps in this section are performed once during `catalog configure`, using the **initial root token**. The root token is revoked at the end of this phase.

### 7.1 Enable KV v2

```bash
vault secrets enable -version=2 -path=secret kv
```

All application secrets are stored under `secret/<service-name>/...`. The KV v2 backend provides versioning and a soft-delete capability at no additional cost.

### 7.2 Enable AppRole Auth

```bash
vault auth enable approle
```

AppRole is the only authentication method enabled. It is machine-to-machine — no human interactive login is needed in steady state.

### 7.3 Catalog Admin Policy

```hcl
# /tmp/catalog-admin.hcl
path "*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo", "patch"]
}
```

```bash
vault policy write catalog-admin /tmp/catalog-admin.hcl
```

This broad policy is intentional: the `catalog-admin` role is used exclusively by the Catalog API server during service provisioning (not during request serving). Its credentials are stored in a Podman secret, not in the database or environment variables.

Per-service policies are narrow (see [Section 8](#8-service-credential-provisioning)).

### 7.4 Catalog AppRole Identity

```bash
# Create the AppRole
vault write auth/approle/role/catalog \
    token_policies="catalog-admin"

# Fetch RoleID (static; never changes)
vault read auth/approle/role/catalog/role-id

# Generate SecretID (one-time; rotatable)
vault write -f auth/approle/role/catalog/secret-id
```

The `role_id` and `secret_id` are stored as a single Podman secret:

```yaml
# vendor/vault/service1-vault-secret.yaml (pattern)
apiVersion: v1
kind: Secret
metadata:
  name: catalog-vault-credentials
type: Opaque
stringData:
  role_id:   "<role-id-value>"
  secret_id: "<secret-id-value>"
```

```bash
podman secret create catalog-vault-credentials catalog-credentials.yaml
```


---

## 8. Service Credential Provisioning

When Catalog deploys a new service (e.g., `chat` under application `app-123` that depends on an OpenSearch component `comp-456`), the following steps are performed by the Catalog API server using the `catalog-admin` token obtained by logging in with the `catalog` AppRole:

```bash
# Step 1 — Create a narrow policy for the service
#   - read/list own secrets under the application/service path
#   - read access to each component this service depends on
cat > /tmp/chat-policy.hcl <<'EOF'
# Service's own secrets
path "secret/data/application/<app-id>/chat/*" {
  capabilities = ["read", "list"]
}
path "secret/metadata/application/<app-id>/chat/*" {
  capabilities = ["list"]
}

# Shared component secrets (one block per component the service consumes)
path "secret/data/component/<comp-id>/*" {
  capabilities = ["read"]
}
path "secret/metadata/component/<comp-id>/*" {
  capabilities = ["list"]
}
EOF
vault policy write chat-policy /tmp/chat-policy.hcl

# Step 2 — Write the components's own secrets into KV
vault kv put secret/component/<comp-id>/config \
    api_key="<value>"

# Step 3 — Create a service-scoped AppRole
vault write auth/approle/role/chat \
    token_policies="chat-policy" \
    token_ttl="1h" \
    token_max_ttl="4h"

# Step 4 — Fetch credentials
ROLE_ID=$(vault read -field=role_id auth/approle/role/chat/role-id)
SECRET_ID=$(vault write -f -field=secret_id auth/approle/role/chat/secret-id)

# Step 5 — Store them as a Podman secret
printf '{"role_id":"%s","secret_id":"%s"}' "$ROLE_ID" "$SECRET_ID" \
    | podman secret create chat-vault-credentials -
```

The service pod is then deployed with this Podman secret mounted (see [Section 9](#9-vault-agent-sidecar-pattern)).

**Secret path scheme:**

| Secret type | KV path pattern | Example |
|---|---|---|
| Service secrets | `secret/data/application/<app-id>/<service>/*` | `secret/data/application/app-123/chat/config` |
| Component secrets | `secret/data/component/<comp-id>/*` | `secret/data/component/comp-456/credentials` |

Component secrets (e.g. OpenSearch password, PostgreSQL password) are written once when the component is deployed. Services only receive `read` access to the specific component paths they depend on — they cannot read credentials of components they do not use.

### 8.1 Component Deployment — PostgreSQL

The existing `catalog-db.yaml.tmpl` reads the Postgres password from a Podman secret (`catalog-db-secret`) mounted at `/etc/secret/catalog-db-secret/db-password`. With Vault, **no Podman secret is created**. Instead, the Vault Agent sidecar renders the password directly into a shared volume file before Postgres starts — using Vault Agent's template rendering feature.

**At `catalog configure` time**, Catalog generates the Postgres password and writes it into Vault KV:

```bash
# Generate password and store in Vault — no Podman secret created
vault kv put secret/component/<comp-id>/credentials     db-password="<generated-password>"
```

**Updated `catalog-db.yaml.tmpl`** — the pod gains a Vault Agent sidecar and a shared volume; the `catalog-db-secret` secret volume and label are removed:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: "{{ .AppName }}--db"
  labels:
    ai-services.io/application: "{{ .AppName }}"
    ai-services.io/template: "{{ .AppTemplateName }}"
    ai-services.io/version: "{{ .Version }}"
    ai-services.io/volume: "postgres-catalog"
    ai-services.io/volume-skip-cleanup: "true"
  annotations:
    io.podman.annotations.pids-limit/postgresql: "4096"
spec:
  restartPolicy: always

  volumes:
    - name: postgres-data
      persistentVolumeClaim:
        claimName: "postgres-catalog"

    # Shared volume where vault-agent renders the password file
    - name: vault-rendered
      emptyDir: {}

    # Podman secret holding this component's Vault AppRole credentials
    - name: db-vault-secret
      secret:
        secretName: "{{ .AppName }}-db-vault-credentials"

    # Vault Agent config for template rendering
    - name: vault-agent-config
      hostPath:
        path: /root/mayuka/vault/agent/db-agent.hcl
        type: File

  containers:

    # -----------------------------------------------------------------
    # CONTAINER 1: Vault Agent — renders db-password into shared volume
    # -----------------------------------------------------------------
    - name: vault-agent
      image: icr.io/ppc64le-oss/vault-ppc64le:v1.14.8
      command:
        - vault
        - agent
        - -config=/etc/vault/agent.hcl
      volumeMounts:
        - name: vault-rendered
          mountPath: /vault/rendered
        - name: vault-agent-config
          mountPath: /etc/vault/agent.hcl
        - name: db-vault-secret
          mountPath: /run/secrets/vault
          readOnly: true

    # -----------------------------------------------------------------
    # CONTAINER 2: PostgreSQL — reads password from rendered file
    # -----------------------------------------------------------------
    - name: postgresql
      image: "{{ .Values.db.image }}"
      command:
        - "/bin/sh"
        - "-c"
      args:
        - |
          # Wait for vault-agent to render the password file
          until [ -f /vault/rendered/db-password ]; do
            echo "[*] Waiting for db-password..."
            sleep 2
          done
          export POSTGRES_PASSWORD=$(cat /vault/rendered/db-password)
          exec docker-entrypoint.sh postgres
      ports:
        - containerPort: 5432
          hostPort: {{ .Values.db.port }}
      env:
        - name: POSTGRES_USER
          value: "{{ .Values.db.user }}"
        - name: POSTGRES_DB
          value: "{{ .Values.db.database }}"
        - name: PGDATA
          value: "/var/lib/pgsql/data"
      securityContext:
        runAsUser: 26
        fsGroup: 26
      volumeMounts:
        - name: postgres-data
          mountPath: /var/lib/pgsql:z
        - name: vault-rendered
          mountPath: /vault/rendered
          readOnly: true
      livenessProbe:
        exec:
          command:
            - /bin/sh
            - -c
            - pg_isready -U {{ .Values.db.user }} -d {{ .Values.db.database }}
        initialDelaySeconds: 30
        periodSeconds: 10
        timeoutSeconds: 5
        failureThreshold: 3
      readinessProbe:
        exec:
          command:
            - /bin/sh
            - -c
            - pg_isready -U {{ .Values.db.user }} -d {{ .Values.db.database }}
        initialDelaySeconds: 10
        periodSeconds: 5
        timeoutSeconds: 3
        failureThreshold: 3
      resources:
        requests:
          memory: "{{ .Values.db.resources.requests.memory }}"
        limits:
          memory: "{{ .Values.db.resources.limits.memory }}"
```

**Vault Agent HCL for template rendering (`db-agent.hcl`):**

```hcl
pid_file = "/tmp/vault-agent.pid"

vault {
  address = "http://vault:8200"
}

auto_auth {
  method "approle" {
    mount_path = "auth/approle"

    config = {
      role_id_file_path   = "/run/secrets/vault/role_id"
      secret_id_file_path = "/run/secrets/vault/secret_id"
    }
  }
}

template {
  contents    = "{{ with secret "secret/data/component/<comp-id>/credentials" }}{{ .Data.data.db-password }}{{ end }}"
  destination = "/vault/rendered/db-password"
}
```

**How it fits together:**

| Step | What happens |
|---|---|
| `catalog configure` | Generates password → writes to `secret/component/<comp-id>/credentials` in Vault KV |
| Pod starts | `vault-agent` authenticates via AppRole, renders password to `/vault/rendered/db-password` |
| Postgres starts | Reads `/vault/rendered/db-password` via `$(cat ...)` — identical to before, different source |
| Service (`chat`) needs password | Reads `secret/data/component/<comp-id>/credentials` via its own Vault token — never touches Postgres's pod |
| Password rotation | `vault kv put` updates the KV entry → Vault Agent re-renders the file → `POSTGRES_PASSWORD` picks it up on next Postgres restart |

**No Podman secret is created for the password.** Vault is the single source of truth. The `catalog-db-secret` secret volume, the `ai-services.io/secret` label, and the `ai-services.io/secret-skip-cleanup` label are all removed from the template.

---

### 8.2 Catalog Backend Pod (`catalog.yaml.tmpl`)

The Catalog backend pod (`catalog.yaml.tmpl`) currently reads two secrets from Podman secret mounts:

| Secret | Current mount path | Used by |
|---|---|---|
| `db-password` | `/etc/secret/catalog-db-secret/db-password` | `db-migration` init container + `backend` container |
| `admin-password` | `/etc/secret/catalog-secret/admin-password` | `backend` container |

With Vault, both secrets are stored in Vault KV and rendered into files by a Vault Agent sidecar — **no Podman secrets are created**.

**Vault KV paths:**

```bash
# DB password (same entry written by catalog configure for the DB component)
vault kv put secret/component/<db-comp-id>/credentials     db-password="<generated-password>"

# Admin password hash
vault kv put secret/application/<app-id>/catalog/config     admin-password="<password-hash>"
```

**Changes to `catalog.yaml.tmpl`:**

- Add a `vault-agent` sidecar container that authenticates via AppRole and renders both secrets into a shared `vault-rendered` volume using template blocks:

```hcl
# catalog-agent.hcl

template {
  contents    = "{{ with secret \"secret/data/component/<db-comp-id>/credentials\" }}{{ .Data.data.db-password }}{{ end }}"
  destination = "/vault/rendered/db-password"
}

template {
  contents    = "{{ with secret \"secret/data/application/<app-id>/catalog/config\" }}{{ .Data.data.admin-password }}{{ end }}"
  destination = "/vault/rendered/admin-password"
}
```

- Replace the two secret volume mounts in the `db-migration` init container and `backend` container with reads from the shared rendered volume:

```yaml
# db-migration init container — before
export DB_PASSWORD=$(cat /etc/secret/catalog-db-secret/db-password)

# db-migration init container — after
until [ -f /vault/rendered/db-password ]; do sleep 2; done
export DB_PASSWORD=$(cat /vault/rendered/db-password)
```

```yaml
# backend container — before
export ADMIN_PASSWORD=$(cat /etc/secret/catalog-secret/admin-password)
export DB_PASSWORD=$(cat /etc/secret/catalog-db-secret/db-password)

# backend container — after
until [ -f /vault/rendered/admin-password ] && [ -f /vault/rendered/db-password ]; do sleep 2; done
export ADMIN_PASSWORD=$(cat /vault/rendered/admin-password)
export DB_PASSWORD=$(cat /vault/rendered/db-password)
```

- Remove the `catalog-db-secret` and `catalog-secret` secret volumes and their `volumeMounts` from the pod spec.
- Remove `catalog-db-secret.yaml.tmpl` and `catalog-secret.yaml.tmpl` from the asset bundle — these secret manifests are no longer deployed.
- Remove the `ai-services.io/secret` label from the pod metadata.

---

### 8.3 OpenSearch Component

OpenSearch (where deployed as a catalog component) follows the same Vault Agent template rendering pattern as PostgreSQL. There is no dedicated secret manifest for OpenSearch credentials.

**At component deploy time**, Catalog generates the OpenSearch admin password and writes it into Vault KV:

```bash
vault kv put secret/component/<opensearch-comp-id>/credentials     admin-password="<generated-password>"
```

**OpenSearch pod changes:**

- Add a Vault Agent sidecar with a template block that renders the password to `/vault/rendered/admin-password`.
- Replace the existing secret volume mount (if any) with the shared rendered volume.
- The OpenSearch container reads the password from `/vault/rendered/admin-password` at startup.

**Services that need the OpenSearch password** (e.g., `similarity`, `digitize`) receive `read` access to `secret/data/component/<opensearch-comp-id>/*` in their policy — they call the Vault HTTP API directly using their own token. The OpenSearch pod itself never needs to authenticate to Vault beyond its own sidecar.

**Summary of all component credential changes:**

| Component | Secret removed | Vault KV path | Rendered file |
|---|---|---|---|
| PostgreSQL | `catalog-db-secret` | `secret/component/<db-comp-id>/credentials` → `db-password` | `/vault/rendered/db-password` |
| Catalog backend | `catalog-secret` | `secret/application/<app-id>/catalog/config` → `admin-password` | `/vault/rendered/admin-password` |
| OpenSearch | component secret | `secret/component/<opensearch-comp-id>/credentials` → `admin-password` | `/vault/rendered/admin-password` |

**Per-service isolation model:**

| Boundary | Mechanism |
|---|---|
| Policy scope | `chat-policy` grants `read` on its own `application/<app-id>/chat/*` path and `read` on each `component/<comp-id>/*` it consumes |
| Auth scope | `chat` AppRole is bound to `chat-policy` only |
| Secret mount | Only `chat-vault-credentials` is mounted into the chat pod |
| Token TTL | Tokens expire after 1 hour; Vault Agent renews them automatically |

---

## 9. Vault Agent Sidecar Pattern

Every service pod runs a **Vault Agent sidecar** container. The agent authenticates to Vault on behalf of the service, obtains a short-lived token, and writes it to a shared named volume (`PersistentVolumeClaim`). The service container reads the token from that volume and uses it directly against the Vault HTTP API; it never touches the raw AppRole credentials.

### 9.1 Agent Configuration (HCL)

`vendor/vault/vault-agent.hcl`:

```hcl
pid_file = "/tmp/vault-agent.pid"

vault {
  address = "http://vault:8200"
}

auto_auth {
  method "approle" {
    mount_path = "auth/approle"

    config = {
      # Points to the individual files created by the single secret mount
      role_id_file_path   = "/run/secrets/vault/role_id"
      secret_id_file_path = "/run/secrets/vault/secret_id"
    }
  }

  sink "file" {
    config = {
      path = "/vault/token"
    }
  }
}
```

The agent reads `role_id` and `secret_id` from the paths where the Podman secret is mounted (one key per file). It authenticates to Vault via AppRole and writes the resulting short-lived token to `/vault/token` on the shared named volume, where the application container reads it directly via the Vault HTTP API.

### 9.2 Service Pod Manifest

The following is an **example** pod manifest (`vendor/vault/service1.yaml`) illustrating the sidecar pattern. In practice, Container 2 is the actual application service being deployed — for example `chat`, `digitize`, `similarity`, or `summarize` — with its own image and secret path.

`vendor/vault/service1.yaml`:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: shared-token-pvc
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Mi
---
apiVersion: v1
kind: Pod
metadata:
  name: service1
spec:
  restartPolicy: Always

  volumes:
    # Shared named volume for the Vault token (PVC replaces emptyDir)
    - name: shared-token-dir
      persistentVolumeClaim:
        claimName: shared-token-pvc

    # Podman secret with role_id + secret_id
    - name: unified-vault-secret
      secret:
        secretName: service1-vault-secret

    # Host path for the agent.hcl config file
    - name: vault-agent-config
      hostPath:
        path: /root/mayuka/vault/agent/agent.hcl
        type: File

  containers:

    # -------------------------------------------------------------
    # CONTAINER 1: Vault Agent Sidecar
    # -------------------------------------------------------------
    - name: vault-agent
      image: icr.io/ppc64le-oss/vault-ppc64le:v1.14.8
      command:
        - vault
        - agent
        - -config=/etc/vault/agent.hcl
      volumeMounts:
        - name: shared-token-dir
          mountPath: /vault
        - name: vault-agent-config
          mountPath: /etc/vault/agent.hcl
        - name: unified-vault-secret
          mountPath: /run/secrets/vault
          readOnly: true

    # -------------------------------------------------------------
    # CONTAINER 2: Application Service (e.g. chat, digitize, similarity, summarize)
    # -------------------------------------------------------------
    - name: backend   # replaced with the actual service name at deploy time
      image: quay.io/jitesoft/alpine:latest   # replaced with the service image
      env:
        - name: VAULT_ADDR
          value: "http://vault:8200"
        - name: SECRET_PATH
          value: "v1/secret/data/application/<app-id>/<service>/config"
      command:
        - /bin/sh
        - -c
        - |
          until [ -f /vault/token ]; do
            echo "[*] Waiting for /vault/token..."
            sleep 2
          done

          echo "[+] Token found! Reading secret from Vault..."

          while true; do
            CURRENT_TOKEN=$(cat /vault/token)
            wget -qO- --header="X-Vault-Token: ${CURRENT_TOKEN}" "${VAULT_ADDR}/${SECRET_PATH}"
            echo ""
            sleep 10
          done
      volumeMounts:
        - name: shared-token-dir
          mountPath: /vault
          readOnly: true
```

**Volume access matrix:**

| Volume | vault-agent | application |
|---|---|---|
| `shared-token-dir` (`/vault`) | read + write | read-only |
| `unified-vault-secret` (`/run/secrets/vault`) | read-only | ❌ not mounted |
| `vault-agent-config` | read-only | ❌ not mounted |

**Note on shared volume:** the token volume is a `PersistentVolumeClaim` rather than an `emptyDir`. The token file therefore survives individual container restarts within the pod — the agent re-authenticates and overwrites it on startup, but the `backend` container does not block on a cold re-auth on every restart.

The application container **cannot** see the `role_id` or `secret_id` at any point. It reads the Vault token from `/vault/token` and passes it as the `X-Vault-Token` header when calling the Vault HTTP API directly.

### 9.3 Secret Object

`vendor/vault/service1-vault-secret.yaml`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: service1-vault-secret
type: Opaque
stringData:
  role_id:   "<static-role-id>"
  secret_id: "<static-secret-id>"
```

This object is created by the Catalog configure step for each service. Podman stores it securely on the host; it is mounted into the pod at `/run/secrets/vault/role_id` and `/run/secrets/vault/secret_id` (one file per key).

---

## 10. Unseal Architecture

Vault's seal mechanism protects the encryption keys that guard all stored secrets. Every time the Vault process restarts — including after a **VM reboot** — it starts in a **sealed** state and cannot serve any requests until it is unsealed.

**This process is fully automatic.** The `vault-bootstrap` pod is responsible for unsealing Vault without any operator intervention. It reads the unseal key from the `vault-unseal-key` Podman secret, which was stored during `catalog configure`, and calls `vault operator unseal` internally. No manual step is required after a reboot.

```
VM Reboot — automatic unseal sequence
──────────────────────────────────────

VM Boot
  └── podman.service
        └── podman-restart.service
              ├── vault pod starts (sealed — data intact but inaccessible)
              └── vault-bootstrap pod starts (restartPolicy: OnFailure)
                    ├── Wait: vault status (retries until Vault API responds)
                    ├── Check: initialized = true AND sealed = true
                    └── Execute: vault operator unseal $(cat /run/secrets/vault-unseal-key)
                          └── Vault unsealed — all service pods can authenticate
```

**Why the key never leaves the container:**

The bootstrap pod mounts the Podman secret at `/run/secrets/vault-unseal-key`. The `vault operator unseal` command is executed *inside* the bootstrap container. The unseal key is passed via command substitution (`$(cat ...)`) directly to the Vault CLI — it is never written to any host file, log, or environment variable outside the container.

**Bootstrap pod state machine:**

| Vault state on startup | Bootstrap action | Exit code |
|---|---|---|
| Not yet initialized | Log and exit — `catalog configure` has not run yet | 1 (retried by `OnFailure`) |
| Initialized, already unsealed | Log and exit — nothing to do | 0 |
| Initialized, sealed | Read key → `vault operator unseal` → exit | 0 |

Once the bootstrap pod exits 0, it does not run again until the next reboot triggers `podman-restart.service` to start it afresh.

---

## 11. Key Design Decisions

### 1. Single-shard Unseal (`-key-shares=1 -key-threshold=1`)

For a single-node, single-operator environment this is the appropriate configuration. It means one key unlocks Vault, which simplifies the bootstrap flow. A multi-operator setup can use Shamir with `key-shares=5 -key-threshold=3`; the bootstrap pod pattern is unchanged.

### 2. Root Token is Revoked Immediately

The initial root token has no time limit; holding it is a risk. It is revoked after the configure phase completes. All ongoing Catalog operations are performed with the `catalog-admin` AppRole token, which has a TTL and is renewed by the agent.

### 3. One AppRole Per Service

Each deployed service gets its own AppRole and its own narrow policy. This enforces least-privilege at the Vault level, independently of any container-level isolation. Even if a service is compromised, it can only read its own secrets.

### 4. Credentials Never in Pod Environment Variables

No secret value appears in `env:` blocks of any pod manifest. Values flow exclusively through:
- Podman secrets → container `/run/secrets` mount (for `role_id`/`secret_id`).
- Vault token on shared memory volume (for the service's running credential access).

### 5. Per-Service AppRole Credentials Stored as Podman Secrets

For every service that Catalog provisions, the generated `role_id` and `secret_id` are stored together in a dedicated Podman secret named `<service>-vault-credentials`. The secret object holds both values as distinct keys:

```yaml
# Example: service1-vault-credentials
apiVersion: v1
kind: Secret
metadata:
  name: service1-vault-credentials
type: Opaque
stringData:
  role_id:   "d8e9e409-f4ab-2c8e-efa6-5cfe9fd16084"
  secret_id: "c5084c45-871d-e543-bb73-1c0c10c4e0e7"
```

Podman mounts both keys as individual files under `/run/secrets/vault/` inside the Vault Agent sidecar:

| File path inside container | Content |
|---|---|
| `/run/secrets/vault/role_id` | The AppRole `role_id` (static; never changes per service) |
| `/run/secrets/vault/secret_id` | The AppRole `secret_id` (rotatable; bound to the role) |

The Vault Agent reads these two files via `role_id_file_path` and `secret_id_file_path` in `vault-agent.hcl`. The application container does **not** mount this secret — it only receives the short-lived token written to the shared memory volume.

This means:
- Every service has exactly one Podman secret holding its Vault identity.
- Rotating credentials means updating the Podman secret and restarting the pod — no manifest change is required.
- The `role_id` (static) and `secret_id` (rotatable) are co-located to keep provisioning atomic: a single `podman secret create` call covers both.

### 6. In-memory Volume for Token (`emptyDir.medium: Memory`)

The token file is written to a `tmpfs` mount. It does not touch the host filesystem. If the pod is killed and restarted, the agent re-authenticates and writes a fresh token — there is no stale credential on disk.

### 7. Bootstrap Pod `restartPolicy: OnFailure`

If the bootstrap pod starts before the Vault pod is ready, it exits non-zero and is retried by Podman. Once it successfully unseals Vault it exits 0 and will not run again in the same boot cycle.

### 8. File Backend (Not Consul or etcd)

The platform is a single-host deployment. A Raft/file storage backend is operationally simpler, has no external dependencies, and is fully supported by Vault for production use in single-node configurations.

---

## 12. Security Considerations

| Concern | Mitigation |
|---|---|
| Unseal key at rest | Stored exclusively as a Podman secret; never written to disk by the bootstrap pod |
| Root token exposure | Revoked immediately after configure; regeneration requires the unseal key |
| Secret sprawl | All secrets stored in one place (Vault KV); nothing in env vars or manifests |
| Service over-privilege | Each service policy is scoped to `secret/data/<service>/*` only |
| Token TTL | Per-service tokens expire in 1 hour; Vault Agent renews silently |
| Audit logging | Enable Vault audit log to capture every read/write operation: `vault audit enable file file_path=/vault/logs/audit.log` |
| Network exposure | Vault listens on `127.0.0.1:8200`; not exposed outside the host |
| TLS | Disabled for initial implementation; see [Section 14](#14-future-considerations) |

### Podman Secret Lifecycle

Podman secrets are stored encrypted by the container engine on the host filesystem. They are:

- **Not** exported by `podman pod inspect` or `podman ps`.
- Only accessible to pods that explicitly mount them via the `secrets:` volume stanza.
- Created with `podman secret create` and managed via `podman secret ls / rm`.

### What to Rotate

| Credential | Rotation command | Impact |
|---|---|---|
| Service `secret_id` | `vault write -f auth/approle/role/<service>/secret-id` + update Podman secret | Restart service pod |
| Catalog `secret_id` | Same as above for `catalog` role | Restart Catalog API server |
| Unseal key | `vault operator rekey` | Update Podman secret; no pod restart needed |
| Root token | `vault operator generate-root` (requires unseal key) | Used only for break-glass recovery |

---

## 13. Disaster Recovery

### What to Back Up

| Artifact | Location | Notes |
|---|---|---|
| Vault data directory | `/opt/catalog/vault/data` | Contains all secrets, policies, AppRoles. Back up regularly. |
| Unseal key | Offline secure storage (e.g., printed, encrypted file in a separate location) | **Critical.** Without this, the Vault data is unrecoverable. |

### What Does NOT Need Separate Backup

| Artifact | Why |
|---|---|
| Root token | Revoked; regenerate via `vault operator generate-root` if needed |
| Policies | Stored inside the Vault data directory |
| KV secrets | Stored inside the Vault data directory |
| AppRole definitions | Stored inside the Vault data directory |

### Recovery Procedure

```bash
# 1. Restore the data directory from backup onto the host
rsync -a /backup/vault/data/ /opt/catalog/vault/data/

# 2. Start the vault pod
podman kube play vault.yaml

# 3. Run the bootstrap pod (reads from the Podman secret as usual)
podman kube play vault-bootrap.yaml

# 4. Verify
vault status
vault kv list secret/
```

If the Podman secret `vault-unseal-key` was also lost (e.g., complete host rebuild):

```bash
# Re-create the Podman secret from the offline backup copy of the key
echo "$UNSEAL_KEY" | podman secret create vault-unseal-key -

# Then follow steps 2–4 above
```

---

## 14. Future Considerations

### TLS on the Vault Listener

The current configuration disables TLS because all traffic is intra-host. If Vault is ever exposed across a network boundary (e.g., a multi-host Catalog cluster), TLS must be enabled. Vault has first-class PKI support via its own PKI secrets engine:

```hcl
listener "tcp" {
  address       = "0.0.0.0:8200"
  tls_cert_file = "/vault/tls/vault.crt"
  tls_key_file  = "/vault/tls/vault.key"
}
```

The PKI engine can issue and auto-rotate certificates for both the Vault server and client agents.

### Vault Agent Template Rendering

Instead of passing a raw token to the service container, Vault Agent can render config files from templates:

```hcl
template {
  source      = "/etc/vault/templates/config.tmpl"
  destination = "/etc/app/config.json"
}
```

This allows secrets to be injected as file contents rather than as a token, so the application code requires no Vault SDK at all.

### Dynamic Secrets

Vault's database secrets engine can issue short-lived, per-connection database credentials instead of long-lived passwords. Enabling this for the Catalog PostgreSQL instance would eliminate static `db_password` values entirely:

```bash
vault secrets enable database
vault write database/config/catalog \
    plugin_name=postgresql-database-plugin \
    connection_url="postgresql://{{username}}:{{password}}@localhost:5432/catalog" \
    allowed_roles="catalog-db-role"
```

### High Availability (HA) Vault

For production-grade deployments, Vault can be run in an HA configuration using the integrated Raft storage backend across three nodes. The `vault.hcl` would switch to:

```hcl
storage "raft" {
  path    = "/vault/data"
  node_id = "node1"
}

cluster_addr = "http://node1:8201"
api_addr     = "http://node1:8200"
```

### Web UI

The UI is disabled in the proposed configuration (`ui = false`). It can be enabled for debugging or administrative purposes by setting `ui = true` and accessing `http://127.0.0.1:8200/ui`. This should be disabled in production.

### Secret Versioning and Rollback

KV v2 already stores every version of a secret. The Catalog API server can expose a `catalog secret rollback` command that wraps `vault kv rollback` to restore a previous secret version without requiring a full redeployment.
