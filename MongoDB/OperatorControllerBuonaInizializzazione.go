package controller

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"strings"
	"text/template"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	webappv1 "my.domain/operator/api/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

//go:embed mongo-statefulset.yaml
var mongoTemplate []byte

type OperatorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *OperatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Recupero la CR
	operator := &webappv1.Operator{}
	if err := r.Get(ctx, req.NamespacedName, operator); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ---- STEP 0: Pulisci le risorse obsolete ----
	if err := r.cleanupObsoleteResources(ctx, operator); err != nil {
		logger.Error(err, "Errore nella pulizia delle risorse obsolete")
		return ctrl.Result{}, err
	}

	// ---- STEP 1: Renderizzo il template YAML ----
	tmpl, err := template.New("mongo-cluster").Funcs(template.FuncMap{
		"add1": func(i int) int { return i + 1 },
		"sub":  func(a, b int) int { return a - b },
		"until": func(n int) []int {
			var result []int
			for i := 0; i < n; i++ {
				result = append(result, i)
			}
			return result
		},
	}).Parse(string(mongoTemplate))
	if err != nil {
		logger.Error(err, "Errore parsing template YAML")
		return ctrl.Result{}, err
	}

	var buf bytes.Buffer
	data := map[string]interface{}{
		"Name":           operator.Name,
		"Namespace":      operator.Namespace,
		"ConfigReplicas": operator.Spec.ConfigReplicas,
		"Shards":         operator.Spec.Shards,
		"Replicas":       operator.Spec.Replicas,
		"MongoImage":     operator.Spec.MongoImage,
		"StorageSize":    operator.Spec.StorageSize,
		"StorageClassName": func() string {
			if operator.Spec.StorageClassName != nil {
				return *operator.Spec.StorageClassName
			}
			return "standard"
		}(),
	}

	if err := tmpl.Execute(&buf, data); err != nil {
		logger.Error(err, "Errore eseguendo il template YAML")
		return ctrl.Result{}, err
	}

	// ---- STEP 2: Processo le risorse YAML ----
	decoder := yaml.NewYAMLToJSONDecoder(&buf)
	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(obj); err != nil {
			if err == io.EOF {
				break
			}
			logger.Error(err, "Errore decodificando YAML")
			return ctrl.Result{}, err
		}

		gvk := obj.GroupVersionKind()
		typedObj, err := r.Scheme.New(gvk)
		if err != nil {
			logger.Error(err, "Errore creando oggetto dallo schema", "gvk", gvk)
			continue
		}

		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, typedObj); err != nil {
			logger.Error(err, "Errore convertendo unstructured", "gvk", gvk)
			continue
		}

		if metaObj, ok := typedObj.(metav1.Object); ok {
			metaObj.SetNamespace(operator.Namespace)
			if err := controllerutil.SetControllerReference(operator, metaObj, r.Scheme); err != nil {
				logger.Error(err, "Errore impostando owner reference", "gvk", gvk)
				continue
			}
		}

		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, typedObj.(client.Object), func() error {
			switch obj := typedObj.(type) {
			case *appsv1.StatefulSet:
				if *obj.Spec.Replicas != int32(operator.Spec.Replicas) {
					replicas := int32(operator.Spec.Replicas)
					obj.Spec.Replicas = &replicas
				}
				for i := range obj.Spec.Template.Spec.Containers {
					if obj.Spec.Template.Spec.Containers[i].Name == "mongod" {
						obj.Spec.Template.Spec.Containers[i].Image = operator.Spec.MongoImage
					}
				}
			case *appsv1.Deployment:
				for i := range obj.Spec.Template.Spec.Containers {
					if obj.Spec.Template.Spec.Containers[i].Name == "mongos" {
						obj.Spec.Template.Spec.Containers[i].Image = operator.Spec.MongoImage

						configDB := fmt.Sprintf("configRepl/%s-configsvr-0.%s-configsvr-svc.%s.svc.cluster.local:27017",
							operator.Name, operator.Name, operator.Namespace)
						for j := 1; j < operator.Spec.ConfigReplicas; j++ {
							configDB += fmt.Sprintf(",%s-configsvr-%d.%s-configsvr-svc.%s.svc.cluster.local:27017",
								operator.Name, j, operator.Name, operator.Namespace)
						}

						obj.Spec.Template.Spec.Containers[i].Command = []string{
							"mongos",
							"--configdb", configDB,
							"--bind_ip_all",
						}
					}
				}
			}
			return nil
		})
		if err != nil {
			logger.Error(err, "Errore in CreateOrUpdate", "gvk", gvk)
			continue
		} else {
			logger.Info("Risorsa creata o aggiornata", "kind", gvk.Kind, "name", getObjectName(typedObj))
		}
	}

	// ---- STEP 3: verifica readiness dei pod ----
	allReady, err := r.areAllPodsReady(ctx, operator.Namespace)
	if err != nil {
		logger.Error(err, "Errore verificando lo stato dei pod")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if allReady {
		// 1. Inizializza replica set per TUTTI gli shard (gestisce anche nuovi shard)
		if err := r.ensureShardReplicaSets(ctx, operator); err != nil {
			logger.Error(err, "Errore gestendo i replica set")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		// 2. Sincronizza tutti gli shard con mongos (aggiunge quelli mancanti, rimuove quelli in eccesso)
		if err := r.syncShardsWithMongos(ctx, operator); err != nil {
			logger.Error(err, "Errore sincronizzando shard con mongos")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		logger.Info("Cluster MongoDB completamente pronto e sincronizzato!")
		return ctrl.Result{}, nil
	}

	logger.Info("Alcuni pod non sono ancora ready, verifico di nuovo tra 10 secondi")
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}
func (r *OperatorReconciler) ensureShardReplicaSets(ctx context.Context, operator *webappv1.Operator) error {
	logger := log.FromContext(ctx)

	for shard := 0; shard < operator.Spec.Shards; shard++ {
		shardID := fmt.Sprintf("shard%d", shard)

		// Verifica se lo StatefulSet dello shard esiste ed è ready
		stsName := fmt.Sprintf("%s-shard%d", operator.Name, shard)
		sts := &appsv1.StatefulSet{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: operator.Namespace, Name: stsName}, sts); err != nil {
			if client.IgnoreNotFound(err) != nil {
				logger.Error(err, "Errore recuperando StatefulSet", "shard", shardID)
				return err
			}
			// StatefulSet non esiste ancora, skip
			logger.Info("StatefulSet non trovato, skipping", "shard", shardID)
			continue
		}

		// Verifica se lo StatefulSet è ready (tutte le repliche pronte)
		if sts.Status.ReadyReplicas != *sts.Spec.Replicas {
			logger.Info("StatefulSet non ancora ready", "shard", shardID, "ready", sts.Status.ReadyReplicas, "desired", *sts.Spec.Replicas)
			continue
		}

		// Inizializza il replica set se necessario
		if err := r.initShardReplicaSet(ctx, operator, shard); err != nil {
			logger.Error(err, "Errore inizializzando replica set", "shard", shardID)
			return err
		}

		logger.Info("Replica set verificato e pronto", "shard", shardID)
	}
	return nil
}
func (r *OperatorReconciler) initShardReplicaSet(ctx context.Context, operator *webappv1.Operator, shard int) error {
	logger := log.FromContext(ctx)
	shardID := fmt.Sprintf("shard%d", shard)

	// Connessione diretta al pod -0 dello shard
	directURI := fmt.Sprintf("mongodb://%s-shard%d-0.%s-shard%d-svc.%s.svc.cluster.local:27017/?directConnection=true",
		operator.Name, shard, operator.Name, shard, operator.Namespace)

	directClient, err := mongo.Connect(ctx, options.Client().ApplyURI(directURI))
	if err != nil {
		return fmt.Errorf("connessione diretta fallita: %w", err)
	}
	defer directClient.Disconnect(ctx)

	// Verifica se il replica set è già inizializzato e ha un PRIMARY
	var status map[string]interface{}
	err = directClient.Database("admin").RunCommand(ctx, map[string]interface{}{"replSetGetStatus": 1}).Decode(&status)
	if err == nil {
		// Verifica che ci sia un PRIMARY
		if hasPrimary, _ := isReplicaSetReady(status); hasPrimary {
			logger.Info("Replica set già inizializzato e pronto", "shard", shardID)
			return nil
		}
		logger.Info("Replica set inizializzato ma senza PRIMARY, ri-inizializzo", "shard", shardID)
	}

	// Configura i membri del replica set
	members := []map[string]interface{}{}
	for i := 0; i < operator.Spec.Replicas; i++ {
		host := fmt.Sprintf("%s-shard%d-%d.%s-shard%d-svc.%s.svc.cluster.local:27017",
			operator.Name, shard, i, operator.Name, shard, operator.Namespace)
		members = append(members, map[string]interface{}{"_id": i, "host": host})
	}

	rsConfig := map[string]interface{}{
		"_id":     shardID,
		"version": 1,
		"members": members,
	}

	// Inizializza il replica set
	if err := directClient.Database("admin").RunCommand(ctx,
		map[string]interface{}{"replSetInitiate": rsConfig}).Err(); err != nil {

		if strings.Contains(err.Error(), "already initialized") {
			logger.Info("Replica set già inizializzato", "shard", shardID)
			// Aspetta che diventi ready anche se già inizializzato
			return r.waitForReplicaSetReady(ctx, operator, shard)
		}
		return fmt.Errorf("errore inizializzando replica set: %w", err)
	}

	logger.Info("Replica set inizializzato", "shard", shardID)
	return r.waitForReplicaSetReady(ctx, operator, shard)
}

func (r *OperatorReconciler) waitForReplicaSetReady(ctx context.Context, operator *webappv1.Operator, shard int) error {
	logger := log.FromContext(ctx)
	shardID := fmt.Sprintf("shard%d", shard)

	timeout := time.Minute * 3
	deadline := time.Now().Add(timeout)
	pollInterval := time.Second * 5

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			directURI := fmt.Sprintf("mongodb://%s-shard%d-0.%s-shard%d-svc.%s.svc.cluster.local:27017/?directConnection=true",
				operator.Name, shard, operator.Name, shard, operator.Namespace)

			directClient, err := mongo.Connect(ctx, options.Client().ApplyURI(directURI))
			if err != nil {
				logger.Error(err, "Errore connessione durante attesa", "shard", shardID)
				time.Sleep(pollInterval)
				continue
			}

			var status map[string]interface{}
			err = directClient.Database("admin").RunCommand(ctx, map[string]interface{}{"replSetGetStatus": 1}).Decode(&status)
			directClient.Disconnect(ctx)

			if err != nil {
				logger.Info("Replica set non ancora pronto", "shard", shardID, "error", err.Error())
				time.Sleep(pollInterval)
				continue
			}

			// Verifica che ci sia un PRIMARY
			if hasPrimary, primaryHost := isReplicaSetReady(status); hasPrimary {
				logger.Info("Replica set pronto con PRIMARY", "shard", shardID, "primary", primaryHost)
				return nil
			}

			logger.Info("Replica set in fase di elezione", "shard", shardID)
			time.Sleep(pollInterval)
		}
	}

	return fmt.Errorf("timeout attesa replica set ready per shard %s", shardID)
}

