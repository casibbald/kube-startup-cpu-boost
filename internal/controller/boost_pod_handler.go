// Copyright 2023 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/google/kube-startup-cpu-boost/internal/boost"
	bpod "github.com/google/kube-startup-cpu-boost/internal/boost/pod"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type BoostPodHandler interface {
	Create(context.Context, event.CreateEvent,
		workqueue.TypedRateLimitingInterface[reconcile.Request])
	Update(context.Context, event.UpdateEvent,
		workqueue.TypedRateLimitingInterface[reconcile.Request])
	Delete(context.Context, event.DeleteEvent,
		workqueue.TypedRateLimitingInterface[reconcile.Request])
	Generic(context.Context, event.GenericEvent,
		workqueue.TypedRateLimitingInterface[reconcile.Request])
	GetPodLabelSelector() *metav1.LabelSelector
}

type boostPodHandler struct {
	manager boost.Manager
	log     logr.Logger
}

func NewBoostPodHandler(manager boost.Manager, log logr.Logger) BoostPodHandler {
	return &boostPodHandler{
		manager: manager,
		log:     log,
	}
}

func (h *boostPodHandler) Create(ctx context.Context, e event.CreateEvent,
	wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod, ok := e.Object.(*corev1.Pod)
	if !ok {
		return
	}
	log := h.log.WithValues("pod", pod.Name, "namespace", pod.Namespace)
	log.V(5).Info("handling pod create")
	boost, err := h.manager.UpsertPod(ctx, pod)
	if err != nil {
		log.Error(err, "failed to handle pod create")
		return
	}
	if boost != nil {
		wq.Add(reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      boost.Name(),
				Namespace: boost.Namespace(),
			},
		})
	}
}

func (h *boostPodHandler) Delete(ctx context.Context, e event.DeleteEvent,
	wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod, ok := e.Object.(*corev1.Pod)
	if !ok {
		return
	}
	log := h.log.WithValues("pod", pod.Name, "namespace", pod.Namespace)
	log.V(5).Info("handling pod delete")
	boost, err := h.manager.DeletePod(ctx, pod)
	if err != nil {
		log.Error(err, "failed to handle pod delete")
		return
	}
	if boost != nil {
		wq.Add(reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      boost.Name(),
				Namespace: boost.Namespace(),
			},
		})
	}
}

func (h *boostPodHandler) Update(ctx context.Context, e event.UpdateEvent,
	wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod, ok := e.ObjectNew.(*corev1.Pod)
	oldPod, ok_ := e.ObjectOld.(*corev1.Pod)
	if !ok || !ok_ {
		return
	}
	log := h.log.WithValues("pod", pod.Name, "namespace", pod.Namespace)
	log.V(5).Info("handling pod update")

	// Check for ContainerRestart trigger - detect restartCount increases
	boost, err := h.manager.UpsertPod(ctx, pod)
	if err != nil {
		log.Error(err, "failed to handle pod update")
		return
	}

	// If boost found, check for ContainerRestart triggers
	if boost != nil {
		// Check for ContainerRestart trigger if configured
		if boost.HasContainerRestartTrigger() {
			// Check if restartCount increased for any container
			annotation, err := bpod.BoostAnnotationFromPod(pod)
			if err == nil {
				// Update restart counts and get containers that restarted
				incrementedContainers := annotation.UpdateLastRestartCounts(pod)
				if len(incrementedContainers) > 0 {
					log.V(5).Info("detected container restart(s)", "containers", incrementedContainers)
					// Check if boost has ContainerRestart trigger configured for any restarted container
					shouldActivate := false
					for containerName := range incrementedContainers {
						if boost.ShouldActivateForContainerRestart(containerName) {
							shouldActivate = true
							log.V(5).Info("ContainerRestart trigger matches container", "container", containerName)
							break
						}
					}
					if shouldActivate {
						log.Info("ContainerRestart trigger detected, queuing boost reconciliation")
						wq.Add(reconcile.Request{
							NamespacedName: types.NamespacedName{
								Name:      boost.Name(),
								Namespace: boost.Namespace(),
							},
						})
					}
				}
			} else {
				// Pod doesn't have boost annotation yet, but we still need to track restart counts
				// This will be handled when the boost is first activated
				log.V(5).Info("pod does not have boost annotation, restart tracking will be initialized on first activation")
			}
		}

		// Check for PodConditionTransition trigger if configured
		if boost.HasPodConditionTransitionTrigger() {
			// Check if condition states changed
			annotation, err := bpod.BoostAnnotationFromPod(pod)
			if err == nil {
				// Update condition states and get transitions
				transitions := annotation.UpdateLastConditionStates(pod)
				if len(transitions) > 0 {
					log.V(5).Info("detected condition transition(s)", "transitions", transitions)
					// Check if boost has PodConditionTransition trigger configured for any transition
					shouldActivate := false
					for conditionType, transition := range transitions {
						// Only trigger if we have a previous state (not initial observation)
						// This prevents false positives on initial Ready=True
						if transition.FromStatus != "" {
							if boost.ShouldActivateForPodConditionTransition(conditionType, transition.FromStatus, transition.ToStatus) {
								shouldActivate = true
								log.V(5).Info("PodConditionTransition trigger matches", "condition", conditionType, "from", transition.FromStatus, "to", transition.ToStatus)
								break
							}
						} else {
							// First observation - store state but don't trigger
							log.V(5).Info("first condition observation, storing state without triggering", "condition", conditionType, "status", transition.ToStatus)
						}
					}
					if shouldActivate {
						log.Info("PodConditionTransition trigger detected, queuing boost reconciliation")
						wq.Add(reconcile.Request{
							NamespacedName: types.NamespacedName{
								Name:      boost.Name(),
								Namespace: boost.Namespace(),
							},
						})
					}
				}
			} else {
				// Pod doesn't have boost annotation yet, but we still need to track condition states
				// This will be handled when the boost is first activated
				log.V(5).Info("pod does not have boost annotation, condition tracking will be initialized on first activation")
			}
		} else {
			// Also check for condition changes (existing logic for backward compatibility)
			// This is for pods that don't have PodConditionTransition triggers but still need
			// to be reconciled when conditions change (e.g., for duration policy expiry)
			if !equality.Semantic.DeepEqual(pod.Status.Conditions, oldPod.Status.Conditions) {
				log.V(5).Info("pod conditions changed, queuing boost reconciliation")
				wq.Add(reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      boost.Name(),
						Namespace: boost.Namespace(),
					},
				})
			}
		}
	}
}

func (h *boostPodHandler) Generic(ctx context.Context, e event.GenericEvent,
	wq workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod, ok := e.Object.(*corev1.Pod)
	if !ok {
		return
	}
	log := h.log.WithValues("pod", pod.Name, "namespace", pod.Namespace)
	log.V(5).Info("handling pod generic event")
}

func (h *boostPodHandler) GetPodLabelSelector() *metav1.LabelSelector {
	return &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      bpod.BoostLabelKey,
				Operator: metav1.LabelSelectorOpExists,
				Values:   []string{},
			},
		},
	}
}
