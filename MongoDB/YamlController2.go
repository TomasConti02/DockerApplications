# =========================
# Config Servers
# =========================
apiVersion: v1
kind: Service
metadata:
  name: {{ .Name }}-configsvr-svc
  namespace: {{ .Namespace }}
  labels:
    app: {{ .Name }}-configsvr
spec:
  clusterIP: None
  selector:
    app: {{ .Name }}-configsvr
  ports:
    - port: 27017
      name: mongodb
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: {{ .Name }}-configsvr
  namespace: {{ .Namespace }}
  labels:
    app: {{ .Name }}-configsvr
spec:
  serviceName: {{ .Name }}-configsvr-svc
  replicas: {{ .ConfigReplicas }}
  selector:
    matchLabels:
      app: {{ .Name }}-configsvr
  template:
    metadata:
      labels:
        app: {{ .Name }}-configsvr
        component: configsvr
    spec:
      terminationGracePeriodSeconds: 10
      containers:
      - name: mongod
        image: {{ .MongoImage }}
        command:
          - mongod
          - "--replSet"
          - "configRepl"
          - "--bind_ip_all"
          - "--port"
          - "27017"
          - "--dbpath"
          - "/data/db"
          - "--configsvr"
        ports:
        - containerPort: 27017
          name: mongodb
        resources:
          requests:
            memory: "1Gi"
            cpu: "500m"
          limits:
            memory: "2Gi"
            cpu: "1000m"
        volumeMounts:
        - name: mongo-data
          mountPath: /data/db
        readinessProbe:
          exec:
            command:
            - mongosh
            - --eval
            - "db.adminCommand('ping')"
          initialDelaySeconds: 30
          periodSeconds: 10
          timeoutSeconds: 5
  volumeClaimTemplates:
  - metadata:
      name: mongo-data
    spec:
      accessModes: ["ReadWriteOnce"]
      storageClassName: {{ .StorageClassName }}
      resources:
        requests:
          storage: {{ .StorageSize }}

# =========================
# Shards
# =========================
{{- range $i := until .Shards }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ $.Name }}-shard-{{ $i }}-svc
  namespace: {{ $.Namespace }}
  labels:
    app: {{ $.Name }}-shard
    shard-index: "{{ $i }}"
spec:
  clusterIP: None
  selector:
    app: {{ $.Name }}-shard
    shard-index: "{{ $i }}"
  ports:
    - port: 27017
      name: mongodb
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: {{ $.Name }}-shard-{{ $i }}
  namespace: {{ $.Namespace }}
  labels:
    app: {{ $.Name }}-shard
    shard-index: "{{ $i }}"
spec:
  serviceName: {{ $.Name }}-shard-{{ $i }}-svc
  replicas: {{ $.Replicas }}
  selector:
    matchLabels:
      app: {{ $.Name }}-shard
      shard-index: "{{ $i }}"
  template:
    metadata:
      labels:
        app: {{ $.Name }}-shard
        shard-index: "{{ $i }}"
        component: shard
    spec:
      terminationGracePeriodSeconds: 10
      containers:
      - name: mongod
        image: {{ $.MongoImage }}
        command:
          - mongod
          - "--replSet"
          - "shard{{ $i }}"
          - "--bind_ip_all"
          - "--port"
          - "27017"
          - "--dbpath"
          - "/data/db"
          - "--shardsvr"
        ports:
        - containerPort: 27017
          name: mongodb
        resources:
          requests:
            memory: "1Gi"
            cpu: "500m"
          limits:
            memory: "2Gi"
            cpu: "1000m"
        volumeMounts:
        - name: mongo-data
          mountPath: /data/db
        readinessProbe:
          exec:
            command:
            - mongosh
            - --eval
            - "db.adminCommand('ping')"
          initialDelaySeconds: 30
          periodSeconds: 10
          timeoutSeconds: 5
  volumeClaimTemplates:
  - metadata:
      name: mongo-data
    spec:
      accessModes: ["ReadWriteOnce"]
      storageClassName: {{ $.StorageClassName }}
      resources:
        requests:
          storage: {{ $.StorageSize }}
{{- end }}

# =========================
# Mongos Router
# =========================
---
apiVersion: v1
kind: Service
metadata:
  name: {{ .Name }}-mongos
  namespace: {{ .Namespace }}
  labels:
    app: {{ .Name }}-mongos
spec:
  selector:
    app: {{ .Name }}-mongos
  ports:
    - port: 27017
      targetPort: 27017
      protocol: TCP
  type: ClusterIP
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Name }}-mongos
  namespace: {{ .Namespace }}
  labels:
    app: {{ .Name }}-mongos
spec:
  replicas: 1
  selector:
    matchLabels:
      app: {{ .Name }}-mongos
  template:
    metadata:
      labels:
        app: {{ .Name }}-mongos
        component: mongos
    spec:
      containers:
      - name: mongos
        image: {{ .MongoImage }}
        command:
          - mongos
          - "--configdb"
          - "configRepl/{{ .Name }}-configsvr-0.{{ .Name }}-configsvr-svc.{{ .Namespace }}.svc.cluster.local:27017{{- range $i := until (sub .ConfigReplicas 1) }},{{ $.Name }}-configsvr-{{ add1 $i }}.{{ $.Name }}-configsvr-svc.{{ $.Namespace }}.svc.cluster.local:27017{{- end }}"
          - "--bind_ip_all"
        ports:
        - containerPort: 27017
        resources:
          requests:
            memory: "512Mi"
            cpu: "250m"
          limits:
            memory: "1Gi"
            cpu: "500m"
        readinessProbe:
          exec:
            command:
            - mongosh
            - --eval
            - "db.adminCommand('ping')"
          initialDelaySeconds: 15
          periodSeconds: 5
          timeoutSeconds: 5
