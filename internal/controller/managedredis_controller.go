package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	redisv1 "github.com/cozyuj/managedredis/api/v1"
)

const (
	finalizerName      = "managedredis.finalizers"
	primaryRole        = "primary"
	replicaRole        = "replica"
	primarySvcSuffix   = "-primary"
	readerSvcSuffix    = "-reader"
	redisContainerPort = 6379
)

type ManagedRedisReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	APIBaseURL string
}

// ============================================================================
// Reconcile
// ============================================================================
func (r *ManagedRedisReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var redis redisv1.ManagedRedis
	if err := r.Get(ctx, req.NamespacedName, &redis); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ----------------------------------------------------------------------
	// Finalizer
	// ----------------------------------------------------------------------
	if redis.ObjectMeta.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&redis, finalizerName) {
			controllerutil.AddFinalizer(&redis, finalizerName)
			if err := r.Update(ctx, &redis); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		pods, _ := r.listPods(ctx, &redis)
		if err := r.reconcileDelete(ctx, &redis, pods); err != nil {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, err
		}
		return ctrl.Result{}, nil
	}

	// ----------------------------------------------------------------------
	// Pod 조회
	// ----------------------------------------------------------------------
	pods, err := r.listPods(ctx, &redis)
	if err != nil {
		return ctrl.Result{}, err
	}

	// ----------------------------------------------------------------------
	// 서비스 보장 (Primary / Replica)
	// ----------------------------------------------------------------------
	if err := r.ensureServices(ctx, &redis); err != nil {
		return ctrl.Result{}, err
	}

	// ----------------------------------------------------------------------
	// Primary 없으면 생성
	// ----------------------------------------------------------------------
	if !hasPrimary(pods) {
		redis.Status.Phase = "CREATING"
		_ = r.Status().Update(ctx, &redis)

		err := r.createPod(ctx, &redis, primaryRole)
		if err != nil {
			return ctrl.Result{RequeueAfter: 3 * time.Second}, err
		}
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	// ----------------------------------------------------------------------
	// Replica 스케일 처리
	// ----------------------------------------------------------------------
	res, err := r.reconcileReplicas(ctx, &redis, pods)
	if err != nil || res.Requeue {
		return res, err
	}

	// ----------------------------------------------------------------------
	// Failover 체크
	// ----------------------------------------------------------------------
	primaryPod := findPrimaryPod(pods)
	if primaryPod == nil {
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}
	if isPodUnhealthy(primaryPod) {
		redis.Status.Phase = "FAILOVER"
		_ = r.Status().Update(ctx, &redis)

		if err := r.performFailover(ctx, &redis, pods); err != nil {
			return ctrl.Result{RequeueAfter: 3 * time.Second}, err
		}
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	// ----------------------------------------------------------------------
	// 모든 상태 정상 → RUNNING 상태 업데이트
	// ----------------------------------------------------------------------
	r.buildStatus(&redis, pods)
	_ = r.Status().Update(ctx, &redis)

	// API 서버로 전달
	if err := r.sendStatusToAPIServer(&redis); err != nil {
		log.Error(err, "failed sending status to API")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// ============================================================================
// Pod 생성
// ============================================================================
func (r *ManagedRedisReconciler) createPod(ctx context.Context, redis *redisv1.ManagedRedis, role string) error {

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s-%d", redis.Name, role, time.Now().UnixNano()),
			Namespace: redis.Namespace,
			Labels: map[string]string{
				"app":  redis.Name,
				"role": role,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "redis",
					Image: "redis:" + redis.Spec.Version,
					Ports: []corev1.ContainerPort{
						{ContainerPort: redisContainerPort, Name: "redis"},
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{
								Port: intstr.FromInt(redisContainerPort),
							},
						},
						InitialDelaySeconds: 3,
						PeriodSeconds:       5,
					},
				},
			},
		},
	}

	return r.Create(ctx, pod)
}

// ============================================================================
// Pod 목록 조회
// ============================================================================
func (r *ManagedRedisReconciler) listPods(ctx context.Context, redis *redisv1.ManagedRedis) ([]corev1.Pod, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(redis.Namespace),
		client.MatchingLabels{"app": redis.Name},
	); err != nil {
		return nil, err
	}
	return pods.Items, nil
}

// ============================================================================
// Service 생성 (Primary, Replica)
// ============================================================================
func (r *ManagedRedisReconciler) ensureServices(ctx context.Context, redis *redisv1.ManagedRedis) error {
	// Primary service
	if err := r.ensureService(ctx, redis,
		redis.Name+primarySvcSuffix,
		map[string]string{"app": redis.Name, "role": primaryRole},
	); err != nil {
		return err
	}

	// Replica service
	return r.ensureService(ctx, redis,
		redis.Name+readerSvcSuffix,
		map[string]string{"app": redis.Name, "role": replicaRole},
	)
}

func (r *ManagedRedisReconciler) ensureService(ctx context.Context, redis *redisv1.ManagedRedis, svcName string, selector map[string]string) error {

	svc := &corev1.Service{}
	err := r.Get(ctx, client.ObjectKey{Name: svcName, Namespace: redis.Namespace}, svc)
	if errors.IsNotFound(err) {

		svc = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      svcName,
				Namespace: redis.Namespace,
			},
			Spec: corev1.ServiceSpec{
				Selector: selector,
				Ports: []corev1.ServicePort{
					{
						Name:       "redis",
						Port:       redisContainerPort,
						TargetPort: intstr.FromInt(redisContainerPort),
					},
				},
			},
		}
		return r.Create(ctx, svc)
	}
	return err
}

