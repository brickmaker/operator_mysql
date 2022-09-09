/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/pointer"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	k8sv1 "brickmaker.github.io/operator-mysql/api/v1"
)

// MySQLReconciler reconciles a MySQL object
type MySQLReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=k8s.brickmaker.github.io,resources=mysqls,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=k8s.brickmaker.github.io,resources=mysqls/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=k8s.brickmaker.github.io,resources=mysqls/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MySQL object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *MySQLReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("reconcile", "req", req.String())

	mysql := &k8sv1.MySQL{}
	if err := r.Get(ctx, req.NamespacedName, mysql); err != nil {
		logger.Error(err, "cannot get mysql object")
		return ctrl.Result{}, client.IgnoreNotFound(err) // 忽略没有找到的错误，用于删除的情况
	}

	// 删除中
	if !mysql.DeletionTimestamp.IsZero() {
		logger.Info("deleting MySQL" + mysql.Name)
		return ctrl.Result{}, nil
	}

	statefulSet := &appsv1.StatefulSet{}

	objectKey := client.ObjectKey{
		Name:      mysql.Name,
		Namespace: mysql.Namespace,
	}
	if err := r.Get(ctx, objectKey, statefulSet); err != nil && errors.IsNotFound(err) {
		// 不存在，新建statefulSet
		ownerRef := []metav1.OwnerReference{
			{
				APIVersion:         mysql.APIVersion,
				Kind:               mysql.Kind,
				Name:               mysql.Name,
				Controller:         pointer.BoolPtr(true),
				BlockOwnerDeletion: pointer.BoolPtr(true),
				UID:                mysql.UID,
			},
		}
		statefulSet = &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:            mysql.Name,
				Namespace:       mysql.Namespace,
				OwnerReferences: ownerRef,
			},
			Spec: appsv1.StatefulSetSpec{
				ServiceName: "mysql-svc", // TODO: 只走了controller流程，没有实际为StatfulSet创建服务
				Replicas:    mysql.Spec.Replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": mysql.Name,
					},
				},
				Template: apiv1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": mysql.Name,
						},
					},
					Spec: apiv1.PodSpec{
						TerminationGracePeriodSeconds: pointer.Int64Ptr(10),
						Containers: []apiv1.Container{
							{
								Name:  "mysql",
								Image: "mysql:" + mysql.Spec.Version,
								Ports: []apiv1.ContainerPort{
									{
										ContainerPort: 3306,
									},
								},
								VolumeMounts: []apiv1.VolumeMount{
									{
										Name:      "mysql-store",
										MountPath: "/var/lib/mysql",
									},
								},
								Env: []apiv1.EnvVar{
									{
										// TODO: 只为了描述问题，设置密码为空
										Name:  "MYSQL_ALLOW_EMPTY_PASSWORD",
										Value: "true",
									},
								},
							},
						},
					},
				},
				VolumeClaimTemplates: []apiv1.PersistentVolumeClaim{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "mysql-store",
						},
						Spec: apiv1.PersistentVolumeClaimSpec{
							AccessModes: []apiv1.PersistentVolumeAccessMode{"ReadWriteOnce"},
							Resources: apiv1.ResourceRequirements{
								Requests: apiv1.ResourceList{
									"storage": resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			},
		}

		if err := r.Create(ctx, statefulSet); err != nil {
			logger.Error(err, "create MySQL deployment failed")
			return ctrl.Result{}, err
		}
		logger.Info("create MySQL deployment success: " + statefulSet.GetObjectMeta().GetName())

	} else if err == nil {
		// 获取到，更新statefulSet
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			statefulSet.Spec.Replicas = mysql.Spec.Replicas
			statefulSet.Spec.Template.Spec.Containers[0].Image = "mysql:" + mysql.Spec.Version
			updateErr := r.Update(ctx, statefulSet)
			return updateErr
		})
		if retryErr != nil {
			logger.Error(retryErr, "update MySQL deployment failed")
			return ctrl.Result{}, retryErr
		}
		logger.Info("update MySQL deployment success: " + statefulSet.GetObjectMeta().GetName())

	} else {
		logger.Error(err, "cannot get MySQL deployment")
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MySQLReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&k8sv1.MySQL{}).
		Owns(&appsv1.StatefulSet{}). // 用来处理删除时的联动删除statefulset
		Complete(r)
}
