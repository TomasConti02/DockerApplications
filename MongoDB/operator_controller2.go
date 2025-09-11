package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	webappv1 "my.domain/operator/api/v1"
)

// OperatorReconciler reconciles a Operator object
type OperatorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=webapp.my.domain,resources=operators,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=webapp.my.domain,resources=operators/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=webapp.my.domain,resources=operators/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete

func (r *OperatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("operator", req.NamespacedName)

	// 1. Fetch the Operator instance
	operator := &webappv1.Operator{}
	if err := r.Get(ctx, req.NamespacedName, operator); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Operator resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get Operator")
		return ctrl.Result{}, err
	}

	// 2. Initialize status if needed
	if operator.Status.Conditions == nil {
		operator.Status.Conditions = []metav1.Condition{}
	}

	// 3. Update phase to Progressing
	meta.SetStatusCondition(&operator.Status.Conditions, metav1.Condition{
		Type:    "Available",
		Status:  metav1.ConditionFalse,
		Reason:  "Reconciling",
		Message: "Starting reconciliation",
	})
	operator.Status.Phase = "Progressing"
	operator.Status.ObservedGeneration = operator.Generation

	// 4. Create Config Servers (StatefulSet)
	if err := r.reconcileConfigServers(ctx, operator); err != nil {
		logger.Error(err, "Failed to reconcile Config Servers")
		return ctrl.Result{}, err
	}

	// 5. Create Shards (StatefulSets)
	if err := r.reconcileShards(ctx, operator); err != nil {
		logger.Error(err, "Failed to reconcile Shards")
		return ctrl.Result{}, err
	}

	// 6. Create Mongos Router (Deployment)
	if err := r.reconcileMongos(ctx, operator); err != nil {
		logger.Error(err, "Failed to reconcile Mongos")
		return ctrl.Result{}, err
	}
	allReady, err := r.checkPodsReady(ctx, operator.Namespace, operator.Name)
	if err != nil {
		logger.Error(err, "Failed to check pod readiness")
		return ctrl.Result{}, err
	}
	if allReady {
		logger.Info("🎉 CI SIAMOOOOO 🎉 Tutti i pod sono Ready!")
	}
	// 7. Update status
	operator.Status.ConfigReady = true
	operator.Status.ShardsReady = operator.Spec.Shards
	operator.Status.MongosReady = true
	operator.Status.Phase = "Ready"
	meta.SetStatusCondition(&operator.Status.Conditions, metav1.Condition{
		Type:    "Available",
		Status:  metav1.ConditionTrue,
		Reason:  "Reconciled",
		Message: "MongoDB cluster components created successfully",
	})

	// 8. Update status
	if err := r.Status().Update(ctx, operator); err != nil {
		logger.Error(err, "Failed to update Operator status")
		return ctrl.Result{}, err
	}

	logger.Info("Reconciliation completed successfully")
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}
func (r *OperatorReconciler) checkPodsReady(ctx context.Context, namespace, operatorName string) (bool, error) {
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(namespace), client.MatchingLabels{"operator": operatorName}); err != nil {
		return false, err
	}

	if len(podList.Items) == 0 {
		return false, nil
	}

	for _, pod := range podList.Items {
		ready := false
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				ready = true
				break
			}
		}
		if !ready {
			return false, nil
		}
	}
	return true, nil
}
func (r *OperatorReconciler) reconcileConfigServers(ctx context.Context, operator *webappv1.Operator) error {
	logger := log.FromContext(ctx)
	configStatefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-config", operator.Name),
			Namespace: operator.Namespace,
			Labels:    map[string]string{"app": "mongodb-config", "operator": operator.Name},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    int32Ptr(int32(operator.Spec.ConfigReplicas)),
			ServiceName: fmt.Sprintf("%s-config-service", operator.Name),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "mongodb-config", "operator": operator.Name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "mongodb-config", "operator": operator.Name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "mongodb",
							Image: operator.Spec.MongoImage,
							Ports: []corev1.ContainerPort{
								{ContainerPort: 27017, Name: "mongodb"},
							},
							Command: []string{"mongod", "--configsvr", "--replSet", "configReplSet", "--bind_ip_all"},
						},
					},
				},
			},
		},
	}

	// Set controller reference
	if err := ctrl.SetControllerReference(operator, configStatefulSet, r.Scheme); err != nil {
		return err
	}

	// Create or Update
	existing := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: configStatefulSet.Name, Namespace: configStatefulSet.Namespace}, existing)
	if err != nil && errors.IsNotFound(err) {
		logger.Info("Creating Config Server StatefulSet", "name", configStatefulSet.Name)
		return r.Create(ctx, configStatefulSet)
	} else if err != nil {
		return err
	}

	// Update if needed
	existing.Spec = configStatefulSet.Spec
	logger.Info("Updating Config Server StatefulSet", "name", configStatefulSet.Name)
	return r.Update(ctx, existing)
}

