#!/bin/bash
echo "🚀 Avvio Sharded Cluster Test..."
# Avvia i container
docker compose -f ShredMongoDB.yaml down -v
docker compose -f ShredMongoDB.yaml up -d
echo "⏳ Attendo che i container siano pronti..."
sleep 30

echo "🔧 Inizializzazione Config Server..."
docker exec configsvr mongosh --port 27019 --eval "
rs.initiate({
  _id: 'cfgrs',
  members: [{ _id: 0, host: 'configsvr:27019' }]
})
"

echo "⏳ Attendo inizializzazione config server..."
sleep 10

echo "🔧 Inizializzazione Shard 1..."
docker exec shard1 mongosh --port 27018 --eval "
rs.initiate({
  _id: 'rs1',
  members: [{ _id: 0, host: 'shard1:27018' }]
})
"

echo "🔧 Inizializzazione Shard 2..."
docker exec shard2 mongosh --port 27020 --eval "
rs.initiate({
  _id: 'rs2',
  members: [{ _id: 0, host: 'shard2:27020' }]
})
"

echo "⏳ Attendo inizializzazione shard..."
sleep 15

echo "🔗 Aggiunta shard al cluster..."
docker exec mongos mongosh --eval "
sh.addShard('rs1/shard1:27018');
sh.addShard('rs2/shard2:27020');
"

echo "✅ Cluster MongoDB pronto!"
echo "📍 Connettiti a: mongodb://localhost:27017"

echo "🔍 Stato del cluster:"
docker exec mongos mongosh --eval "sh.status()"
