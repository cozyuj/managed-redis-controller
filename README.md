# Managed Redis Controller README

## 1. 개요
이 프로젝트는 Kubernetes 환경에서 관리되는 **Managed Redis 서비스**를 위한 Controller를 개발하는 것을 목표로 합니다.  
Controller는 ManagedRedis CR의 변경을 감지하여 실제 Redis Pod 및 Service를 생성/삭제하고, 클러스터 상태를 관리합니다.

- 언어: Go (Golang)  
- Kubernetes Controller Runtime 사용 (`controller-runtime`)  
- Redis Deployment/StatefulSet 사용 금지, Pod와 Service 직접 관리  



## 2. Controller 주요 기능

1. **CR 생성 감지**
   - 새로운 `ManagedRedis` CR이 생성되면 `Spec`에 정의된 Replica 수와 Redis 버전에 따라 Pod 생성
   - 단일 인스턴스는 Primary만 생성, 다중 인스턴스인 경우 Primary-Replica 구성

2. **Pod Role 관리**
   - Pod의 `role` label을 통해 Primary / Replica 구분
   - Primary Pod만 Primary Service에 포함, Replica Pod는 Reader Service에 포함
   - Cluster 모드(Shard)는 사용하지 않음

3. **Replica 스케일링**
   - 사용자가 CR의 `Spec.Replicas` 수를 늘리면 기존 Primary 노드에 Replica 추가
   - 줄이면 기존 Replica 중 하나 삭제
   - 상태(`Phase`)는 `SCALING-OUT` / `SCALING-IN`으로 업데이트

4. **Failover**
   - Primary Pod 장애 감지 시 Replica 중 하나를 Primary로 승격
   - Phase를 `FAILOVER`로 설정 후 승격 완료 시 `RUNNING`으로 복귀
   - Replica 선택 기준: READY 상태이며, 가장 오래 실행 중인 Pod 우선

5. **Status 업데이트**
   - 각 단계에 맞춰 CR의 Phase 업데이트
     - `CREATING`: Redis 구성 중
     - `RUNNING`: 구성 완료, Service Endpoint 노출
     - `SCALING-OUT`: Replica 추가 중
     - `SCALING-IN`: Replica 제거 중
     - `WARNING`: Replica 장애 발생 시
     - `FAILOVER`: Primary 장애 발생 시
   - Primary/Replica Pod 정보 및 Endpoint를 Status에 기록

6. **Finalizer 처리**
   - CR 삭제 요청 시 관련 Pod와 Service를 모두 삭제 후 Finalizer 제거
   - 삭제 후 잔여 리소스가 남지 않도록 보장



## 3. 멱등성 보장

- Reconcile Loop 반복 실행 시, 이미 생성된 Pod/Service를 중복 생성하지 않음
- Replica 수 증가/감소 시 기존 Pod 상태 확인 후 필요한 작업만 수행
- Phase 업데이트 시 현재 상태를 확인 후 변경 → 불필요한 업데이트 방지



## 4. API 서버와 Controller 역할 분리

- **API 서버**
  - 사용자 요청 처리
  - CR 생성/수정/삭제
  - 클러스터 상태 조회 제공 (JSON 응답)
- **Controller**
  - CR 변경 감지
  - 실제 Pod/Service 생성 및 관리
  - 클러스터 상태 모니터링 및 Phase 업데이트
  - API 서버에 상태 전송 (POST 요청)

해당 구조를 통해 API 서버는 사용자 인터페이스 역할, Controller는 실제 클러스터 관리 역할을 담당



## 5. Switchover vs Failover 처리

| 구분 | Switchover | Failover |
|------|------------|----------|
| 정의 | 계획된 Primary 교체 | 장애로 인한 Primary 교체 |
| 트리거 | 운영자가 직접 요청 | Primary Pod 상태가 Failed/Unknown |
| 수행 | Replica 승격 전 통제 | READY 상태의 Replica 중 자동 승격 |
| Phase | SCALING-OUT/SCALING-IN | FAILOVER → RUNNING |

- Failover 수행 시 Replica 선택 기준
  - READY 상태인 Pod
  - 가장 오래 실행 중인 Pod 우선
  - 네트워크 지연이나 장애 이력 고려 가능



