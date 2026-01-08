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

// Package pod contains implementation of startup-cpu-boost POD manipulation
// functions
package pod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	autoscaling "github.com/google/kube-startup-cpu-boost/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apiResource "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	BoostLabelKey      = "autoscaling.x-k8s.io/startup-cpu-boost"
	BoostAnnotationKey = "autoscaling.x-k8s.io/startup-cpu-boost"
	EmptyPatchString   = "{}"
	// ActivationStateVersion is the current version of the ActivationState format
	// Increment this when making breaking changes to the state structure
	ActivationStateVersion = "1"
)

type BoostPodAnnotation struct {
	BoostTimestamp  time.Time         `json:"timestamp,omitempty"`
	InitCPURequests map[string]string `json:"initCPURequests,omitempty"`
	InitCPULimits   map[string]string `json:"initCPULimits,omitempty"`

	// ActivationState tracks boost activation state for runtime triggers
	// These fields are optional and maintain backward compatibility
	ActivationState *ActivationState `json:"activationState,omitempty"`
}

// ActivationState tracks the state of boost activations for a pod
// State is stored in pod annotations to survive controller restarts
type ActivationState struct {
	// Version is the version of the ActivationState format
	// Used for future migration and backward compatibility
	// Current version: "1"
	Version string `json:"version,omitempty"`

	// CurrentActivation is the currently active boost activation, if any
	CurrentActivation *ActivationStateEntry `json:"currentActivation,omitempty"`

	// LastActivationTime tracks the last activation time per trigger type
	// Used for cooldown minimum interval enforcement
	LastActivationTime map[string]string `json:"lastActivationTime,omitempty"`

	// ActivationHistory is a list of activation timestamps (RFC3339 format)
	// Used for rate limiting (max activations per hour)
	// Only keeps activations from the last hour
	ActivationHistory []string `json:"activationHistory,omitempty"`

	// LastRestartCounts tracks the last seen restartCount per container
	// Used to detect ContainerRestart trigger activations
	// Key: container name, Value: restartCount (as string)
	LastRestartCounts map[string]int32 `json:"lastRestartCounts,omitempty"`

	// LastConditionStates tracks the last seen condition status per condition type
	// Used to detect PodConditionTransition trigger activations
	// Key: condition type (e.g., "Ready", "PodScheduled"), Value: condition status ("True", "False", "Unknown")
	LastConditionStates map[string]string `json:"lastConditionStates,omitempty"`
}

// ActivationStateEntry represents a single activation state entry
type ActivationStateEntry struct {
	// TriggerType is the type of trigger that activated this boost
	TriggerType autoscaling.BoostTriggerType `json:"triggerType"`

	// StartTime is when the boost was activated (RFC3339 format)
	StartTime string `json:"startTime"`

	// ExpiryConditionType indicates how this activation expires
	ExpiryConditionType string `json:"expiryConditionType"`

	// ExpiryFixedDuration is set when expiry is based on fixed duration (seconds)
	ExpiryFixedDuration *int64 `json:"expiryFixedDuration,omitempty"`

	// ExpiryPodCondition is set when expiry is based on pod condition
	ExpiryPodCondition *PodConditionExpiryEntry `json:"expiryPodCondition,omitempty"`
}

// PodConditionExpiryEntry represents pod condition-based expiry
type PodConditionExpiryEntry struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type mutatePodFunc func(pod *corev1.Pod) error

func NewBoostAnnotation() *BoostPodAnnotation {
	return &BoostPodAnnotation{
		BoostTimestamp:  time.Now(),
		InitCPURequests: make(map[string]string),
		InitCPULimits:   make(map[string]string),
		ActivationState: &ActivationState{
			Version:             ActivationStateVersion,
			LastActivationTime:  make(map[string]string),
			ActivationHistory:   make([]string, 0),
			LastRestartCounts:   make(map[string]int32),
			LastConditionStates: make(map[string]string),
		},
	}
}

