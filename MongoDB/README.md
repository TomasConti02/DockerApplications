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