func isReplicaSetReady(status map[string]interface{}) (bool, string) {
	members, ok := status["members"].([]interface{})
	if !ok {
		return false, ""
	}

	for _, member := range members {
		memberMap, ok := member.(map[string]interface{})
		if !ok {
			continue
		}

		stateStr, ok := memberMap["stateStr"].(string)
		if !ok {
			continue
		}

		name, ok := memberMap["name"].(string)
		if !ok {
			continue
		}

		if stateStr == "PRIMARY" {
			return true, name
		}
	}
	return false, ""
}

func (r *OperatorReconciler) syncShardsWithMongos(ctx context.Context, operator *webappv1.Operator) error {
	logger := log.FromContext(ctx)

	mongosURI := fmt.Sprintf("mongodb://%s-mongos-svc.%s.svc.cluster.local:27017",
		operator.Name, operator.Namespace)

	logger.Info("Connessione a mongos", "uri", mongosURI)

	clientOpts := options.Client().ApplyURI(mongosURI)
	mongosClient, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return fmt.Errorf("connessione a mongos fallita: %w", err)
	}
	defer mongosClient.Disconnect(ctx)

	// Recupera shard registrati
	existingShards, err := r.getExistingShards(ctx, mongosClient)
	if err != nil {
		return err
	}

	logger.Info("Shard esistenti in mongos", "shards", existingShards)

	// Aggiungi shard mancanti
	for shard := 0; shard < operator.Spec.Shards; shard++ {
		shardID := fmt.Sprintf("shard%d", shard)

		if existingShards[shardID] {
			logger.Info("Shard già registrato", "shard", shardID)
			continue
		}

		if err := r.addShardToMongos(ctx, mongosClient, operator, shard); err != nil {
			logger.Error(err, "Errore aggiungendo shard", "shard", shardID)
			return err
		}
	}

	return nil
}
func (r *OperatorReconciler) getExistingShards(ctx context.Context, mongosClient *mongo.Client) (map[string]bool, error) {
	existingShards := make(map[string]bool)

	cursor, err := mongosClient.Database("config").Collection("shards").Find(ctx, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("errore recuperando shard esistenti: %w", err)
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var shard struct {
			Id string `bson:"_id"`
		}
		if err := cursor.Decode(&shard); err == nil {
			existingShards[shard.Id] = true
		}
	}
	return existingShards, nil
}