func (a BoostPodAnnotation) ToJSON() string {
	result, err := json.Marshal(a)
	if err != nil {
		panic("failed to marshall to JSON: " + err.Error())
	}
	return string(result)
}

func BoostAnnotationFromPod(pod *corev1.Pod) (*BoostPodAnnotation, error) {
	annotation := &BoostPodAnnotation{}
	data, ok := pod.Annotations[BoostAnnotationKey]
	if !ok {
		return nil, errors.New("boost annotation not found")
	}
	if err := json.Unmarshal([]byte(data), annotation); err != nil {
		return nil, err
	}
	// Ensure ActivationState is initialized for backward compatibility
	if annotation.ActivationState == nil {
		annotation.ActivationState = &ActivationState{
			Version:            ActivationStateVersion,
			LastActivationTime: make(map[string]string),
			ActivationHistory:  make([]string, 0),
			LastRestartCounts:  make(map[string]int32),
		}
	}
	// Set version if missing (backward compatibility with old annotations)
	if annotation.ActivationState.Version == "" {
		annotation.ActivationState.Version = ActivationStateVersion
	}
	if annotation.ActivationState.LastActivationTime == nil {
		annotation.ActivationState.LastActivationTime = make(map[string]string)
	}
	if annotation.ActivationState.ActivationHistory == nil {
		annotation.ActivationState.ActivationHistory = make([]string, 0)
	}
	if annotation.ActivationState.LastRestartCounts == nil {
		annotation.ActivationState.LastRestartCounts = make(map[string]int32)
	}
	if annotation.ActivationState.LastConditionStates == nil {
		annotation.ActivationState.LastConditionStates = make(map[string]string)
	}
	return annotation, nil
}

func RevertResourceBoost(pod *corev1.Pod) error {
	if err := revertBoostResources(pod); err != nil {
		return err
	}
	return revertBoostLabels(pod)
}

func revertBoostLabels(pod *corev1.Pod) error {
	delete(pod.Labels, BoostLabelKey)
	delete(pod.Annotations, BoostAnnotationKey)
	return nil
}

func revertBoostResources(pod *corev1.Pod) error {
	annotation, err := BoostAnnotationFromPod(pod)
	if err != nil {
		return fmt.Errorf("failed to get boost annotation from pod: %s", err)
	}
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		if request, ok := annotation.InitCPURequests[container.Name]; ok {
			if reqQuantity, err := apiResource.ParseQuantity(request); err == nil {
				if container.Resources.Requests == nil {
					container.Resources.Requests = corev1.ResourceList{}
				}
				container.Resources.Requests[corev1.ResourceCPU] = reqQuantity
			} else {
				return fmt.Errorf("failed to parse CPU request: %s", err)
			}
		}
		if limit, ok := annotation.InitCPULimits[container.Name]; ok {
			if limitQuantity, err := apiResource.ParseQuantity(limit); err == nil {
				if container.Resources.Limits == nil {
					container.Resources.Limits = corev1.ResourceList{}
				}
				container.Resources.Limits[corev1.ResourceCPU] = limitQuantity
			} else {
				return fmt.Errorf("failed to parse CPU limit: %s", err)
			}
		}
	}
	return nil
}

func buildPodPatch(pod *corev1.Pod, mutatePodFunc mutatePodFunc) ([]byte, error) {
	podJSON, err := json.Marshal(pod)
	if err != nil {
		return nil, err
	}
	if err := mutatePodFunc(pod); err != nil {
		return nil, err
	}
	updatedPodJSON, err := json.Marshal(pod)
	if err != nil {
		return nil, err
	}
	return jsonpatch.CreateMergePatch(podJSON, updatedPodJSON)

}

func NewRevertBoostLabelsPatch() client.Patch {
	return &revertBoostLabelsPatch{}
}

type revertBoostLabelsPatch struct {
}

