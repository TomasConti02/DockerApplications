#!/bin/bash
echo "🚀 Avvio Sharded Cluster con Fault Tolerance..."
# Avvia i container
docker compose -f ShredMongoDB.yaml down -v
docker compose -f ShredMongoDB.yaml up -d
echo "⏳ Attendo che i container siano pronti..."
sleep 30

echo "🔧 Inizializzazione Config Server Replica Set..."
docker exec configsvr1 mongosh --port 27019 --eval "
rs.initiate({
  _id: 'cfgrs',
  configsvr: true,
  members: [
    { _id: 0, host: 'configsvr1:27019' },
    { _id: 1, host: 'configsvr2:27019' },
    { _id: 2, host: 'configsvr3:27019' }
  ]
})
"

echo "⏳ Attendo inizializzazione config server..."
sleep 15

echo "🔧 Inizializzazione Shard 1 Replica Set..."
docker exec shard1a mongosh --port 27018 --eval "
rs.initiate({
  _id: 'rs1',
  members: [
    { _id: 0, host: 'shard1a:27018', priority: 2 },
    { _id: 1, host: 'shard1b:27018', priority: 1 },
    { _id: 2, host: 'shard1c:27018', priority: 1 }
  ]
})
"

echo "🔧 Inizializzazione Shard 2 Replica Set..."
docker exec shard2a mongosh --port 27020 --eval "
rs.initiate({
  _id: 'rs2',
  members: [
    { _id: 0, host: 'shard2a:27020', priority: 2 },
    { _id: 1, host: 'shard2b:27020', priority: 1 },
    { _id: 2, host: 'shard2c:27020', priority: 1 }
  ]
})
"

echo "⏳ Attendo inizializzazione shard replica sets..."
sleep 20

echo "🔗 Aggiunta shard al cluster..."
docker exec mongos mongosh --eval "
sh.addShard('rs1/shard1a:27018,shard1b:27018,shard1c:27018');
sh.addShard('rs2/shard2a:27020,shard2b:27020,shard2c:27020');
"

echo "✅ Cluster MongoDB con Fault Tolerance pronto!"
echo "📍 Connettiti a: mongodb://localhost:27017"

echo "🔍 Stato del cluster:"
docker exec mongos mongosh --eval "sh.status()"