func (r *OperatorReconciler) addShardToMongos(ctx context.Context, mongosClient *mongo.Client, operator *webappv1.Operator, shard int) error {
	shardID := fmt.Sprintf("shard%d", shard)

	// Costruisci la connection string corretta per il replica set
	var hosts []string
	for i := 0; i < operator.Spec.Replicas; i++ {
		host := fmt.Sprintf("%s-shard%d-%d.%s-shard%d-svc.%s.svc.cluster.local:27017",
			operator.Name, shard, i, operator.Name, shard, operator.Namespace)
		hosts = append(hosts, host)
	}

	// Formato corretto per addShard: "replicaSetName/host1,host2,host3"
	shardConnStr := fmt.Sprintf("%s/%s", shardID, strings.Join(hosts, ","))

	cmd := map[string]interface{}{
		"addShard": shardConnStr,
	}

	if err := mongosClient.Database("admin").RunCommand(ctx, cmd).Err(); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return fmt.Errorf("errore aggiungendo shard %s: %w", shardID, err)
	}

	log.FromContext(ctx).Info("Shard aggiunto correttamente", "shard", shardID)
	return nil
}

func (r *OperatorReconciler) removeExtraShards(ctx context.Context, mongosClient *mongo.Client, operator *webappv1.Operator, existingShards map[string]bool) error {
	logger := log.FromContext(ctx)

	// Identifica shard da rimuovere (presenti in MongoDB ma non nella spec)
	shardsToRemove := []string{}
	for shardID := range existingShards {
		// Verifica se lo shard è tra quelli desiderati
		isDesired := false
		for shard := 0; shard < operator.Spec.Shards; shard++ {
			if shardID == fmt.Sprintf("shard%d", shard) {
				isDesired = true
				break
			}
		}

		if !isDesired {
			shardsToRemove = append(shardsToRemove, shardID)
		}
	}

	// Rimuovi shard non desiderati
	for _, shardID := range shardsToRemove {
		cmd := map[string]interface{}{
			"removeShard": shardID,
		}

		if err := mongosClient.Database("admin").RunCommand(ctx, cmd).Err(); err != nil {
			logger.Error(err, "Errore rimuovendo shard", "shard", shardID)
			return err
		}
		logger.Info("Shard rimosso", "shard", shardID)
	}

	return nil
}