func (p *revertBoostLabelsPatch) Type() types.PatchType {
	return types.MergePatchType
}

func (p *revertBoostLabelsPatch) Data(obj client.Object) ([]byte, error) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil, errors.New("revertBoostLabelsPatch applies only on *corev1.Pod objects")
	}
	return buildPodPatch(pod, revertBoostLabels)
}

func NewRevertBootsResourcesPatch() client.Patch {
	return &revertBoostResourcesPatch{}
}

type revertBoostResourcesPatch struct {
}

func (p *revertBoostResourcesPatch) Type() types.PatchType {
	return types.MergePatchType
}

func (p *revertBoostResourcesPatch) Data(obj client.Object) ([]byte, error) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil, errors.New("revertBoostResourcesPatch applies only on *corev1.Pod objects")
	}
	patchData, err := buildPodPatch(pod, revertBoostResources)
	if err != nil {
		return []byte(EmptyPatchString), nil
	}
	return patchData, nil
}

// GetActivationState returns the activation state, initializing it if needed
func (a *BoostPodAnnotation) GetActivationState() *ActivationState {
	if a.ActivationState == nil {
		a.ActivationState = &ActivationState{
			Version:            ActivationStateVersion,
			LastActivationTime: make(map[string]string),
			ActivationHistory:  make([]string, 0),
			LastRestartCounts:  make(map[string]int32),
		}
	}
	// Set version if missing (backward compatibility)
	if a.ActivationState.Version == "" {
		a.ActivationState.Version = ActivationStateVersion
	}
	if a.ActivationState.LastActivationTime == nil {
		a.ActivationState.LastActivationTime = make(map[string]string)
	}
	if a.ActivationState.ActivationHistory == nil {
		a.ActivationState.ActivationHistory = make([]string, 0)
	}
	if a.ActivationState.LastRestartCounts == nil {
		a.ActivationState.LastRestartCounts = make(map[string]int32)
	}
	if a.ActivationState.LastConditionStates == nil {
		a.ActivationState.LastConditionStates = make(map[string]string)
	}
	return a.ActivationState
}

// SetCurrentActivation sets the current active boost activation
func (a *BoostPodAnnotation) SetCurrentActivation(triggerType autoscaling.BoostTriggerType, startTime time.Time, expiryType string, expiryFixedDuration *int64, expiryPodCondition *PodConditionExpiryEntry) {
	state := a.GetActivationState()
	state.CurrentActivation = &ActivationStateEntry{
		TriggerType:         triggerType,
		StartTime:           startTime.Format(time.RFC3339),
		ExpiryConditionType: expiryType,
		ExpiryFixedDuration: expiryFixedDuration,
		ExpiryPodCondition:  expiryPodCondition,
	}
}

// ClearCurrentActivation clears the current active boost activation
func (a *BoostPodAnnotation) ClearCurrentActivation() {
	state := a.GetActivationState()
	state.CurrentActivation = nil
}

// GetCurrentActivation returns the current active activation, if any
func (a *BoostPodAnnotation) GetCurrentActivation() *ActivationStateEntry {
	if a.ActivationState == nil {
		return nil
	}
	return a.ActivationState.CurrentActivation
}

