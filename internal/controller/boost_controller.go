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
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/go-logr/logr"
	autoscaling "github.com/google/kube-startup-cpu-boost/api/v1alpha1"
	"github.com/google/kube-startup-cpu-boost/internal/boost"
	bpod "github.com/google/kube-startup-cpu-boost/internal/boost/pod"
	"github.com/google/kube-startup-cpu-boost/internal/metrics"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	BoostActiveConditionTrueReason   = "Ready"
	BoostActiveConditionTrueMessage  = "Can boost new containers"
	BoostActiveConditionFalseReason  = "NotFound"
	BoostActiveConditionFalseMessage = "StartupCPUBoost not found"
	WantedServerVersionForNewRevert  = "v1.32.0"

	// Condition types for triggers
	ConditionTypePodCreate              = "PodCreate"
	ConditionTypeContainerRestart       = "ContainerRestart"
	ConditionTypePodConditionTransition = "PodConditionTransition"
)

// StartupCPUBoostReconciler reconciles a StartupCPUBoost object
type StartupCPUBoostReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Log              logr.Logger
	Manager          boost.Manager
	LegacyRevertMode bool
	Recorder         record.EventRecorder
}

//+kubebuilder:rbac:groups=autoscaling.x-k8s.io,resources=startupcpuboosts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=autoscaling.x-k8s.io,resources=startupcpuboosts/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=autoscaling.x-k8s.io,resources=startupcpuboosts/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;update;patch;watch
//+kubebuilder:rbac:groups="",resources=pods/resize,verbs=patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *StartupCPUBoostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result,
	error) {
	var boostObj autoscaling.StartupCPUBoost
	var err error
	if err = r.Client.Get(ctx, req.NamespacedName, &boostObj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := r.Log.WithValues("name", boostObj.Name, "namespace", boostObj.Namespace)
	newBoostObj := boostObj.DeepCopy()
	activeCondition := metav1.Condition{
		Type:               "Active",
		Status:             metav1.ConditionFalse,
		Reason:             BoostActiveConditionFalseReason,
		Message:            BoostActiveConditionFalseMessage,
		ObservedGeneration: boostObj.Generation,
	}
	boostInstance, ok := r.Manager.GetRegularCPUBoost(ctx, boostObj.Name, boostObj.Namespace)
	if !ok {
		// Boost not found in manager, create it from the current spec
		log.V(5).Info("boost not found in manager, creating from spec")
		newBoost, err := boost.NewStartupCPUBoost(r.Client, &boostObj, r.LegacyRevertMode)
		if err != nil {
			log.Error(err, "failed to create boost from spec")
			if r.Recorder != nil {
				r.Recorder.Event(&boostObj, corev1.EventTypeWarning, "BoostCreationFailed", fmt.Sprintf("Failed to create boost from spec: %v", err))
			}
			return ctrl.Result{}, err
		}
		if err := r.Manager.AddRegularCPUBoost(ctx, newBoost); err != nil {
			log.Error(err, "failed to add boost to manager")
			if r.Recorder != nil {
				r.Recorder.Event(&boostObj, corev1.EventTypeWarning, "BoostRegistrationFailed", fmt.Sprintf("Failed to register boost in manager: %v", err))
			}
			return ctrl.Result{}, err
		}
		boostInstance = newBoost
		if r.Recorder != nil {
			r.Recorder.Event(&boostObj, corev1.EventTypeNormal, "BoostCreated", "StartupCPUBoost successfully created and registered")
		}
	} else {
		// Boost exists, ensure it's up-to-date with the current spec
		// This handles cases where the Update event handler hasn't run yet
		log.V(5).Info("found boost in manager, ensuring it's up-to-date")
		if err := r.Manager.UpdateRegularCPUBoost(ctx, &boostObj); err != nil {
			log.Error(err, "failed to update boost from spec")
			if r.Recorder != nil {
				r.Recorder.Event(&boostObj, corev1.EventTypeWarning, "BoostUpdateFailed", fmt.Sprintf("Failed to update boost from spec: %v", err))
			}
			// Continue with existing boost rather than failing reconciliation
		} else {
			// Re-fetch the updated boost
			boostInstance, ok = r.Manager.GetRegularCPUBoost(ctx, boostObj.Name, boostObj.Namespace)
			if !ok {
				log.Error(nil, "boost disappeared after update")
				if r.Recorder != nil {
					r.Recorder.Event(&boostObj, corev1.EventTypeWarning, "BoostDisappeared", "Boost disappeared from manager after update")
				}
				return ctrl.Result{Requeue: true}, nil
			}
		}
	}

	log.V(5).Info("processing boost")
	stats := boostInstance.Stats()
	activeCondition.Status = metav1.ConditionTrue
	activeCondition.Reason = BoostActiveConditionTrueReason
	activeCondition.Message = BoostActiveConditionTrueMessage
	newBoostObj.Status.ActiveContainerBoosts = int32(stats.ActiveContainerBoosts)
	newBoostObj.Status.TotalContainerBoosts = int32(stats.TotalContainerBoosts)

	// Set conditions for each configured trigger
	// Check triggers directly from the CR spec to ensure we have the latest configuration
	// This is more robust than checking the boost instance, which might have stale data
	hasPodCreate := false
	hasContainerRestart := false
	hasPodConditionTransition := false

	for _, trigger := range boostObj.Spec.Triggers {
		switch trigger.Type {
		case autoscaling.BoostTriggerTypePodCreate:
			hasPodCreate = true
		case autoscaling.BoostTriggerTypeContainerRestart:
			hasContainerRestart = true
		case autoscaling.BoostTriggerTypePodConditionTransition:
			hasPodConditionTransition = true
		}
	}

	// If no triggers are specified, default to PodCreate (backward compatibility)
	if len(boostObj.Spec.Triggers) == 0 {
		hasPodCreate = true
	}

	// PodCreate trigger condition
	if hasPodCreate {
		podCreateCondition := metav1.Condition{
			Type:               ConditionTypePodCreate,
			Status:             metav1.ConditionTrue,
			Reason:             "Configured",
			Message:            "PodCreate trigger is configured and active",
			ObservedGeneration: boostObj.Generation,
		}
		meta.SetStatusCondition(&newBoostObj.Status.Conditions, podCreateCondition)

		// Check for PodCreate triggers and emit activation events if needed
		// PodCreate boosts are applied in the webhook, so we emit events when we see
		// pods that were recently created with PodCreate boosts
		if err := r.emitPodCreateActivationEvents(ctx, boostInstance, log); err != nil {
			log.Error(err, "failed to emit PodCreate activation events")
			// Don't fail reconciliation, just log the error
		}
	} else {
		podCreateCondition := metav1.Condition{
			Type:               ConditionTypePodCreate,
			Status:             metav1.ConditionFalse,
			Reason:             "NotConfigured",
			Message:            "PodCreate trigger is not configured",
			ObservedGeneration: boostObj.Generation,
		}
		meta.SetStatusCondition(&newBoostObj.Status.Conditions, podCreateCondition)
	}

	// ContainerRestart trigger condition
	if hasContainerRestart {
		containerRestartCondition := metav1.Condition{
			Type:               ConditionTypeContainerRestart,
			Status:             metav1.ConditionTrue,
			Reason:             "Configured",
			Message:            "ContainerRestart trigger is configured and active",
			ObservedGeneration: boostObj.Generation,
		}
		meta.SetStatusCondition(&newBoostObj.Status.Conditions, containerRestartCondition)

		// Check for ContainerRestart triggers and apply runtime boosts if needed
		if err := r.applyRuntimeBoostsForContainerRestart(ctx, boostInstance, boostObj.Spec.Cooldown, log); err != nil {
			log.Error(err, "failed to apply runtime boosts for ContainerRestart triggers")
			// Don't fail reconciliation, just log the error
		}
	} else {
		containerRestartCondition := metav1.Condition{
			Type:               ConditionTypeContainerRestart,
			Status:             metav1.ConditionFalse,
			Reason:             "NotConfigured",
			Message:            "ContainerRestart trigger is not configured",
			ObservedGeneration: boostObj.Generation,
		}
		meta.SetStatusCondition(&newBoostObj.Status.Conditions, containerRestartCondition)
	}

	// PodConditionTransition trigger condition
	if hasPodConditionTransition {
		podConditionTransitionCondition := metav1.Condition{
			Type:               ConditionTypePodConditionTransition,
			Status:             metav1.ConditionTrue,
			Reason:             "Configured",
			Message:            "PodConditionTransition trigger is configured and active",
			ObservedGeneration: boostObj.Generation,
		}
		meta.SetStatusCondition(&newBoostObj.Status.Conditions, podConditionTransitionCondition)

		// Check for PodConditionTransition triggers and apply runtime boosts if needed
		if err := r.applyRuntimeBoostsForPodConditionTransition(ctx, boostInstance, boostObj.Spec.Cooldown, log); err != nil {
			log.Error(err, "failed to apply runtime boosts for PodConditionTransition triggers")
			// Don't fail reconciliation, just log the error
		}
	} else {
		podConditionTransitionCondition := metav1.Condition{
			Type:               ConditionTypePodConditionTransition,
			Status:             metav1.ConditionFalse,
			Reason:             "NotConfigured",
			Message:            "PodConditionTransition trigger is not configured",
			ObservedGeneration: boostObj.Generation,
		}
		meta.SetStatusCondition(&newBoostObj.Status.Conditions, podConditionTransitionCondition)
	}

	meta.SetStatusCondition(&newBoostObj.Status.Conditions, activeCondition)

	// Ensure all conditions have the current ObservedGeneration
	// SetStatusCondition only updates ObservedGeneration if the condition changed,
	// so we need to manually update it to ensure it always reflects the current generation
	for i := range newBoostObj.Status.Conditions {
		newBoostObj.Status.Conditions[i].ObservedGeneration = boostObj.Generation
	}

	// Debug: Log condition details before comparison
	log.V(5).Info("status comparison",
		"oldConditionsCount", len(boostObj.Status.Conditions),
		"newConditionsCount", len(newBoostObj.Status.Conditions),
		"oldActiveContainers", boostObj.Status.ActiveContainerBoosts,
		"newActiveContainers", newBoostObj.Status.ActiveContainerBoosts,
		"oldTotalContainers", boostObj.Status.TotalContainerBoosts,
		"newTotalContainers", newBoostObj.Status.TotalContainerBoosts)

	// Log all condition types for debugging
	for _, cond := range newBoostObj.Status.Conditions {
		log.V(5).Info("new condition", "type", cond.Type, "status", cond.Status, "reason", cond.Reason, "observedGen", cond.ObservedGeneration)
	}
	for _, cond := range boostObj.Status.Conditions {
		log.V(5).Info("old condition", "type", cond.Type, "status", cond.Status, "reason", cond.Reason, "observedGen", cond.ObservedGeneration)
	}

	// Always update status if conditions changed or if this is a new CR (no status yet)
	// This ensures trigger conditions are always persisted
	// Force update if status.Conditions is nil or empty (first reconciliation)
	forceUpdate := len(boostObj.Status.Conditions) == 0
	statusChanged := !equality.Semantic.DeepEqual(newBoostObj.Status, boostObj.Status)

	if forceUpdate || statusChanged {
		log.Info("updating boost status",
			"forceUpdate", forceUpdate,
			"statusChanged", statusChanged,
			"conditionsCount", len(newBoostObj.Status.Conditions),
			"activeContainers", newBoostObj.Status.ActiveContainerBoosts,
			"totalContainers", newBoostObj.Status.TotalContainerBoosts)
		err = r.Client.Status().Update(ctx, newBoostObj)
		if err != nil {
			if apierrors.IsConflict(err) {
				log.V(5).Info("boost status update conflict, requeueing")
				return ctrl.Result{Requeue: true}, nil
			}
			log.Error(err, "boost status update error")
			if r.Recorder != nil {
				r.Recorder.Event(&boostObj, corev1.EventTypeWarning, "StatusUpdateFailed", fmt.Sprintf("Failed to update boost status: %v", err))
			}
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		// Emit event for successful reconciliation
		if r.Recorder != nil {
			triggerCount := len(boostObj.Spec.Triggers)
			if triggerCount == 0 {
				triggerCount = 1 // Default to PodCreate if no triggers specified
			}
			message := fmt.Sprintf("StartupCPUBoost reconciled successfully. Active containers: %d, Total containers: %d, Triggers configured: %d",
				stats.ActiveContainerBoosts, stats.TotalContainerBoosts, triggerCount)
			r.Recorder.Event(&boostObj, corev1.EventTypeNormal, "Reconciled", message)
		}
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *StartupCPUBoostReconciler) SetupWithManager(mgr ctrl.Manager,
	serverVersion string) error {
	setupLog := ctrl.Log.WithName("boost-controller-setup")
	setupLog.Info("SetupWithManager: starting controller setup")

	setupLog.Info("SetupWithManager: creating boost pod handler")
	boostPodHandler := NewBoostPodHandler(r.Manager, ctrl.Log.WithName("pod-handler"))

	setupLog.Info("SetupWithManager: creating label selector predicate")
	lsPredicate, err := predicate.LabelSelectorPredicate(*boostPodHandler.GetPodLabelSelector())
	if err != nil {
		setupLog.Error(err, "SetupWithManager: failed to create label selector predicate")
		return err
	}
	setupLog.Info("SetupWithManager: label selector predicate created")

	r.LegacyRevertMode = shouldUseLegacyRevertMode(serverVersion)
	setupLog.Info("SetupWithManager: legacy revert mode determined", "legacyRevertMode", r.LegacyRevertMode)

	setupLog.Info("SetupWithManager: getting event recorder")
	r.Recorder = mgr.GetEventRecorderFor("startupcpuboost-controller")
	setupLog.Info("SetupWithManager: event recorder obtained")

	setupLog.Info("SetupWithManager: building controller with manager")
	err = ctrl.NewControllerManagedBy(mgr).
		For(&autoscaling.StartupCPUBoost{}).
		Watches(&corev1.Pod{},
			boostPodHandler,
			builder.WithPredicates(lsPredicate)).
		WithEventFilter(r).
		Complete(r)
	if err != nil {
		setupLog.Error(err, "SetupWithManager: failed to build controller")
		return err
	}
	setupLog.Info("SetupWithManager: controller setup completed successfully")
	return nil
}

func (r *StartupCPUBoostReconciler) Create(e event.CreateEvent) bool {
	boostObj, ok := e.Object.(*autoscaling.StartupCPUBoost)
	if !ok {
		return true
	}
	log := r.Log.WithValues("name", boostObj.Name, "namespace", boostObj.Namespace)
	log.V(5).Info("handling boost create event")
	ctx := ctrl.LoggerInto(context.Background(), log)
	boost, err := boost.NewStartupCPUBoost(r.Client, boostObj, r.LegacyRevertMode)
	if err != nil {
		log.Error(err, "boost creation error")
		if r.Recorder != nil {
			r.Recorder.Event(boostObj, corev1.EventTypeWarning, "BoostCreationFailed", fmt.Sprintf("Failed to create boost: %v", err))
		}
		return true
	}
	if err := r.Manager.AddRegularCPUBoost(ctx, boost); err != nil {
		log.Error(err, "boost registration error")
		if r.Recorder != nil {
			r.Recorder.Event(boostObj, corev1.EventTypeWarning, "BoostRegistrationFailed", fmt.Sprintf("Failed to register boost: %v", err))
		}
		return true
	}
	if r.Recorder != nil {
		triggerCount := len(boostObj.Spec.Triggers)
		if triggerCount == 0 {
			triggerCount = 1 // Default to PodCreate
		}
		r.Recorder.Event(boostObj, corev1.EventTypeNormal, "BoostCreated", fmt.Sprintf("StartupCPUBoost created with %d trigger(s)", triggerCount))
	}
	return true
}

func (r *StartupCPUBoostReconciler) Delete(e event.DeleteEvent) bool {
	boostObj, ok := e.Object.(*autoscaling.StartupCPUBoost)
	if !ok {
		return true
	}
	log := r.Log.WithValues("name", boostObj.Name, "namespace", boostObj.Namespace)
	log.V(5).Info("handling boost delete event")
	ctx := ctrl.LoggerInto(context.Background(), log)
	r.Manager.DeleteRegularCPUBoost(ctx, boostObj.Namespace, boostObj.Name)
	if r.Recorder != nil {
		r.Recorder.Event(boostObj, corev1.EventTypeNormal, "BoostDeleted", fmt.Sprintf("StartupCPUBoost '%s' deleted from namespace '%s'", boostObj.Name, boostObj.Namespace))
	}
	return true
}

func (r *StartupCPUBoostReconciler) Update(e event.UpdateEvent) bool {
	boostObj, ok := e.ObjectNew.(*autoscaling.StartupCPUBoost)
	if !ok {
		return true
	}
	log := r.Log.WithValues("name", boostObj.Name, "namespace", boostObj.Namespace)
	log.V(5).Info("handling boost update event")
	ctx := ctrl.LoggerInto(context.Background(), log)
	if err := r.Manager.UpdateRegularCPUBoost(ctx, boostObj); err != nil {
		log.Error(err, "boost update error")
		if r.Recorder != nil {
			r.Recorder.Event(boostObj, corev1.EventTypeWarning, "BoostUpdateFailed", fmt.Sprintf("Failed to update boost: %v", err))
		}
		return true
	}
	if r.Recorder != nil {
		// Check if triggers changed
		oldBoostObj, oldOk := e.ObjectOld.(*autoscaling.StartupCPUBoost)
		if oldOk && !equality.Semantic.DeepEqual(oldBoostObj.Spec.Triggers, boostObj.Spec.Triggers) {
			triggerCount := len(boostObj.Spec.Triggers)
			if triggerCount == 0 {
				triggerCount = 1 // Default to PodCreate
			}
			r.Recorder.Event(boostObj, corev1.EventTypeNormal, "BoostUpdated", fmt.Sprintf("StartupCPUBoost updated with %d trigger(s)", triggerCount))
		}
	}
	return true
}

func (r *StartupCPUBoostReconciler) Generic(e event.GenericEvent) bool {
	log := r.Log.WithValues("object", klog.KObj(e.Object))
	log.V(5).Info("handling generic event")
	return true
}

// shouldUseLegacyRevertMode determines if legacy resource revert mode should be used
// basing on server version
func shouldUseLegacyRevertMode(serverVersion string) (legacyMode bool) {
	return version.CompareKubeAwareVersionStrings(WantedServerVersionForNewRevert,
		serverVersion) < 0
}

// applyRuntimeBoostsForContainerRestart applies runtime boosts to pods that have ContainerRestart triggers
// This is called during reconciliation when ContainerRestart triggers are detected
func (r *StartupCPUBoostReconciler) applyRuntimeBoostsForContainerRestart(ctx context.Context, boost boost.StartupCPUBoost, cooldownPolicy *autoscaling.CooldownPolicy, log logr.Logger) error {
	// List all pods in the boost namespace
	podList := &corev1.PodList{}
	if err := r.Client.List(ctx, podList, client.InNamespace(boost.Namespace())); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	// Filter to pods that match the boost selector
	matchingPods := make([]*corev1.Pod, 0)
	for i := range podList.Items {
		pod := &podList.Items[i]
		if boost.Matches(pod) {
			matchingPods = append(matchingPods, pod)
		}
	}

	// For each matching pod, check if boost should be applied
	for _, pod := range matchingPods {
		// Get pod annotation to check restart counts
		annotation, err := bpod.BoostAnnotationFromPod(pod)
		if err != nil {
			// Pod doesn't have boost annotation yet, skip (will be handled on first activation)
			continue
		}

		// Check if current activation has expired and clear it if so (idempotent behavior)
		if annotation.IsActivationExpired(pod) {
			log.V(5).Info("boost activation expired, clearing active state", "pod", pod.Name)
			// Get activation details before clearing for event emission
			currentActivation := annotation.GetCurrentActivation()
			if currentActivation != nil && r.Recorder != nil {
				// Emit expiration event before clearing
				eventMessage := r.formatExpirationEventMessage(pod, boost, currentActivation)
				r.Recorder.Event(pod, corev1.EventTypeNormal, "BoostExpired", eventMessage)
			}
			annotation.ClearCurrentActivation()
			// Update metrics: decrement active boost gauge when boost expires
			metrics.SetBoostActive(boost.Name(), boost.Namespace(), 0)
			// Update pod annotation to persist the cleared state
			labelsPatch := bpod.NewApplyBoostLabelsPatch(annotation, boost.Name())
			if err := r.Client.Patch(ctx, pod, labelsPatch); err != nil {
				log.Error(err, "failed to clear expired boost activation", "pod", pod.Name)
				// Continue processing even if patch fails
			}
		}

		// Update restart counts and get containers that restarted
		incrementedContainers := annotation.UpdateLastRestartCounts(pod)
		if len(incrementedContainers) == 0 {
			continue
		}

		// Check if boost should activate for any restarted container
		shouldActivate := false
		for containerName := range incrementedContainers {
			if boost.ShouldActivateForContainerRestart(containerName) {
				shouldActivate = true
				break
			}
		}

		if shouldActivate {
			// Check cooldown policy before applying boost
			triggerType := autoscaling.BoostTriggerTypeContainerRestart
			shouldSkip, reason := annotation.ShouldSkipDueToCooldown(triggerType, cooldownPolicy)
			if shouldSkip {
				log.Info("skipping boost activation due to cooldown", "pod", pod.Name, "reason", reason)
				// Emit event for skipped activation with detailed information
				eventMessage := r.formatCooldownEventMessage(pod, boost, triggerType, cooldownPolicy, annotation, reason)
				r.Recorder.Event(pod, corev1.EventTypeWarning, "BoostSkippedCooldown", eventMessage)
				// Update metrics for skipped activation
				metrics.IncrementBoostSkipped("cooldown", boost.Name(), boost.Namespace())
				continue
			}

			log.Info("applying runtime boost for ContainerRestart trigger", "pod", pod.Name, "namespace", pod.Namespace)
			applied, err := boost.ApplyBoostAtRuntime(ctx, pod, triggerType)
			if err != nil {
				log.Error(err, "failed to apply runtime boost", "pod", pod.Name)
				continue
			}
			if applied {
				log.Info("runtime boost applied successfully", "pod", pod.Name)
				// Emit event for boost activation
				eventMessage := fmt.Sprintf("CPU boost '%s' activated for pod '%s' via ContainerRestart trigger", boost.Name(), pod.Name)
				r.Recorder.Event(pod, corev1.EventTypeNormal, "BoostActivated", eventMessage)
				// Update metrics for successful activation
				metrics.IncrementBoostActivations(string(triggerType), boost.Name(), boost.Namespace())
				metrics.SetBoostActive(boost.Name(), boost.Namespace(), 1)
			} else {
				log.V(5).Info("boost not applied (likely already active)", "pod", pod.Name)
				// Emit event for skipped activation due to idempotency (boost already active)
				eventMessage := r.formatIdempotencySkipEventMessage(pod, boost, triggerType, annotation)
				r.Recorder.Event(pod, corev1.EventTypeNormal, "BoostSkippedActive", eventMessage)
				// Update metrics for skipped activation
				metrics.IncrementBoostSkipped("idempotency", boost.Name(), boost.Namespace())
			}
		}
	}

	return nil
}

// applyRuntimeBoostsForPodConditionTransition applies runtime boosts to pods that have PodConditionTransition triggers
// This is called during reconciliation when PodConditionTransition triggers are detected
func (r *StartupCPUBoostReconciler) applyRuntimeBoostsForPodConditionTransition(ctx context.Context, boost boost.StartupCPUBoost, cooldownPolicy *autoscaling.CooldownPolicy, log logr.Logger) error {
	// List all pods in the boost namespace
	podList := &corev1.PodList{}
	if err := r.Client.List(ctx, podList, client.InNamespace(boost.Namespace())); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	// Filter to pods that match the boost selector
	matchingPods := make([]*corev1.Pod, 0)
	for i := range podList.Items {
		pod := &podList.Items[i]
		if boost.Matches(pod) {
			matchingPods = append(matchingPods, pod)
		}
	}

	// For each matching pod, check if boost should be applied
	for _, pod := range matchingPods {
		// Get pod annotation to check condition states
		annotation, err := bpod.BoostAnnotationFromPod(pod)
		if err != nil {
			// Pod doesn't have boost annotation yet, skip (will be handled on first activation)
			continue
		}

		// Check if current activation has expired and clear it if so (idempotent behavior)
		if annotation.IsActivationExpired(pod) {
			log.V(5).Info("boost activation expired, clearing active state", "pod", pod.Name)
			// Get activation details before clearing for event emission
			currentActivation := annotation.GetCurrentActivation()
			if currentActivation != nil && r.Recorder != nil {
				// Emit expiration event before clearing
				eventMessage := r.formatExpirationEventMessage(pod, boost, currentActivation)
				r.Recorder.Event(pod, corev1.EventTypeNormal, "BoostExpired", eventMessage)
			}
			annotation.ClearCurrentActivation()
			// Update pod annotation to persist the cleared state
			labelsPatch := bpod.NewApplyBoostLabelsPatch(annotation, boost.Name())
			if err := r.Client.Patch(ctx, pod, labelsPatch); err != nil {
				log.Error(err, "failed to clear expired boost activation", "pod", pod.Name)
				// Continue processing even if patch fails
			}
		}

		// Update condition states and get transitions
		transitions := annotation.UpdateLastConditionStates(pod)
		if len(transitions) == 0 {
			continue
		}

		// Check if boost should activate for any condition transition
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
			// Check cooldown policy before applying boost
			triggerType := autoscaling.BoostTriggerTypePodConditionTransition
			shouldSkip, reason := annotation.ShouldSkipDueToCooldown(triggerType, cooldownPolicy)
			if shouldSkip {
				log.Info("skipping boost activation due to cooldown", "pod", pod.Name, "reason", reason)
				// Emit event for skipped activation with detailed information
				eventMessage := r.formatCooldownEventMessage(pod, boost, triggerType, cooldownPolicy, annotation, reason)
				r.Recorder.Event(pod, corev1.EventTypeWarning, "BoostSkippedCooldown", eventMessage)
				// Update metrics for skipped activation
				metrics.IncrementBoostSkipped("cooldown", boost.Name(), boost.Namespace())
				continue
			}

			log.Info("applying runtime boost for PodConditionTransition trigger", "pod", pod.Name, "namespace", pod.Namespace)
			applied, err := boost.ApplyBoostAtRuntime(ctx, pod, triggerType)
			if err != nil {
				log.Error(err, "failed to apply runtime boost", "pod", pod.Name)
				continue
			}
			if applied {
				log.Info("runtime boost applied successfully", "pod", pod.Name)
				// Emit event for boost activation
				eventMessage := fmt.Sprintf("CPU boost '%s' activated for pod '%s' via PodConditionTransition trigger", boost.Name(), pod.Name)
				r.Recorder.Event(pod, corev1.EventTypeNormal, "BoostActivated", eventMessage)
				// Update metrics for successful activation
				metrics.IncrementBoostActivations(string(triggerType), boost.Name(), boost.Namespace())
				metrics.SetBoostActive(boost.Name(), boost.Namespace(), 1)
			} else {
				log.V(5).Info("boost not applied (likely already active)", "pod", pod.Name)
				// Emit event for skipped activation due to idempotency (boost already active)
				eventMessage := r.formatIdempotencySkipEventMessage(pod, boost, triggerType, annotation)
				r.Recorder.Event(pod, corev1.EventTypeNormal, "BoostSkippedActive", eventMessage)
				// Update metrics for skipped activation
				metrics.IncrementBoostSkipped("idempotency", boost.Name(), boost.Namespace())
			}
		}
	}

	return nil
}

// formatCooldownEventMessage formats a detailed event message for cooldown-skipped activations
// Includes boost name, pod name, reason, and relevant timestamps
func (r *StartupCPUBoostReconciler) formatCooldownEventMessage(pod *corev1.Pod, boost boost.StartupCPUBoost, triggerType autoscaling.BoostTriggerType, cooldownPolicy *autoscaling.CooldownPolicy, annotation *bpod.BoostPodAnnotation, reason string) string {
	now := time.Now()
	boostName := boost.Name()
	podName := pod.Name

	// Build detailed message
	message := fmt.Sprintf("Boost '%s' activation skipped for pod '%s'", boostName, podName)

	// Add trigger type information
	message += fmt.Sprintf(" (trigger: %s)", triggerType)

	// Add reason
	message += fmt.Sprintf(". Reason: %s", reason)

	// Add timestamp information
	if cooldownPolicy != nil {
		// Add minimum interval information if applicable
		if cooldownPolicy.MinIntervalSeconds != nil && *cooldownPolicy.MinIntervalSeconds > 0 {
			lastActivationTime, exists := annotation.GetLastActivationTime(triggerType)
			if exists {
				elapsed := now.Sub(lastActivationTime)
				remaining := time.Duration(*cooldownPolicy.MinIntervalSeconds)*time.Second - elapsed
				if remaining > 0 {
					message += fmt.Sprintf(". Last activation: %s, remaining cooldown: %v", lastActivationTime.Format(time.RFC3339), remaining.Round(time.Second))
				}
			}
		}

		// Add maximum activations per hour information if applicable
		if cooldownPolicy.MaxActivationsPerHour != nil && *cooldownPolicy.MaxActivationsPerHour > 0 {
			activationCount := annotation.GetActivationHistoryCount()
			message += fmt.Sprintf(". Current activations in last hour: %d/%d", activationCount, *cooldownPolicy.MaxActivationsPerHour)
		}
	}

	// Add current timestamp
	message += fmt.Sprintf(". Timestamp: %s", now.Format(time.RFC3339))

	return message
}

// formatIdempotencySkipEventMessage formats an event message for idempotency-skipped activations
// Includes boost name, pod name, trigger type, and current activation details
func (r *StartupCPUBoostReconciler) formatIdempotencySkipEventMessage(pod *corev1.Pod, boost boost.StartupCPUBoost, triggerType autoscaling.BoostTriggerType, annotation *bpod.BoostPodAnnotation) string {
	now := time.Now()
	boostName := boost.Name()
	podName := pod.Name

	// Build message
	message := fmt.Sprintf("Boost '%s' activation skipped for pod '%s'", boostName, podName)
	message += fmt.Sprintf(" (trigger: %s)", triggerType)
	message += ". Reason: boost already active (idempotency)"

	// Add current activation details if available
	currentActivation := annotation.GetCurrentActivation()
	if currentActivation != nil {
		// Parse start time
		startTime, err := time.Parse(time.RFC3339, currentActivation.StartTime)
		if err == nil {
			duration := now.Sub(startTime)
			message += fmt.Sprintf(". Current activation started: %s (duration: %v)", startTime.Format(time.RFC3339), duration.Round(time.Second))
		}
		message += fmt.Sprintf(". Current activation trigger: %s", currentActivation.TriggerType)
		if currentActivation.ExpiryConditionType != "" {
			message += fmt.Sprintf(", expiry: %s", currentActivation.ExpiryConditionType)
		}
	}

	// Add timestamp
	message += fmt.Sprintf(". Timestamp: %s", now.Format(time.RFC3339))

	return message
}

// formatExpirationEventMessage formats an event message for boost expiration
// Includes boost name, pod name, trigger type, duration policy type, and total boost duration
func (r *StartupCPUBoostReconciler) formatExpirationEventMessage(pod *corev1.Pod, boost boost.StartupCPUBoost, activation *bpod.ActivationStateEntry) string {
	now := time.Now()
	boostName := boost.Name()
	podName := pod.Name

	// Build message
	message := fmt.Sprintf("CPU boost '%s' expired for pod '%s'", boostName, podName)
	message += fmt.Sprintf(" (trigger: %s)", activation.TriggerType)

	// Calculate total boost duration from start time
	startTime, err := time.Parse(time.RFC3339, activation.StartTime)
	if err == nil {
		duration := now.Sub(startTime)
		message += fmt.Sprintf(". Total boost duration: %v", duration.Round(time.Second))
	}

	// Add duration policy type and details
	message += fmt.Sprintf(". Duration policy type: %s", activation.ExpiryConditionType)
	switch activation.ExpiryConditionType {
	case "FixedDuration":
		if activation.ExpiryFixedDuration != nil {
			policyDuration := time.Duration(*activation.ExpiryFixedDuration) * time.Second
			message += fmt.Sprintf(" (%v)", policyDuration.Round(time.Second))
		}
	case "PodCondition":
		if activation.ExpiryPodCondition != nil {
			message += fmt.Sprintf(" (condition: %s=%s)", activation.ExpiryPodCondition.Type, activation.ExpiryPodCondition.Status)
		}
	}

	// Add timestamps
	if err == nil {
		message += fmt.Sprintf(". Activation started: %s", startTime.Format(time.RFC3339))
	}
	message += fmt.Sprintf(". Expired at: %s", now.Format(time.RFC3339))

	return message
}

// emitPodCreateActivationEvents emits events for pods that were boosted via PodCreate trigger
// PodCreate boosts are applied in the webhook, so we detect them by checking for pods with
// PodCreate activation timestamps that were recently created
func (r *StartupCPUBoostReconciler) emitPodCreateActivationEvents(ctx context.Context, boost boost.StartupCPUBoost, log logr.Logger) error {
	// List all pods in the boost namespace
	podList := &corev1.PodList{}
	if err := r.Client.List(ctx, podList, client.InNamespace(boost.Namespace())); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	// Filter to pods that match the boost selector
	matchingPods := make([]*corev1.Pod, 0)
	for i := range podList.Items {
		pod := &podList.Items[i]
		if boost.Matches(pod) {
			matchingPods = append(matchingPods, pod)
		}
	}

	now := time.Now()
	// Only emit events for pods created within the last 5 minutes
	// This avoids duplicate events while catching recent PodCreate activations
	recentWindow := 5 * time.Minute

	// For each matching pod, check if it has a PodCreate boost and emit event
	for _, pod := range matchingPods {
		// Skip if pod was created more than 5 minutes ago (avoid duplicate events)
		if pod.CreationTimestamp.Time.Add(recentWindow).Before(now) {
			continue
		}

		// Get pod annotation to check for PodCreate activation
		annotation, err := bpod.BoostAnnotationFromPod(pod)
		if err != nil {
			// Pod doesn't have boost annotation yet, skip
			continue
		}

		// Check if pod has PodCreate activation (lastActivationTime for PodCreate trigger)
		triggerType := autoscaling.BoostTriggerTypePodCreate
		lastActivationTime, exists := annotation.GetLastActivationTime(triggerType)
		if !exists {
			continue
		}

		// Only emit event if activation was recent (within last 5 minutes)
		// This ensures we catch PodCreate activations without emitting duplicates
		if now.Sub(lastActivationTime) > recentWindow {
			continue
		}

		// Check if there's no current activation (meaning this is a fresh PodCreate boost)
		// If there's a current activation, it might be from a runtime trigger, not PodCreate
		currentActivation := annotation.GetCurrentActivation()
		if currentActivation != nil && currentActivation.TriggerType != triggerType {
			// Current activation is from a different trigger, skip
			continue
		}

		// Emit event for PodCreate activation
		eventMessage := fmt.Sprintf("CPU boost '%s' activated for pod '%s' via PodCreate trigger", boost.Name(), pod.Name)
		r.Recorder.Event(pod, corev1.EventTypeNormal, "BoostActivated", eventMessage)
		log.V(5).Info("emitted PodCreate activation event", "pod", pod.Name)
	}

	return nil
}
