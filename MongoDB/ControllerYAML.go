package controller

import (
	"bytes"
	"context"
	_ "embed"
	"io"
	"text/template"

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
			// In questo callback possiamo aggiornare lo stato desiderato
			return nil // il typedObj già contiene lo stato corretto dal template
		})
		if err != nil {
			logger.Error(err, "Errore in CreateOrUpdate", "gvk", gvk)
			continue
		} else {
			logger.Info("Risorsa creata o aggiornata", "kind", gvk.Kind, "name", getObjectName(typedObj))
		}
	}

	logger.Info("Cluster MongoDB configurato con successo")
	return ctrl.Result{}, nil
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