// IsActivationExpired checks if the stored activation has expired based on the pod's current state
// This is used to determine if the active boost state should be cleared
func (a *BoostPodAnnotation) IsActivationExpired(pod *corev1.Pod) bool {
	activation := a.GetCurrentActivation()
	if activation == nil {
		return false // No active activation
	}

	// Validate start time format (but we don't use it for expiry calculation)
	_, err := time.Parse(time.RFC3339, activation.StartTime)
	if err != nil {
		// Invalid start time, consider expired
		return true
	}

	// Check expiry based on type
	switch activation.ExpiryConditionType {
	case "FixedDuration":
		if activation.ExpiryFixedDuration == nil {
			return false // No expiry duration set, never expires
		}
		// Fixed duration is based on pod scheduled time (not activation start time)
		// This matches the behavior of BoostActivation.IsExpired
		if pod.Status.StartTime == nil {
			return false
		}
		elapsed := time.Since(pod.Status.StartTime.Time)
		duration := time.Duration(*activation.ExpiryFixedDuration) * time.Second
		return elapsed >= duration
	case "PodCondition":
		if activation.ExpiryPodCondition == nil {
			return false // No expiry condition set, never expires
		}
		// Check if pod condition matches
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodConditionType(activation.ExpiryPodCondition.Type) {
				expectedStatus := corev1.ConditionStatus(activation.ExpiryPodCondition.Status)
				return condition.Status == expectedStatus
			}
		}
		return false // Condition not found or doesn't match
	default:
		// Unknown expiry type, don't expire
		return false
	}
}

// GetLastActivationTime returns the last activation time for a given trigger type
// Returns (time, true) if found and valid, (zero time, false) if not found or invalid
// Invalid timestamps (malformed or future) are treated as not found
func (a *BoostPodAnnotation) GetLastActivationTime(triggerType autoscaling.BoostTriggerType) (time.Time, bool) {
	state := a.GetActivationState()
	timeStr, ok := state.LastActivationTime[string(triggerType)]
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		// Malformed timestamp - treat as not found
		return time.Time{}, false
	}
	// Check for future timestamps (clock skew or manual corruption)
	// Future timestamps are treated as invalid to prevent bypassing cooldown
	now := time.Now()
	if t.After(now) {
		// Future timestamp detected - treat as invalid
		return time.Time{}, false
	}
	return t, true
}

// SetLastActivationTime sets the last activation time for a given trigger type
func (a *BoostPodAnnotation) SetLastActivationTime(triggerType autoscaling.BoostTriggerType, activationTime time.Time) {
	state := a.GetActivationState()
	state.LastActivationTime[string(triggerType)] = activationTime.Format(time.RFC3339)
}

// AddActivationToHistory adds an activation timestamp to the history
// and removes activations older than 1 hour
// Limits annotation size to max 100 timestamps to prevent unbounded growth
func (a *BoostPodAnnotation) AddActivationToHistory(activationTime time.Time) {
	state := a.GetActivationState()
	now := time.Now()
	cutoffTime := now.Add(-1 * time.Hour)
	const maxHistorySize = 100

	// Add new activation
	state.ActivationHistory = append(state.ActivationHistory, activationTime.Format(time.RFC3339))

	// Remove activations older than 1 hour and malformed timestamps
	filtered := make([]string, 0, len(state.ActivationHistory))
	for _, timeStr := range state.ActivationHistory {
		t, err := time.Parse(time.RFC3339, timeStr)
		// Only keep valid timestamps that are within the last hour
		// Skip malformed timestamps and future timestamps (clock skew protection)
		if err == nil && t.After(cutoffTime) && !t.After(now) {
			filtered = append(filtered, timeStr)
		}
	}
	state.ActivationHistory = filtered

	// Limit annotation size to prevent unbounded growth
	// If we exceed maxHistorySize, keep only the most recent entries
	if len(state.ActivationHistory) > maxHistorySize {
		// Keep the most recent maxHistorySize entries
		state.ActivationHistory = state.ActivationHistory[len(state.ActivationHistory)-maxHistorySize:]
	}
}

// GetActivationHistoryCount returns the number of activations in the last hour
// Defensively filters out old, malformed, or future timestamps
func (a *BoostPodAnnotation) GetActivationHistoryCount() int {
	if a.ActivationState == nil || a.ActivationState.ActivationHistory == nil {
		return 0
	}
	now := time.Now()
	cutoffTime := now.Add(-1 * time.Hour)

	// Defensively filter timestamps to handle edge cases:
	// - Old timestamps that weren't cleaned up
	// - Malformed timestamps
	// - Future timestamps (clock skew)
	count := 0
	for _, timeStr := range a.ActivationState.ActivationHistory {
		t, err := time.Parse(time.RFC3339, timeStr)
		if err == nil && t.After(cutoffTime) && !t.After(now) {
			count++
		}
	}
	return count
}

