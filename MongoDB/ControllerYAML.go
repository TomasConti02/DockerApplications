package controller

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"text/template"
	"time"

	appsv1 "k8s.io/api/apps/v1"
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

		// ---- Qui uso CreateOrUpdate per evitare loop ----
		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, typedObj.(client.Object), func() error {
			// Aggiorna i campi che potrebbero essere cambiati nella CR
			switch obj := typedObj.(type) {
			case *appsv1.StatefulSet:
				// Per StatefulSet, aggiorna replica count e immagine
				if *obj.Spec.Replicas != int32(operator.Spec.Replicas) {
					replicas := int32(operator.Spec.Replicas)
					obj.Spec.Replicas = &replicas
				}
				for i := range obj.Spec.Template.Spec.Containers {
					if obj.Spec.Template.Spec.Containers[i].Name == "mongo" {
						obj.Spec.Template.Spec.Containers[i].Image = operator.Spec.MongoImage
					}
				}
			case *appsv1.Deployment:
				// Per Deployment, aggiorna replica count e immagine
				if *obj.Spec.Replicas != int32(operator.Spec.ConfigReplicas) {
					replicas := int32(operator.Spec.ConfigReplicas)
					obj.Spec.Replicas = &replicas
				}
				for i := range obj.Spec.Template.Spec.Containers {
					if obj.Spec.Template.Spec.Containers[i].Name == "mongo" {
						obj.Spec.Template.Spec.Containers[i].Image = operator.Spec.MongoImage
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

	// ---- STEP 3: verifica se tutti i pod sono ready ----
	allReady, err := r.areAllPodsReady(ctx, operator.Namespace)
	if err != nil {
		logger.Error(err, "Errore verificando lo stato dei pod")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if allReady {
		logger.Info("CI SIAMOOO - Tutti i pod sono ready!")
		return ctrl.Result{}, nil
	}

	logger.Info("Alcuni pod non sono ancora ready, verifico di nuovo tra 10 secondi")
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// cleanupObsoleteResources elimina le risorse che non sono più necessarie
func (r *OperatorReconciler) cleanupObsoleteResources(ctx context.Context, operator *webappv1.Operator) error {
	logger := log.FromContext(ctx)

	// Elimina StatefulSet di shard obsoleti
	stsList := &appsv1.StatefulSetList{}
	if err := r.List(ctx, stsList, client.InNamespace(operator.Namespace)); err != nil {
		return err
	}

	for _, sts := range stsList.Items {
		// Controlla se lo StatefulSet appartiene a questo operatore
		if metav1.IsControlledBy(&sts, operator) {
			// Estrai il numero di shard dal nome (es: "mongodb-shard-2")
			var shardNumber int
			_, err := fmt.Sscanf(sts.Name, "mongodb-shard-%d", &shardNumber)
			if err == nil && shardNumber >= operator.Spec.Shards {
				// Questo shard non è più necessario
				logger.Info("Eliminando shard obsoleto", "shard", sts.Name)
				if err := r.Delete(ctx, &sts); err != nil {
					logger.Error(err, "Errore eliminando shard obsoleto", "shard", sts.Name)
				}
			}
		}
	}

	return nil
}

// areAllPodsReady verifica se tutti i pod nel namespace sono ready
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

// isPodReady verifica se un pod è ready
func isPodReady(pod *corev1.Pod) bool {
	// Controlla se il pod è in fase Running
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}

	// Controlla che tutti i container siano ready
	for _, containerStatus := range pod.Status.ContainerStatuses {
		if !containerStatus.Ready {
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