// ============================================================================
// Replica Scaling
// ============================================================================
func (r *ManagedRedisReconciler) reconcileReplicas(ctx context.Context, redis *redisv1.ManagedRedis, pods []corev1.Pod) (ctrl.Result, error) {

	current := countReplicas(pods)
	desired := int(redis.Spec.Replicas)

	// Scale Out
	if current < desired {
		redis.Status.Phase = "SCALING-OUT"
		_ = r.Status().Update(ctx, redis)

		if err := r.createPod(ctx, redis, replicaRole); err != nil {
			return ctrl.Result{RequeueAfter: 3 * time.Second}, err
		}
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	// Scale In
	if current > desired {
		redis.Status.Phase = "SCALING-IN"
		_ = r.Status().Update(ctx, redis)

		for _, p := range pods {
			if p.Labels["role"] == replicaRole {
				_ = r.Delete(ctx, &p)
				break
			}
		}
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// ============================================================================
// Failover
// ============================================================================
func (r *ManagedRedisReconciler) performFailover(ctx context.Context, redis *redisv1.ManagedRedis, pods []corev1.Pod) error {

	for _, p := range pods {
		if p.Labels["role"] == replicaRole {
			patch := []byte(`{"metadata":{"labels":{"role":"primary"}}}`)
			err := r.Patch(ctx, &p, client.RawPatch(types.MergePatchType, patch))
			return err
		}
	}
	return nil
}

// ============================================================================
// 상태 빌드
// ============================================================================
func (r *ManagedRedisReconciler) buildStatus(redis *redisv1.ManagedRedis, pods []corev1.Pod) {

	redis.Status.Phase = "RUNNING"

	var primary *redisv1.RedisNodeInfo
	var replicas []redisv1.RedisNodeInfo

	for _, p := range pods {
		info := redisv1.RedisNodeInfo{
			Name:     p.Name,
			Role:     p.Labels["role"],
			PodIP:    p.Status.PodIP,
			NodeName: p.Spec.NodeName,
			Status:   string(p.Status.Phase),
		}

		if p.Labels["role"] == primaryRole {
			primary = &info
		} else if p.Labels["role"] == replicaRole {
			replicas = append(replicas, info)
		}
	}

	redis.Status.Primary = primary
	redis.Status.Replicas = replicas

	redis.Status.PrimaryEndpoint = fmt.Sprintf(
		"%s.%s.svc.cluster.local:%d",
		redis.Name+primarySvcSuffix,
		redis.Namespace,
		redisContainerPort,
	)

	redis.Status.ReplicaEndpoint = fmt.Sprintf(
		"%s.%s.svc.cluster.local:%d",
		redis.Name+readerSvcSuffix,
		redis.Namespace,
		redisContainerPort,
	)

	redis.Status.ReadyReplicas = int32(countReadyPods(pods))
}

// ============================================================================
// API 서버 전달
// ============================================================================
func (r *ManagedRedisReconciler) sendStatusToAPIServer(redis *redisv1.ManagedRedis) error {

	body, _ := json.Marshal(redis.Status)
	url := fmt.Sprintf("%s/api/clusters/%s", r.APIBaseURL, string(redis.UID))

	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)

	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("API server returned %d", resp.StatusCode)
	}

	return nil
}

// ============================================================================
// 헬퍼함수
// ============================================================================
func hasPrimary(pods []corev1.Pod) bool {
	for _, p := range pods {
		if p.Labels["role"] == primaryRole {
			return true
		}
	}
	return false
}

func findPrimaryPod(pods []corev1.Pod) *corev1.Pod {
	for _, p := range pods {
		if p.Labels["role"] == primaryRole {
			return &p
		}
	}
	return nil
}

func countReplicas(pods []corev1.Pod) int {
	n := 0
	for _, p := range pods {
		if p.Labels["role"] == replicaRole {
			n++
		}
	}
	return n
}

func countReadyPods(pods []corev1.Pod) int {
	n := 0
	for _, p := range pods {
		if p.Status.Phase == corev1.PodRunning {
			n++
		}
	}
	return n
}

func isPodUnhealthy(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodUnknown
}

// ============================================================================
// Finalizer 삭제 처리
// ============================================================================
func (r *ManagedRedisReconciler) reconcileDelete(ctx context.Context, redis *redisv1.ManagedRedis, pods []corev1.Pod) error {

	// Pod 삭제
	for _, p := range pods {
		_ = r.Delete(ctx, &p)
	}

	// 서비스 삭제
	_ = r.Delete(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      redis.Name + primarySvcSuffix,
			Namespace: redis.Namespace,
		},
	})
	_ = r.Delete(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      redis.Name + readerSvcSuffix,
			Namespace: redis.Namespace,
		},
	})

	controllerutil.RemoveFinalizer(redis, finalizerName)
	return r.Update(ctx, redis)
}

// ============================================================================
// Setup
// ============================================================================
func (r *ManagedRedisReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&redisv1.ManagedRedis{}).
		Complete(r)
}
