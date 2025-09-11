package controller

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/template"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	webappv1 "my.domain/operator/api/v1"
)

// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

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

	// ---- STEP 0: Pulisci le risorse obsolete prima di crearne di nuove ----
	if err := r.cleanupObsoleteResources(ctx, operator); err != nil {
		logger.Error(err, "Errore nella pulizia delle risorse obsolete")
		return ctrl.Result{}, err
	}

	// ---- STEP 1: renderizzo il template YAML con i valori della CR ----
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

	// ---- STEP 2: processo tutte le risorse nel YAML ----
	decoder := yaml.NewYAMLToJSONDecoder(&buf)
	resources := []client.Object{}

	for {
		obj := &unstructured.Unstructured{}
		err := decoder.Decode(obj)
		if err != nil {
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

		resources = append(resources, typedObj.(client.Object))
	}

	// Creazione delle risorse
	for _, resource := range resources {
		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, resource, func() error {
			return r.updateResource(resource, operator)
		})
		if err != nil {
			logger.Error(err, "Errore in CreateOrUpdate", "kind", resource.GetObjectKind().GroupVersionKind().Kind, "name", resource.GetName())
			return ctrl.Result{}, err
		} else {
			logger.Info("Risorsa creata o aggiornata", "kind", resource.GetObjectKind().GroupVersionKind().Kind, "name", resource.GetName())
		}
	}

	// ---- STEP 3: Inizializza i replica sets ----
	initialized, err := r.initializeReplicaSets(ctx, operator)
	if err != nil {
		logger.Error(err, "Errore nell'inizializzazione dei replica sets")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	if !initialized {
		logger.Info("Replica sets non ancora inizializzati, riprovo tra 15 secondi")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// ---- STEP 4: verifica se tutti i pod sono ready ----
	allReady, err := r.areAllPodsReady(ctx, operator)
	if err != nil {
		logger.Error(err, "Errore verificando lo stato dei pod")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if allReady {
		logger.Info("CI SIAMOOO - Tutti i pod sono ready e il cluster è operativo!")
		return ctrl.Result{}, nil
	}

	logger.Info("Alcuni pod non sono ancora ready, verifico di nuovo tra 10 secondi")
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// updateResource aggiorna le risorse esistenti con i nuovi valori dalla CR
func (r *OperatorReconciler) updateResource(obj client.Object, operator *webappv1.Operator) error {
	switch resource := obj.(type) {
	case *appsv1.StatefulSet:
		if strings.Contains(resource.Name, "configsvr") {
			if *resource.Spec.Replicas != int32(operator.Spec.ConfigReplicas) {
				replicas := int32(operator.Spec.ConfigReplicas)
				resource.Spec.Replicas = &replicas
			}
		} else if strings.Contains(resource.Name, "shard") {
			if *resource.Spec.Replicas != int32(operator.Spec.Replicas) {
				replicas := int32(operator.Spec.Replicas)
				resource.Spec.Replicas = &replicas
			}
		}

		for i := range resource.Spec.Template.Spec.Containers {
			if resource.Spec.Template.Spec.Containers[i].Name == "mongod" {
				resource.Spec.Template.Spec.Containers[i].Image = operator.Spec.MongoImage
			}
		}

	case *appsv1.Deployment:
		for i := range resource.Spec.Template.Spec.Containers {
			if resource.Spec.Template.Spec.Containers[i].Name == "mongos" {
				resource.Spec.Template.Spec.Containers[i].Image = operator.Spec.MongoImage
			}
		}
	}
	return nil
}

// initializeReplicaSets inizializza i replica sets di MongoDB
func (r *OperatorReconciler) initializeReplicaSets(ctx context.Context, operator *webappv1.Operator) (bool, error) {
	logger := log.FromContext(ctx)

	// Inizializza config server replica set
	configInitialized, err := r.initializeConfigReplicaSet(ctx, operator)
	if err != nil || !configInitialized {
		return false, err
	}

	// Inizializza shard replica sets
	for i := 0; i < operator.Spec.Shards; i++ {
		shardInitialized, err := r.initializeShardReplicaSet(ctx, operator, i)
		if err != nil || !shardInitialized {
			return false, err
		}
	}

	logger.Info("Tutti i replica sets sono stati inizializzati")
	return true, nil
}

// initializeConfigReplicaSet inizializza il replica set dei config server
func (r *OperatorReconciler) initializeConfigReplicaSet(ctx context.Context, operator *webappv1.Operator) (bool, error) {
	logger := log.FromContext(ctx)

	// Verifica se i pod dei config server sono ready
	podsReady, err := r.arePodsReady(ctx, operator.Namespace, map[string]string{
		"app": operator.Name + "-configsvr",
	})
	if err != nil || !podsReady {
		return false, err
	}

	// Crea un Job per inizializzare il replica set
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      operator.Name + "-init-config",
			Namespace: operator.Namespace,
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, job, func() error {
		// Configura il Job per l'inizializzazione
		job.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
		job.Spec.Template.Spec.Containers = []corev1.Container{
			{
				Name:    "init-config",
				Image:   operator.Spec.MongoImage,
				Command: []string{"/bin/sh", "-c"},
				Args: []string{
					fmt.Sprintf(`
					until mongosh --host %s-configsvr-0.%s-configsvr-svc --eval "rs.initiate({_id: 'configRepl', configsvr: true, members: [{_id: 0, host: '%s-configsvr-0.%s-configsvr-svc:27017'}, {_id: 1, host: '%s-configsvr-1.%s-configsvr-svc:27017'}, {_id: 2, host: '%s-configsvr-2.%s-configsvr-svc:27017'}]})" && mongosh --host %s-configsvr-0.%s-configsvr-svc --eval "rs.status()"; do
						echo "Waiting for config server initialization..."
						sleep 5
					done
					`, operator.Name, operator.Name, operator.Name, operator.Name, operator.Name, operator.Name, operator.Name, operator.Name, operator.Name, operator.Name),
				},
			},
		}
		return controllerutil.SetControllerReference(operator, job, r.Scheme)
	})

	if err != nil {
		return false, err
	}

	// Verifica se il Job è completato
	existingJob := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Name: job.Name, Namespace: job.Namespace}, existingJob); err != nil {
		return false, err
	}

	if existingJob.Status.Succeeded > 0 {
		logger.Info("Config server replica set inizializzato con successo")
		return true, nil
	}

	logger.Info("Config server replica set in fase di inizializzazione")
	return false, nil
}

