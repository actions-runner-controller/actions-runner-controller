/*
Copyright 2020 The actions-runner-controller authors.

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
	"errors"
	"fmt"
	gogithub "github.com/google/go-github/v33/github"
	"time"

	"github.com/go-logr/logr"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/summerwind/actions-runner-controller/api/v1alpha1"
	"github.com/summerwind/actions-runner-controller/github"
)

// RunnerReplicaSetReconciler reconciles a Runner object
type RunnerReplicaSetReconciler struct {
	client.Client
	Log          logr.Logger
	Recorder     record.EventRecorder
	Scheme       *runtime.Scheme
	GitHubClient *github.Client
}

// +kubebuilder:rbac:groups=actions.summerwind.dev,resources=runnerreplicasets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=actions.summerwind.dev,resources=runnerreplicasets/finalizers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=actions.summerwind.dev,resources=runnerreplicasets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=actions.summerwind.dev,resources=runners,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=actions.summerwind.dev,resources=runners/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

func (r *RunnerReplicaSetReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("runnerreplicaset", req.NamespacedName)

	var rs v1alpha1.RunnerReplicaSet
	if err := r.Get(ctx, req.NamespacedName, &rs); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !rs.ObjectMeta.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	var allRunners v1alpha1.RunnerList
	if err := r.List(ctx, &allRunners, client.InNamespace(req.Namespace)); err != nil {
		if !kerrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	var myRunners []v1alpha1.Runner

	var available, ready int

	for _, r := range allRunners.Items {
		if metav1.IsControlledBy(&r, &rs) {
			myRunners = append(myRunners, r)

			available += 1

			if r.Status.Phase == string(corev1.PodRunning) {
				ready += 1
			}
		}
	}

	var desired int

	if rs.Spec.Replicas != nil {
		desired = *rs.Spec.Replicas
	} else {
		desired = 1
	}

	log.V(0).Info("debug", "desired", desired, "available", available)

	if available > desired {
		n := available - desired

		// get runners that are currently not busy
		var notBusy []v1alpha1.Runner
		for _, runner := range myRunners {
			busy, err := r.GitHubClient.IsRunnerBusy(ctx, runner.Spec.Enterprise, runner.Spec.Organization, runner.Spec.Repository, runner.Name)
			if err != nil {
				notRegistered := false
				offline := false

				var notFoundException *github.RunnerNotFound
				var offlineException *github.RunnerOffline
				if errors.As(err, &notFoundException) {
					log.V(1).Info("Failed to check if runner is busy. Either this runner has never been successfully registered to GitHub or it still needs more time.", "runnerName", runner.Name)
					notRegistered = true
				} else if errors.As(err, &offlineException) {
					offline = true
				} else {
					var e *gogithub.RateLimitError
					if errors.As(err, &e) {
						// We log the underlying error when we failed calling GitHub API to list or unregisters,
						// or the runner is still busy.
						log.Error(
							err,
							fmt.Sprintf(
								"Failed to check if runner is busy due to GitHub API rate limit. Retrying in %s to avoid excessive GitHub API calls",
								retryDelayOnGitHubAPIRateLimitError,
							),
						)

						return ctrl.Result{RequeueAfter: retryDelayOnGitHubAPIRateLimitError}, err
					}

					return ctrl.Result{}, err
				}

				registrationTimeout := 15 * time.Minute
				currentTime := time.Now()
				registrationDidTimeout := currentTime.Sub(runner.CreationTimestamp.Add(registrationTimeout)) > 0

				if notRegistered && registrationDidTimeout {
					log.Info(
						"Runner failed to register itself to GitHub in timely manner. "+
							"Marking the runner for scale down. "+
							"CAUTION: If you see this a lot, you should investigate the root cause. "+
							"See https://github.com/summerwind/actions-runner-controller/issues/288",
						"runnerCreationTimestamp", runner.CreationTimestamp,
						"currentTime", currentTime,
						"configuredRegistrationTimeout", registrationTimeout,
					)

					notBusy = append(notBusy, runner)
				}

				// offline runners should always be a great target for scale down
				if offline {
					notBusy = append(notBusy, runner)
				}

			} else if !busy {
				notBusy = append(notBusy, runner)
			}
		}

		if len(notBusy) < n {
			n = len(notBusy)
		}

		for i := 0; i < n; i++ {
			if err := r.Client.Delete(ctx, &notBusy[i]); client.IgnoreNotFound(err) != nil {
				log.Error(err, "Failed to delete runner resource")

				return ctrl.Result{}, err
			}

			r.Recorder.Event(&rs, corev1.EventTypeNormal, "RunnerDeleted", fmt.Sprintf("Deleted runner '%s'", myRunners[i].Name))
			log.Info("Deleted runner", "runnerreplicaset", rs.ObjectMeta.Name)
		}
	} else if desired > available {
		n := desired - available

		for i := 0; i < n; i++ {
			newRunner, err := r.newRunner(rs)
			if err != nil {
				log.Error(err, "Could not create runner")

				return ctrl.Result{}, err
			}

			if err := r.Client.Create(ctx, &newRunner); err != nil {
				log.Error(err, "Failed to create runner resource")

				return ctrl.Result{}, err
			}
		}
	}

	if rs.Status.AvailableReplicas != available || rs.Status.ReadyReplicas != ready {
		updated := rs.DeepCopy()
		updated.Status.AvailableReplicas = available
		updated.Status.ReadyReplicas = ready

		if err := r.Status().Update(ctx, updated); err != nil {
			log.Error(err, "Failed to update runner status")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *RunnerReplicaSetReconciler) newRunner(rs v1alpha1.RunnerReplicaSet) (v1alpha1.Runner, error) {
	objectMeta := rs.Spec.Template.ObjectMeta.DeepCopy()

	objectMeta.GenerateName = rs.ObjectMeta.Name + "-"
	objectMeta.Namespace = rs.ObjectMeta.Namespace

	runner := v1alpha1.Runner{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: *objectMeta,
		Spec:       rs.Spec.Template.Spec,
	}

	if err := ctrl.SetControllerReference(&rs, &runner, r.Scheme); err != nil {
		return runner, err
	}

	return runner, nil
}

func (r *RunnerReplicaSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	name := "runnerreplicaset-controller"
	r.Recorder = mgr.GetEventRecorderFor(name)

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.RunnerReplicaSet{}).
		Owns(&v1alpha1.Runner{}).
		Named(name).
		Complete(r)
}