func (r *OperatorReconciler) reconcileShards(ctx context.Context, operator *webappv1.Operator) error {
	logger := log.FromContext(ctx)
	for i := 0; i < operator.Spec.Shards; i++ {
		shardName := fmt.Sprintf("%s-shard-%d", operator.Name, i)

		shardStatefulSet := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      shardName,
				Namespace: operator.Namespace,
				Labels:    map[string]string{"app": "mongodb-shard", "shard": fmt.Sprintf("%d", i), "operator": operator.Name},
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas:    int32Ptr(int32(operator.Spec.Replicas)),
				ServiceName: fmt.Sprintf("%s-service", shardName),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "mongodb-shard", "shard": fmt.Sprintf("%d", i), "operator": operator.Name},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": "mongodb-shard", "shard": fmt.Sprintf("%d", i), "operator": operator.Name},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "mongodb",
								Image: operator.Spec.MongoImage,
								Ports: []corev1.ContainerPort{
									{ContainerPort: 27017, Name: "mongodb"},
								},
								Command: []string{"mongod", "--shardsvr", "--replSet", fmt.Sprintf("shardReplSet%d", i), "--bind_ip_all"},
							},
						},
					},
				},
			},
		}

		// Set controller reference
		if err := ctrl.SetControllerReference(operator, shardStatefulSet, r.Scheme); err != nil {
			return err
		}

		// Create or Update
		existing := &appsv1.StatefulSet{}
		err := r.Get(ctx, types.NamespacedName{Name: shardStatefulSet.Name, Namespace: shardStatefulSet.Namespace}, existing)
		if err != nil && errors.IsNotFound(err) {
			logger.Info("Creating Shard StatefulSet", "name", shardStatefulSet.Name, "shard", i)
			if err := r.Create(ctx, shardStatefulSet); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			// Update if needed
			existing.Spec = shardStatefulSet.Spec
			logger.Info("Updating Shard StatefulSet", "name", shardStatefulSet.Name, "shard", i)
			if err := r.Update(ctx, existing); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *OperatorReconciler) reconcileMongos(ctx context.Context, operator *webappv1.Operator) error {
	logger := log.FromContext(ctx)
	mongosDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-mongos", operator.Name),
			Namespace: operator.Namespace,
			Labels:    map[string]string{"app": "mongodb-mongos", "operator": operator.Name},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1), // Single mongos for testing
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "mongodb-mongos", "operator": operator.Name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "mongodb-mongos", "operator": operator.Name},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "mongos",
							Image: operator.Spec.MongoImage,
							Ports: []corev1.ContainerPort{
								{ContainerPort: 27017, Name: "mongodb"},
							},
							Command: []string{"mongos", "--configdb", "configReplSet/mongodb-config-0.mongodb-config-service:27017,mongodb-config-1.mongodb-config-service:27017,mongodb-config-2.mongodb-config-service:27017", "--bind_ip_all"},
						},
					},
				},
			},
		},
	}

	// Set controller reference
	if err := ctrl.SetControllerReference(operator, mongosDeployment, r.Scheme); err != nil {
		return err
	}

	// Create or Update
	existing := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: mongosDeployment.Name, Namespace: mongosDeployment.Namespace}, existing)
	if err != nil && errors.IsNotFound(err) {
		logger.Info("Creating Mongos Deployment", "name", mongosDeployment.Name)
		return r.Create(ctx, mongosDeployment)
	} else if err != nil {
		return err
	}

	// Update if needed
	existing.Spec = mongosDeployment.Spec
	logger.Info("Updating Mongos Deployment", "name", mongosDeployment.Name)
	return r.Update(ctx, existing)
}

func (r *OperatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&webappv1.Operator{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

// Helper function
func int32Ptr(i int32) *int32 { return &i }