// initializeShardReplicaSet inizializza il replica set di uno shard
func (r *OperatorReconciler) initializeShardReplicaSet(ctx context.Context, operator *webappv1.Operator, shardIndex int) (bool, error) {
	logger := log.FromContext(ctx)

	// Verifica se i pod dello shard sono ready
	podsReady, err := r.arePodsReady(ctx, operator.Namespace, map[string]string{
		"app":         operator.Name + "-shard",
		"shard-index": strconv.Itoa(shardIndex),
	})
	if err != nil || !podsReady {
		return false, err
	}

	// Crea un Job per inizializzare il replica set dello shard
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-init-shard-%d", operator.Name, shardIndex),
			Namespace: operator.Namespace,
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, job, func() error {
		job.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
		job.Spec.Template.Spec.Containers = []corev1.Container{
			{
				Name:    "init-shard",
				Image:   operator.Spec.MongoImage,
				Command: []string{"/bin/sh", "-c"},
				Args: []string{
					fmt.Sprintf(`
until mongosh --host %s-shard-%d-0.%s-shard-%d-svc --eval "rs.initiate({
  _id: 'shard%d', 
  members: [
    {_id: 0, host: '%s-shard-%d-0.%s-shard-%d-svc:27017'}, 
    {_id: 1, host: '%s-shard-%d-1.%s-shard-%d-svc:27017'}, 
    {_id: 2, host: '%s-shard-%d-2.%s-shard-%d-svc:27017'}
  ]})" && mongosh --host %s-shard-%d-0.%s-shard-%d-svc --eval "rs.status()"; do
  echo "Waiting for shard %d initialization..."
  sleep 5
done
`,
						operator.Name, shardIndex, // host 0
						operator.Name, shardIndex, // host 0 FQDN
						shardIndex,                                           // _id dello shard
						operator.Name, shardIndex, operator.Name, shardIndex, // member 0
						operator.Name, shardIndex, operator.Name, shardIndex, // member 1
						operator.Name, shardIndex, operator.Name, shardIndex, // member 2
						operator.Name, shardIndex, operator.Name, shardIndex, // rs.status host
						shardIndex, // echo shard index
					),
				},
			},
		}
		return controllerutil.SetControllerReference(operator, job, r.Scheme)
	})

	if err != nil {
		return false, err
	}

	// Verifica se il Job è completato
	existingJob := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Name: job.Name, Namespace: job.Namespace}, existingJob); err != nil {
		return false, err
	}

	if existingJob.Status.Succeeded > 0 {
		logger.Info("Shard replica set inizializzato con successo", "shard", shardIndex)
		return true, nil
	}

	logger.Info("Shard replica set in fase di inizializzazione", "shard", shardIndex)
	return false, nil
}