## 6. Controller 다중 배포 시 고려 사항

- 여러 Controller가 동시에 같은 CR을 Reconcile할 경우 race condition 발생 가능
- 해결 방안
  - Kubernetes 리소스 수준에서 `ResourceVersion` 기반 멱등성 유지
  - Controller Runtime에서 제공하는 Leader Election 기능 활성화
  - Pod/Service 생성 시 존재 여부 체크 후 실행



## 7. 사용 예시

### CR 생성
```yaml
apiVersion: redis.yourdomain.com/v1
kind: ManagedRedis
metadata:
  name: redis-demo
  namespace: default
spec:
  version: "7.0"
  mode: "primary-replica"
  replicas: 2
```

## 7. 상태 업데이트 예시

Controller는 Redis 클러스터의 상태 변화에 따라 `ManagedRedis.Status` 필드를 지속적으로 업데이트합니다.

### ■ 상태 흐름 예시

### 1) CR 생성 직후
```yaml
status:
  phase: CREATING
  primary: null
  replicas: []
  readyReplicas: 0
```
### 2) Primary Pod 생성 직후
```yaml
status:
  phase: RUNNING
  primary:
    name: redis-demo-primary-xxx
    podIP: 10.1.0.12
    role: primary
  replicas: []
  readyReplicas: 1
  ```

### 3) Replica 수 증가 요청 (SCALING-OUT)
```yaml
status:
  phase: SCALING-OUT
  readyReplicas: 1
```

### 4) Replica Pod 정상 배치 완료
```yaml
status:
  phase: RUNNING
  primary: {...}
  replicas:
    - name: redis-demo-replica-xxx
      role: replica
      podIP: 10.1.0.15
  readyReplicas: 2
```

### 5) Replica 노드 장애 발생 (WARNING)
```yaml
status:
  phase: WARNING
```

### 6) Primary 장애 발생 → Failover 수행
```yaml
status:
  phase: FAILOVER
```



## 8. CR 삭제 시 리소스 정리 (축소 버전)

Controller는 Finalizer를 통해 ManagedRedis CR 삭제 시 다음을 보장합니다:

- 생성했던 모든 Pod 삭제
- Primary / Replica Service 삭제
- 상태 보고 중단
- 마지막에 Finalizer 제거 → CR 완전 삭제

최종적으로 Kubernetes 클러스터에 불필요한 리소스가 남지 않도록 정리됩니다.

---

## 9. Controller 다중 인스턴스 실행 시 문제점 및 해결 방안 (요약)

### 문제점
- 여러 Controller가 동시에 같은 CR을 처리하면 Pod 중복 생성, Failover 충돌 등 Race Condition 발생 가능.

### 해결 방안
- **Leader Election 사용**하여 단일 Active Controller만 동작  
- 리소스 존재 여부를 항상 확인 후 생성/삭제  
- Patch 기반의 멱등적(Re-entrant) 업데이트 적용  
- Failover 수행 시 Primary 존재 여부 재검증



## 10. Failover 시 Replica 선택 기준 (요약)

Primary 장애 발생 시 다음 기준으로 Replica를 선택해 승격합니다:

1. Running 상태(PodRunning)
2. Readiness Probe를 통과한 Pod
3. 가장 오래 실행 중인 Replica 우선 선택 (StartTime 기준)

선택된 Replica는 `role=primary`로 라벨이 변경되고 Primary 서비스가 새 Pod로 트래픽을 전달합니다.



## 11. Switchover vs Failover 차이 (요약)

| 구분 | Switchover (계획된 교체) | Failover (장애 대응) |
|------|---------------------------|------------------------|
| 발생 원인 | 관리자가 의도적으로 Primary 교체 | Primary Pod 장애 발생 |
| 안정성 | 높음, 사전 준비 후 진행 | 낮음, 즉각적 대응 |
| 처리 방법 | 선정된 Replica를 Primary로 승격 | 생존한 Replica 중 하나를 자동 승격 |

Switchover는 예측 가능한 작업이며, Failover는 장애 대응 중심의 빠른 처리 절차입니다.

