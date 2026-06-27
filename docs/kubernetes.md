# ☸️ Kubernetes deployment guide

[← Back to README](../README.md) · Related: [Deployment & storage](deployment.md) · [Configuration reference](configuration.md) · [Security model](security.md)

This guide covers deploying escrow on Kubernetes with a central S3 cache backend, a shared
PersistentVolume for allow/block lists, and the necessary RBAC, Service, and Ingress resources.

---

## Quick start

Create a ConfigMap for your `escrow.toml`, a Deployment, and a Service:

```yaml
# escrow-config.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: escrow-config
  labels:
    app.kubernetes.io/name: escrow
data:
  escrow.toml: |
    [server]
      host = "0.0.0.0"
      port = 7888

    [dashboard]
      enabled = true
      username = "admin"
      # password and secret are injected via env vars; see Deployment below.
      path = "/dashboard"

    [storage]
      cache = "s3"

    [cache]
      s3_bucket = "escrow-cache"
      s3_region = "us-east-1"
      s3_endpoint = "https://minio.example.com"
      # s3_access_key_id and s3_secret_access_key via env vars

    [policy]
      min_days = 7

    [eventlog]
      path = "/data/escrow-events.jsonl"

    allowlist_path = "/data/escrow-allowlist.json"
    blocklist_path  = "/data/escrow-blocklist.json"
```

```yaml
# escrow-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: escrow
  labels:
    app.kubernetes.io/name: escrow
spec:
  replicas: 1  # escrow is single-node (allow/block lists are local JSON files)
  selector:
    matchLabels:
      app.kubernetes.io/name: escrow
  template:
    metadata:
      labels:
        app.kubernetes.io/name: escrow
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        fsGroup: 1000
      containers:
        - name: escrow
          image: ghcr.io/jverhoeks/escrow:latest
          ports:
            - containerPort: 7888
              name: http
              protocol: TCP
          env:
            - name: ESCROW_DASHBOARD_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: escrow-secret
                  key: dashboard_password
            - name: ESCROW_DASHBOARD_SECRET
              valueFrom:
                secretKeyRef:
                  name: escrow-secret
                  key: dashboard_secret
            - name: ESCROW_CACHE_S3_ACCESS_KEY_ID
              valueFrom:
                secretKeyRef:
                  name: escrow-secret
                  key: s3_access_key_id
            - name: ESCROW_CACHE_S3_SECRET_ACCESS_KEY
              valueFrom:
                secretKeyRef:
                  name: escrow-secret
                  key: s3_secret_access_key
          volumeMounts:
            - name: config
              mountPath: /etc/escrow
              readOnly: true
            - name: data
              mountPath: /data
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 5
            periodSeconds: 15
          readinessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 3
            periodSeconds: 10
          resources:
            requests:
              cpu: "250m"
              memory: "256Mi"
            limits:
              cpu: "1"
              memory: "512Mi"
      volumes:
        - name: config
          configMap:
            name: escrow-config
        - name: data
          persistentVolumeClaim:
            claimName: escrow-data
```

```yaml
# escrow-service.yaml
apiVersion: v1
kind: Service
metadata:
  name: escrow
  labels:
    app.kubernetes.io/name: escrow
spec:
  type: ClusterIP
  ports:
    - port: 7888
      targetPort: http
      name: http
      protocol: TCP
  selector:
    app.kubernetes.io/name: escrow
```

```yaml
# escrow-ingress.yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: escrow
  annotations:
    nginx.ingress.kubernetes.io/proxy-read-timeout: "120"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "120"
spec:
  rules:
    - host: escrow.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: escrow
                port:
                  name: http
```

> The long proxy timeouts prevent nginx from killing streaming SSE connections and slow
> artifact downloads. Adjust down if your environment has stricter timeout requirements.

---

## Secrets

Create a Secret for credentials that must never live in the ConfigMap:

```bash
kubectl create secret generic escrow-secret \
  --from-literal=dashboard_password="$(openssl rand -base64 24)" \
  --from-literal=dashboard_secret="$(openssl rand -hex 32)" \
  --from-literal=s3_access_key_id="..." \
  --from-literal=s3_secret_access_key="..."
```

Environment variables take precedence over config-file values, so you can set
`dashboard_password`, `dashboard_secret`, and S3 credentials through the Secret
without ever writing them to disk in the ConfigMap.

---

## Persistent storage

Allow/block lists and the optional event-log JSONL file need a writable volume.
An S3 cache backend means blobs live in S3, not on the local disk, so the volume
only needs to hold small JSON files (a few KB):

```yaml
# escrow-pvc.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: escrow-data
  labels:
    app.kubernetes.io/name: escrow
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
```

If you use the disk cache backend instead of S3, size the volume to
`cache.max_size_gb + 20%` headroom.

---

## Prometheus monitoring

The built-in `/metrics` endpoint exports Prometheus-compatible metrics. Add a
`ServiceMonitor` if you use the Prometheus Operator:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: escrow
  labels:
    app.kubernetes.io/name: escrow
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: escrow
  endpoints:
    - port: http
      path: /metrics
      interval: 15s
```

---

## Horizontal scaling

**escrow is currently single-node.** Allow/block lists and the event log are local
JSON files — they are not replicated. Two replicas with different data would see
and log different events. If you need HA:

1. Use S3 as the cache backend (shared across all replicas).
2. Mount the allow/block list directory on a shared ReadWriteMany volume (NFS,
   EFS, Longhorn).
3. Accept that the event log will be split per-replica.
4. Set `replicas: 1` unless you have the shared volume in place.

Horizontal scale-out across multiple replicas with shared state is on the roadmap.

---

## Upgrading

Pull the latest image and let Kubernetes roll the Deployment:

```bash
kubectl set image deployment/escrow escrow=ghcr.io/jverhoeks/escrow:latest
kubectl rollout status deployment/escrow
```

If the new image changes the config format, update the ConfigMap before or after
the rollout — escrow validates its config at startup and exits with a fatal error
if the config is invalid.

---

## Related

- [Deployment, storage & alerts](deployment.md) — TLS, systemd, S3 setup, webhooks
- [Configuration reference](configuration.md) — all `escrow.toml` keys
- [Security model](security.md) — dashboard hardening, threat coverage
- [Docker & `docker build` protection](docker.md) — egress proxy for container builds