// arePodsReady verifica se i pod con specifiche label sono ready
func (r *OperatorReconciler) arePodsReady(ctx context.Context, namespace string, labels map[string]string) (bool, error) {
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(namespace), client.MatchingLabels(labels)); err != nil {
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

// cleanupObsoleteResources elimina le risorse che non sono più necessarie
func (r *OperatorReconciler) cleanupObsoleteResources(ctx context.Context, operator *webappv1.Operator) error {
	logger := log.FromContext(ctx)

	// Elimina StatefulSet di shard obsoleti
	stsList := &appsv1.StatefulSetList{}
	if err := r.List(ctx, stsList, client.InNamespace(operator.Namespace), client.MatchingLabels{"app": operator.Name + "-shard"}); err != nil {
		return err
	}

	for _, sts := range stsList.Items {
		if metav1.IsControlledBy(&sts, operator) {
			// Estrai il numero di shard dal nome (es: "mongodb-cluster-shard-0")
			parts := strings.Split(sts.Name, "-")
			if len(parts) >= 3 {
				shardNumberStr := parts[len(parts)-1]
				shardNumber, err := strconv.Atoi(shardNumberStr)
				if err == nil && shardNumber >= operator.Spec.Shards {
					logger.Info("Eliminando shard obsoleto", "shard", sts.Name)
					if err := r.Delete(ctx, &sts); err != nil {
						logger.Error(err, "Errore eliminando shard obsoleto", "shard", sts.Name)
					}

					// Elimina anche il service associato
					serviceName := fmt.Sprintf("%s-shard-%d-svc", operator.Name, shardNumber)
					service := &corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Name:      serviceName,
							Namespace: operator.Namespace,
						},
					}
					if err := r.Delete(ctx, service); client.IgnoreNotFound(err) != nil {
						logger.Error(err, "Errore eliminando service obsoleto", "service", serviceName)
					}

					// Elimina i job di inizializzazione obsoleti
					jobName := fmt.Sprintf("%s-init-shard-%d", operator.Name, shardNumber)
					job := &batchv1.Job{
						ObjectMeta: metav1.ObjectMeta{
							Name:      jobName,
							Namespace: operator.Namespace,
						},
					}
					if err := r.Delete(ctx, job); client.IgnoreNotFound(err) != nil {
						logger.Error(err, "Errore eliminando job obsoleto", "job", jobName)
					}
				}
			}
		}
	}

	return nil
}

// areAllPodsReady verifica se tutti i pod del cluster sono ready
func (r *OperatorReconciler) areAllPodsReady(ctx context.Context, operator *webappv1.Operator) (bool, error) {
	// Verifica config server
	configReady, err := r.arePodsReady(ctx, operator.Namespace, map[string]string{
		"app": operator.Name + "-configsvr",
	})
	if err != nil || !configReady {
		return false, err
	}

	// Verifica shards
	for i := 0; i < operator.Spec.Shards; i++ {
		shardReady, err := r.arePodsReady(ctx, operator.Namespace, map[string]string{
			"app":         operator.Name + "-shard",
			"shard-index": strconv.Itoa(i),
		})
		if err != nil || !shardReady {
			return false, err
		}
	}

	// Verifica mongos
	mongosReady, err := r.arePodsReady(ctx, operator.Namespace, map[string]string{
		"app": operator.Name + "-mongos",
	})
	if err != nil || !mongosReady {
		return false, err
	}

	return true, nil
}

// isPodReady verifica se un pod è ready
func isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}

	for _, containerStatus := range pod.Status.ContainerStatuses {
		if !containerStatus.Ready {
			return false
		}
	}

	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status != corev1.ConditionTrue {
			return false
		}
	}

	return true
}

func (r *OperatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&webappv1.Operator{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