// ShouldSkipDueToCooldown checks if activation should be skipped due to cooldown policy
// Returns (shouldSkip, reason) where reason is empty if shouldSkip is false
func (a *BoostPodAnnotation) ShouldSkipDueToCooldown(triggerType autoscaling.BoostTriggerType, cooldownPolicy *autoscaling.CooldownPolicy) (bool, string) {
	if cooldownPolicy == nil {
		return false, ""
	}

	now := time.Now()

	// Check minimum interval
	if cooldownPolicy.MinIntervalSeconds != nil && *cooldownPolicy.MinIntervalSeconds > 0 {
		lastActivationTime, exists := a.GetLastActivationTime(triggerType)
		if exists {
			elapsed := now.Sub(lastActivationTime)
			// Handle edge case: negative elapsed time (future timestamp)
			// This can happen due to clock skew or manual annotation corruption
			// Treat as invalid timestamp - allow activation
			if elapsed < 0 {
				// Future timestamp detected - treat as no previous activation
				return false, ""
			}
			// Check for overflow: very large intervals could cause issues
			// Max int32 seconds = ~68 years, which is reasonable
			minInterval := time.Duration(*cooldownPolicy.MinIntervalSeconds) * time.Second
			if elapsed < minInterval {
				remaining := minInterval - elapsed
				return true, fmt.Sprintf("minimum interval not met (remaining: %v)", remaining.Round(time.Second))
			}
		}
	}

	// Check maximum activations per hour
	if cooldownPolicy.MaxActivationsPerHour != nil && *cooldownPolicy.MaxActivationsPerHour > 0 {
		activationCount := a.GetActivationHistoryCount()
		if activationCount >= int(*cooldownPolicy.MaxActivationsPerHour) {
			return true, fmt.Sprintf("maximum activations per hour exceeded (%d/%d)", activationCount, *cooldownPolicy.MaxActivationsPerHour)
		}
	}

	return false, ""
}

// GetLastRestartCount returns the last seen restartCount for a container
func (a *BoostPodAnnotation) GetLastRestartCount(containerName string) (int32, bool) {
	state := a.GetActivationState()
	count, ok := state.LastRestartCounts[containerName]
	return count, ok
}

// SetLastRestartCount sets the last seen restartCount for a container
func (a *BoostPodAnnotation) SetLastRestartCount(containerName string, restartCount int32) {
	state := a.GetActivationState()
	state.LastRestartCounts[containerName] = restartCount
}

// UpdateLastRestartCounts updates the last seen restartCounts from pod status
// Returns a map of containers whose restartCount increased
func (a *BoostPodAnnotation) UpdateLastRestartCounts(pod *corev1.Pod) map[string]int32 {
	state := a.GetActivationState()
	incremented := make(map[string]int32)

	for _, containerStatus := range pod.Status.ContainerStatuses {
		containerName := containerStatus.Name
		currentCount := containerStatus.RestartCount
		lastCount, exists := state.LastRestartCounts[containerName]

		// If restartCount increased, record it
		if !exists || currentCount > lastCount {
			incremented[containerName] = currentCount
		}

		// Always update the last seen count
		state.LastRestartCounts[containerName] = currentCount
	}

	return incremented
}

// GetLastConditionState returns the last seen condition status for a condition type
func (a *BoostPodAnnotation) GetLastConditionState(conditionType string) (string, bool) {
	state := a.GetActivationState()
	status, ok := state.LastConditionStates[conditionType]
	return status, ok
}

// SetLastConditionState sets the last seen condition status for a condition type
func (a *BoostPodAnnotation) SetLastConditionState(conditionType string, status string) {
	state := a.GetActivationState()
	state.LastConditionStates[conditionType] = status
}

