# 📌 MongoDB Replica Set

Un **Replica Set** in MongoDB è un gruppo di istanze (server) che mantengono gli stessi dati.  
Serve per garantire **alta disponibilità**, **ridondanza** e **tolleranza ai guasti**.

---

## 🧩 Componenti principali

- **Primary**
  - Riceve tutte le **scritture** (insert, update, delete).
  - Replica i dati verso i **Secondary**.
  - Solo un nodo può essere Primary alla volta.

- **Secondary**
  - Copia i dati dal Primary tramite l’**Oplog** (operation log).
  - Può essere usato per query in sola lettura.
  - In caso di guasto del Primary, uno di essi viene promosso a nuovo Primary.

- **Arbiter (opzionale)**
  - Non conserva dati.
  - Serve solo per partecipare alle **elezioni** e prevenire pareggi.
  - Utile quando si ha un numero pari di nodi.

---

## 🔄 Come funziona la replica

1. Il **Primary** scrive tutte le operazioni nell’**Oplog**.
2. I **Secondary** leggono l’Oplog e applicano le modifiche ai propri dati.
3. In caso di crash del Primary:
   - I nodi rimanenti rilevano il problema.
   - Viene avviata un’**elezione**.
   - Uno dei Secondary diventa **nuovo Primary**.

---

## ⚡ Processo di failover

- **Primary cade** ➝ i Secondary se ne accorgono.
- Parte un’elezione ➝ i nodi votano chi deve diventare Primary.
- In pochi secondi il cluster torna operativo con un nuovo Primary.

---

## 🎯 Vantaggi dei Replica Set

- ✅ Alta disponibilità: il sistema resta attivo anche se un nodo cade.  
- ✅ Ridondanza: i dati sono replicati su più nodi.  
- ✅ Scalabilità in lettura: query di sola lettura possono essere distribuite ai Secondary.  
- ✅ Failover automatico: elezione rapida di un nuovo Primary.  

---

## 📖 Esempio pratico

Replica Set con 3 nodi:

- `mongo1` ➝ Primary  
- `mongo2` ➝ Secondary  
- `mongo3` ➝ Secondary  

### Flusso operativo
- Scrivi su `mongo1`.
- `mongo2` e `mongo3` replicano i dati.
- Se `mongo1` crasha, dopo pochi secondi `mongo2` diventa Primary.

---

## 📊 Schema semplificato

```mermaid
graph TD;
    Primary["🟢 Primary"] --> Secondary1["🔵 Secondary 1"];
    Primary["🟢 Primary"] --> Secondary2["🔵 Secondary 2"];
    Arbiter["⚪ Arbiter (opzionale)"] -. Vota .-> Primary;
    Arbiter -. Vota .-> Secondary1;
    Arbiter -. Vota .-> Secondary2;
# 🚀 MongoDB Sharded Cluster con Docker Compose

Questo progetto configura un **cluster MongoDB sharded** utilizzando **Docker Compose**.  
La configurazione include:

- **3 Config Servers** (Replica Set `cfgrs`) → archiviano i metadati del cluster  
- **2 Shard Replica Sets** (`rs1` e `rs2`) → archiviano i dati  
- **1 Mongos Router** → punto di accesso per i client  

---

## 📂 Struttura del Cluster

```
                  +-------------------+
                  |      Mongos       |
                  | (porta 27017)     |
                  +---------+---------+
                            |
                            v
             +--------------+----------------+
             |   Config Servers (cfgrs)     |
             |  (porta 27019, Replica Set)  |
             | configsvr1 - configsvr2 - configsvr3 |
             +--------------+----------------+
                            |
       -----------------------------------------------
       |                                             |
       v                                             v
+-------------+                           +----------------+
|   Shard 1   |                           |    Shard 2     |
| ReplicaSet  |                           |  ReplicaSet    |
| rs1         |                           | rs2            |
| shard1a,b   |                           | shard2a,b      |
+-------------+                           +----------------+
```

---

## ⚙️ Servizi Principali

### 🔹 Config Servers
- `configsvr1`, `configsvr2`, `configsvr3`
- Ruolo: mantengono i **metadati del cluster**
- Comando avvio:
  ```bash
  mongod --configsvr --replSet cfgrs --bind_ip_all
  ```
- Porta: `27019`

---

### 🔹 Shards
- Ogni shard è un **Replica Set**
- **Shard 1** → `rs1`: nodi `shard1a`, `shard1b`  
- **Shard 2** → `rs2`: nodi `shard2a`, `shard2b`  
- Comando avvio:
  ```bash
  mongod --shardsvr --replSet <nomeReplicaSet> --bind_ip_all
  ```

---

### 🔹 Mongos Router
- Servizio: `mongos`
- Ruolo: router per le query, unico **entry point** per il client
- Comando avvio:
  ```bash
  mongos --configdb cfgrs/configsvr1:27019,configsvr2:27019,configsvr3:27019 --bind_ip_all
  ```
- Porta: `27017` (quella a cui ti colleghi normalmente)

---

## ▶️ Avvio del Cluster

1. Lancia i container:
   ```bash
   docker-compose up -d
   ```

2. Inizializza i **Replica Set dei Config Server**:
   ```bash
   docker exec -it configsvr1 mongosh
   rs.initiate({
     _id: "cfgrs",
     configsvr: true,
     members: [
       { _id: 0, host: "configsvr1:27019" },
       { _id: 1, host: "configsvr2:27019" },
       { _id: 2, host: "configsvr3:27019" }
     ]
   })
   ```

3. Inizializza i **Replica Set degli Shard**:
   ```bash
   # Shard 1
   docker exec -it shard1a mongosh --port 27018
   rs.initiate({
     _id: "rs1",
     members: [
       { _id: 0, host: "shard1a:27018" },
       { _id: 1, host: "shard1b:27018" }
     ]
   })

   # Shard 2
   docker exec -it shard2a mongosh --port 27018
   rs.initiate({
     _id: "rs2",
     members: [
       { _id: 0, host: "shard2a:27018" },
       { _id: 1, host: "shard2b:27018" }
     ]
   })
   ```

4. Configura gli shard dal **mongos**:
   ```bash
   docker exec -it mongos mongosh
   sh.addShard("rs1/shard1a:27018,shard1b:27018")
   sh.addShard("rs2/shard2a:27018,shard2b:27018")
   ```

---

## 🛠️ Connessione

Collegati al cluster tramite il router:
```bash
mongosh --port 27017
```

