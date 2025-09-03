#!/bin/bash
# start-cluster.sh

echo "🚀 Avvio cluster MongoDB Replica Set..."
docker compose -f ReplicaSetMongoDB.yaml down -v
docker compose -f ReplicaSetMongoDB.yaml up -d
echo "⏳ Attendo configurazione automatica..."
sleep 20
echo "🔍 Verifico stato replica set..."
docker exec -it mongo1 mongosh --eval "
try {
    const status = rs.status();
    print('✅ REPLICA SET CONFIGURATO CORRETTAMENTE');
    print('Primary:', rs.isMaster().primary);
    print('Stato:', status.members.map(m => m.name + ': ' + m.stateStr).join(', '));
} catch (e) {
    print('❌ Errore:', e.message);
    print('Provo a inizializzare...');
    rs.initiate({
        _id: 'rs0',
        members: [
            { _id: 0, host: 'mongo1:27017' },
            { _id: 1, host: 'mongo2:27017' },
            { _id: 2, host: 'mongo3:27017' }
        ]
    });
    sleep(10000);
    print('✅ Replica set inizializzato!');
}
"
echo "🎉 Cluster pronto! Connetti con:"
echo "Primary: mongosh localhost:27017"
echo "Secondary: mongosh localhost:27018"