// UpdateLastConditionStates updates the last seen condition states from pod status
// Returns a map of condition types that transitioned (from old status to new status)
// Key: condition type, Value: transition info (fromStatus -> toStatus)
type ConditionTransition struct {
	FromStatus string
	ToStatus   string
}

func (a *BoostPodAnnotation) UpdateLastConditionStates(pod *corev1.Pod) map[string]ConditionTransition {
	state := a.GetActivationState()
	transitions := make(map[string]ConditionTransition)

	// Defensive check: handle nil or empty conditions
	if pod == nil || pod.Status.Conditions == nil {
		return transitions
	}

	for _, condition := range pod.Status.Conditions {
		conditionType := string(condition.Type)
		currentStatus := string(condition.Status)
		lastStatus, exists := state.LastConditionStates[conditionType]

		// If condition status changed, record the transition
		if !exists || currentStatus != lastStatus {
			transitions[conditionType] = ConditionTransition{
				FromStatus: lastStatus,
				ToStatus:   currentStatus,
			}
			// Update the stored state
			state.LastConditionStates[conditionType] = currentStatus
		}
	}

	return transitions
}

// applyBoostResourcesToPod applies boost resources to a pod (mutates pod in-place)
// This is a helper function used by patch functions
func applyBoostResourcesToPod(pod *corev1.Pod, annotation *BoostPodAnnotation, getResourcePolicy func(containerName string) (func(context.Context, *corev1.Container) *corev1.ResourceRequirements, bool), removeLimits bool, podLevelResourcesEnabled bool) error {
	// Store original resources in annotation if not already stored
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		containerName := container.Name

		// Store original resources if not already stored
		if _, exists := annotation.InitCPURequests[containerName]; !exists {
			if cpuRequests, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
				annotation.InitCPURequests[containerName] = cpuRequests.String()
			}
		}
		if _, exists := annotation.InitCPULimits[containerName]; !exists {
			if cpuLimits, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
				annotation.InitCPULimits[containerName] = cpuLimits.String()
			}
		}

		// Get resource policy for this container
		newResourcesFunc, ok := getResourcePolicy(containerName)
		if !ok {
			continue
		}

		// Check if resize requires restart
		if resizeRequiresRestart(*container, corev1.ResourceCPU) {
			continue
		}

		// Check if container has resources to increase
		if !hasResourcesToIncrease(*container) {
			continue
		}

		// Calculate new resources
		newResources := newResourcesFunc(context.Background(), container)

		// Apply new resources
		if newResources.Requests != nil {
			if container.Resources.Requests == nil {
				container.Resources.Requests = corev1.ResourceList{}
			}
			for k, v := range newResources.Requests {
				container.Resources.Requests[k] = v
			}
		}
		if newResources.Limits != nil {
			if container.Resources.Limits == nil {
				container.Resources.Limits = corev1.ResourceList{}
			}
			// Handle limit removal for burstable/besteffort pods
			if removeLimits {
				originalQosClass := computePodQOSForBoost(pod, podLevelResourcesEnabled)
				if originalQosClass == corev1.PodQOSBurstable || originalQosClass == corev1.PodQOSBestEffort {
					delete(container.Resources.Limits, corev1.ResourceCPU)
				} else {
					for k, v := range newResources.Limits {
						container.Resources.Limits[k] = v
					}
				}
			} else {
				for k, v := range newResources.Limits {
					container.Resources.Limits[k] = v
				}
			}
		}
	}
	return nil
}

// ApplyBoostLabelsAndAnnotations applies boost labels and annotations to a pod
func ApplyBoostLabelsAndAnnotations(pod *corev1.Pod, annotation *BoostPodAnnotation, boostName string) {
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[BoostAnnotationKey] = annotation.ToJSON()
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[BoostLabelKey] = boostName
}

