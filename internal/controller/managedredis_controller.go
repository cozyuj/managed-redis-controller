package controller

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	redisv1 "github.com/cozyuj/managedredis/api/v1"
)

// ManagedRedisReconciler reconciles a ManagedRedis object
type ManagedRedisReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=redis.redis-youjin.com,resources=managedredis,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=redis.redis-youjin.com,resources=managedredis/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=redis.redis-youjin.com,resources=managedredis/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

func (r *ManagedRedisReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// CR 가져오기
	var redis redisv1.ManagedRedis
	if err := r.Get(ctx, req.NamespacedName, &redis); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// StatefulSet 생성/업데이트
	sts := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: redis.Name, Namespace: redis.Namespace}, sts)
	if err != nil {
		// 존재하지 않으면 새로 생성
		sts = &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      redis.Name,
				Namespace: redis.Namespace,
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: &redis.Spec.Replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": redis.Name},
				},
				ServiceName: redis.Name,
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": redis.Name},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "redis",
								Image: "redis:" + redis.Spec.Version,
								Ports: []corev1.ContainerPort{
									{ContainerPort: 6379, Name: "redis"},
								},
							},
						},
					},
				},
			},
		}
		if err := r.Create(ctx, sts); err != nil {
			log.Error(err, "Failed to create StatefulSet")
			return ctrl.Result{}, err
		}
	} else {
		// replicas 변경 등 업데이트
		if sts.Spec.Replicas == nil || *sts.Spec.Replicas != redis.Spec.Replicas {
			sts.Spec.Replicas = &redis.Spec.Replicas
			if err := r.Update(ctx, sts); err != nil {
				log.Error(err, "Failed to update StatefulSet replicas")
				return ctrl.Result{}, err
			}
		}
	}

	// Service 생성/업데이트
	svc := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: redis.Name, Namespace: redis.Namespace}, svc)
	if err != nil {
		svc = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      redis.Name,
				Namespace: redis.Namespace,
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": redis.Name},
				Ports: []corev1.ServicePort{
					{Port: 6379, TargetPort: intstr.FromInt(6379), Name: "redis"},
				},
			},
		}
		if err := r.Create(ctx, svc); err != nil {
			log.Error(err, "Failed to create Service")
			return ctrl.Result{}, err
		}
	}

	// CR status 업데이트
	redis.Status.Phase = "Running"
	redis.Status.Endpoint = redis.Name + "." + redis.Namespace + ".svc.cluster.local:6379"
	if err := r.Status().Update(ctx, &redis); err != nil {
		log.Error(err, "Failed to update ManagedRedis status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *ManagedRedisReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&redisv1.ManagedRedis{}).
		Complete(r)
}
