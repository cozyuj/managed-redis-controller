package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
	Scheme     *runtime.Scheme
	APIBaseURL string
}

// +kubebuilder:rbac:groups=redis.redis-youjin.com,resources=managedredis,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=redis.redis-youjin.com,resources=managedredis/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=redis.redis-youjin.com,resources=managedredis/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

func (r *ManagedRedisReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. ManagedRedis CR 가져오기
	var redis redisv1.ManagedRedis
	if err := r.Get(ctx, req.NamespacedName, &redis); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. StatefulSet 생성/업데이트
	sts := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: redis.Name, Namespace: redis.Namespace}, sts)
	if err != nil && errors.IsNotFound(err) {
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
	} else if err == nil {
		// replicas 업데이트
		if sts.Spec.Replicas == nil || *sts.Spec.Replicas != redis.Spec.Replicas {
			sts.Spec.Replicas = &redis.Spec.Replicas
			if err := r.Update(ctx, sts); err != nil {
				log.Error(err, "Failed to update StatefulSet replicas")
				return ctrl.Result{}, err
			}
		}
	} else {
		return ctrl.Result{}, err
	}

	// 3. Service 생성/업데이트
	svc := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: redis.Name, Namespace: redis.Namespace}, svc)
	if err != nil && errors.IsNotFound(err) {
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
	} else if err != nil {
		return ctrl.Result{}, err
	}

	// 4. CR status 업데이트
	redis.Status.Phase = "Running"
	redis.Status.Endpoint = redis.Name + "." + redis.Namespace + ".svc.cluster.local:6379"
	if err := r.Status().Update(ctx, &redis); err != nil {
		log.Error(err, "Failed to update ManagedRedis status")
		return ctrl.Result{}, err
	}

	// 5. API 서버로 status 전달
	statusPayload := map[string]interface{}{
		"phase":    redis.Status.Phase,
		"endpoint": redis.Status.Endpoint,
		"replicas": sts.Status.ReadyReplicas,
	}
	body, _ := json.Marshal(statusPayload)
	apiURL := r.APIBaseURL + "/api/clusters/" + string(redis.UID)
	reqHttp, _ := http.NewRequest("GET", apiURL, bytes.NewBuffer(body))
	reqHttp.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(reqHttp)
	if err != nil {
		log.Error(err, "failed to send status to API server")
		return ctrl.Result{Requeue: true}, err
	}
	defer resp.Body.Close()
	log.Info("status sent to API server", "name", string(redis.UID), "statusCode", resp.StatusCode)

	return ctrl.Result{}, nil
}

func (r *ManagedRedisReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&redisv1.ManagedRedis{}).
		Complete(r)
}