// Helper function to check if resize requires restart
func resizeRequiresRestart(c corev1.Container, r corev1.ResourceName) bool {
	for _, p := range c.ResizePolicy {
		if p.ResourceName != r {
			continue
		}
		return p.RestartPolicy == corev1.RestartContainer
	}
	return false
}

// Helper function to check if container has resources to increase
func hasResourcesToIncrease(c corev1.Container) bool {
	return !c.Resources.Requests.Cpu().IsZero() || !c.Resources.Limits.Cpu().IsZero()
}

// computePodQOSForBoost computes pod QOS class for boost application
// This is a simplified version that doesn't require the full webhook context
func computePodQOSForBoost(pod *corev1.Pod, podLevelResourcesEnabled bool) corev1.PodQOSClass {
	// Simplified QOS computation - we only need to distinguish burstable/besteffort
	// for limit removal logic
	hasRequests := false
	hasLimits := false

	if podLevelResourcesEnabled && pod.Spec.Resources != nil {
		if len(pod.Spec.Resources.Requests) > 0 {
			hasRequests = true
		}
		if len(pod.Spec.Resources.Limits) > 0 {
			hasLimits = true
		}
	} else {
		for _, container := range pod.Spec.Containers {
			if len(container.Resources.Requests) > 0 {
				hasRequests = true
			}
			if len(container.Resources.Limits) > 0 {
				hasLimits = true
			}
		}
	}

	if !hasRequests && !hasLimits {
		return corev1.PodQOSBestEffort
	}
	return corev1.PodQOSBurstable
}

// NewApplyBoostResourcesPatch creates a patch for applying boost resources via /resize subresource
func NewApplyBoostResourcesPatch(annotation *BoostPodAnnotation, getResourcePolicy func(containerName string) (func(context.Context, *corev1.Container) *corev1.ResourceRequirements, bool), removeLimits bool, podLevelResourcesEnabled bool) client.Patch {
	return &applyBoostResourcesPatch{
		annotation:               annotation,
		getResourcePolicy:        getResourcePolicy,
		removeLimits:             removeLimits,
		podLevelResourcesEnabled: podLevelResourcesEnabled,
	}
}

type applyBoostResourcesPatch struct {
	annotation               *BoostPodAnnotation
	getResourcePolicy        func(containerName string) (func(context.Context, *corev1.Container) *corev1.ResourceRequirements, bool)
	removeLimits             bool
	podLevelResourcesEnabled bool
}

func (p *applyBoostResourcesPatch) Type() types.PatchType {
	return types.MergePatchType
}

func (p *applyBoostResourcesPatch) Data(obj client.Object) ([]byte, error) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil, errors.New("applyBoostResourcesPatch applies only on *corev1.Pod objects")
	}
	patchData, err := buildPodPatch(pod, func(pod *corev1.Pod) error {
		return applyBoostResourcesToPod(pod, p.annotation, p.getResourcePolicy, p.removeLimits, p.podLevelResourcesEnabled)
	})
	if err != nil {
		return []byte(EmptyPatchString), nil
	}
	return patchData, nil
}

// NewApplyBoostLabelsPatch creates a patch for applying boost labels and annotations
func NewApplyBoostLabelsPatch(annotation *BoostPodAnnotation, boostName string) client.Patch {
	return &applyBoostLabelsPatch{
		annotation: annotation,
		boostName:  boostName,
	}
}

type applyBoostLabelsPatch struct {
	annotation *BoostPodAnnotation
	boostName  string
}

func (p *applyBoostLabelsPatch) Type() types.PatchType {
	return types.MergePatchType
}

func (p *applyBoostLabelsPatch) Data(obj client.Object) ([]byte, error) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil, errors.New("applyBoostLabelsPatch applies only on *corev1.Pod objects")
	}
	return buildPodPatch(pod, func(pod *corev1.Pod) error {
		ApplyBoostLabelsAndAnnotations(pod, p.annotation, p.boostName)
		return nil
	})
}