// ---------------------------------------------------------------------------------------------------------------------------//
// ---------------------------------------------------------------------------------------------------------------------------//
// ---------------------------------------------------------------------------------------------------------------------------//
// ---------------------------------------------------------------------------------------------------------------------------//
// ---------------------------------------------------------------------------------------------------------------------------//
// cleanupObsoleteResources elimina le risorse obsolete
func (r *OperatorReconciler) cleanupObsoleteResources(ctx context.Context, operator *webappv1.Operator) error {
	logger := log.FromContext(ctx)

	stsList := &appsv1.StatefulSetList{}
	if err := r.List(ctx, stsList, client.InNamespace(operator.Namespace)); err != nil {
		return err
	}

	for _, sts := range stsList.Items {
		if metav1.IsControlledBy(&sts, operator) {
			var shardNumber int
			_, err := fmt.Sscanf(sts.Name, "mongodb-shard-%d", &shardNumber)
			if err == nil && shardNumber >= operator.Spec.Shards {
				logger.Info("Eliminando shard obsoleto", "shard", sts.Name)
				if err := r.Delete(ctx, &sts); err != nil {
					logger.Error(err, "Errore eliminando shard obsoleto", "shard", sts.Name)
				}
			}
		}
	}

	return nil
}

// areAllPodsReady verifica se tutti i pod sono ready
func (r *OperatorReconciler) areAllPodsReady(ctx context.Context, namespace string) (bool, error) {
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(namespace)); err != nil {
		return false, err
	}

	if len(podList.Items) == 0 {
		return false, nil
	}

	for _, pod := range podList.Items {
		if !isPodReady(&pod) {
			return false, nil
		}
	}

	return true, nil
}

func isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range pod.Status.ContainerStatuses {
		if !c.Ready {
			return false
		}
	}
	return true
}

func getObjectName(obj runtime.Object) string {
	if metaObj, ok := obj.(metav1.Object); ok {
		return metaObj.GetName()
	}
	return "unknown"
}

func (r *OperatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&webappv1.Operator{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
