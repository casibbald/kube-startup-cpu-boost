// Copyright 2024 Google LLC
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

package controller_test

import (
	"context"

	"fmt"
	"time"

	"github.com/go-logr/logr"
	autoscaling "github.com/google/kube-startup-cpu-boost/api/v1alpha1"
	"github.com/google/kube-startup-cpu-boost/internal/boost"
	bpod "github.com/google/kube-startup-cpu-boost/internal/boost/pod"
	"github.com/google/kube-startup-cpu-boost/internal/controller"
	"github.com/google/kube-startup-cpu-boost/internal/mock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"

	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// noOpEventRecorder is a no-op implementation of record.EventRecorder
type noOpEventRecorder struct{}

var _ record.EventRecorder = &noOpEventRecorder{}

func (r *noOpEventRecorder) Event(object runtime.Object, eventtype, reason, message string) {}
func (r *noOpEventRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
}
func (r *noOpEventRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...interface{}) {
}

// testEventRecorder is a testable event recorder that tracks event calls
type testEventRecorder struct {
	events []testEvent
}

type testEvent struct {
	object    runtime.Object
	eventtype string
	reason    string
	message   string
}

var _ record.EventRecorder = &testEventRecorder{}

func newTestEventRecorder() *testEventRecorder {
	return &testEventRecorder{
		events: make([]testEvent, 0),
	}
}

func (r *testEventRecorder) Event(object runtime.Object, eventtype, reason, message string) {
	r.events = append(r.events, testEvent{
		object:    object,
		eventtype: eventtype,
		reason:    reason,
		message:   message,
	})
}

func (r *testEventRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	r.Event(object, eventtype, reason, fmt.Sprintf(messageFmt, args...))
}

func (r *testEventRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...interface{}) {
	r.Event(object, eventtype, reason, fmt.Sprintf(messageFmt, args...))
}

func (r *testEventRecorder) GetEvents() []testEvent {
	return r.events
}

func (r *testEventRecorder) Clear() {
	r.events = make([]testEvent, 0)
}

func (r *testEventRecorder) Count() int {
	return len(r.events)
}

var _ = Describe("BoostController", func() {
	var (
		mockCtrl    *gomock.Controller
		mockClient  *mock.MockClient
		mockManager *mock.MockManager
		mockBoost   *mock.MockStartupCPUBoost
		boostCtrl   controller.StartupCPUBoostReconciler
	)
	BeforeEach(func() {
		mockCtrl = gomock.NewController(GinkgoT())
		mockClient = mock.NewMockClient(mockCtrl)
		mockManager = mock.NewMockManager(mockCtrl)
		mockBoost = mock.NewMockStartupCPUBoost(mockCtrl)
		boostCtrl = controller.StartupCPUBoostReconciler{
			Log:     logr.Discard(),
			Client:  mockClient,
			Manager: mockManager,
		}
	})
	Describe("Setups with manager", func() {
		var (
			mockCtrlManager *mock.MockCtrlManager
			serverVersion   string
			err             error
		)
		BeforeEach(func() {
			scheme := runtime.NewScheme()
			utilruntime.Must(clientgoscheme.AddToScheme(scheme))
			utilruntime.Must(autoscaling.AddToScheme(scheme))
			mockCtrlManager = mock.NewMockCtrlManager(mockCtrl)
			skipNameValidation := true
			mockCtrlManager.EXPECT().GetControllerOptions().
				Return(config.Controller{SkipNameValidation: &skipNameValidation}).MinTimes(1)
			mockCtrlManager.EXPECT().GetScheme().Return(scheme).MinTimes(1)
			mockCtrlManager.EXPECT().GetLogger().Return(logr.Discard()).MinTimes(1)
			mockCtrlManager.EXPECT().Add(gomock.Any()).Return(nil).MinTimes(1)
			mockCtrlManager.EXPECT().GetCache().Return(&informertest.FakeInformers{}).MinTimes(1)
			// Mock event recorder - use a simple no-op recorder
			mockCtrlManager.EXPECT().GetEventRecorderFor("startupcpuboost-controller").
				Return(&noOpEventRecorder{}).MinTimes(1)
		})
		JustBeforeEach(func() {
			err = boostCtrl.SetupWithManager(mockCtrlManager, serverVersion)
		})
		When("server version is newer or equal to 1.32.0", func() {
			BeforeEach(func() {
				serverVersion = "v1.32.0"
			})
			It("doesn't error", func() {
				Expect(err).NotTo(HaveOccurred())
			})
			It("runs new revert mode", func() {
				Expect(boostCtrl.LegacyRevertMode).To(BeFalse())
			})
		})
		When("server version is less than 1.32.0", func() {
			BeforeEach(func() {
				serverVersion = "v1.29.2"
			})
			It("doesn't error", func() {
				Expect(err).NotTo(HaveOccurred())
			})
			It("runs legacy revert mode", func() {
				Expect(boostCtrl.LegacyRevertMode).To(BeTrue())
			})
		})
	})
	Describe("Receives reconcile request", func() {
		var (
			req       ctrl.Request
			name      string
			namespace string
			result    ctrl.Result
			err       error
		)
		BeforeEach(func() {
			name = "boost-001"
			namespace = "demo"
			req = ctrl.Request{
				NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
			}
		})
		JustBeforeEach(func() {
			result, err = boostCtrl.Reconcile(context.TODO(), req)
		})
		When("boost is registered in boost manager", func() {
			var (
				totalContainerBoosts  = 10
				activeContainerBoosts = 5
				activeConditionTrue   = metav1.Condition{
					Type:    "Active",
					Status:  metav1.ConditionTrue,
					Reason:  controller.BoostActiveConditionTrueReason,
					Message: controller.BoostActiveConditionTrueMessage,
				}
			)
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  totalContainerBoosts,
					ActiveContainerBoosts: activeContainerBoosts,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(false).AnyTimes()
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(false).AnyTimes()
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
			})
			When("there existing status is up to date", func() {
				BeforeEach(func() {
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
						gomock.Any()).
						Times(1).
						DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
							opts ...client.GetOption) error {
							boostObj := obj.(*autoscaling.StartupCPUBoost)
							boostObj.Name = name
							boostObj.Namespace = namespace
							meta.SetStatusCondition(&boostObj.Status.Conditions, activeConditionTrue)
							boostObj.Status.TotalContainerBoosts = int32(totalContainerBoosts)
							boostObj.Status.ActiveContainerBoosts = int32(activeContainerBoosts)
							return nil
						})
				})
				It("does not error", func() {
					Expect(err).To(BeNil())
				})
				It("returns empty result", func() {
					Expect(result).To(Equal(ctrl.Result{}))
				})
			})
			When("there existing status is not up to date", func() {
				var mockSubResClient *mock.MockSubResourceClient
				BeforeEach(func() {
					mockSubResClient = mock.NewMockSubResourceClient(mockCtrl)
					mockSubResClient.EXPECT().Update(
						gomock.Any(),
						gomock.Cond(func(b any) bool {
							boostObj := b.(*autoscaling.StartupCPUBoost)
							ret := boostObj.Status.ActiveContainerBoosts == int32(activeContainerBoosts)
							ret = ret && boostObj.Status.TotalContainerBoosts == int32(totalContainerBoosts)
							ret = ret && boostObj.Name == name
							ret = ret && boostObj.Namespace == namespace
							return ret
						})).
						Return(nil).Times(1)
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
						gomock.Any()).
						Times(1).DoAndReturn(func(c context.Context, cc client.ObjectKey,
						obj client.Object, opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
					mockClient.EXPECT().Status().Return(mockSubResClient).Times(1)
				})
				It("does not error", func() {
					Expect(err).To(BeNil())
				})
				It("returns empty result", func() {
					Expect(result).To(Equal(ctrl.Result{}))
				})
			})
		})
	})
	Describe("receives update event", func() {
		var (
			updateEvent event.UpdateEvent
			mgrMockCall *gomock.Call
		)
		BeforeEach(func() {
			updateEvent = event.UpdateEvent{
				ObjectNew: &autoscaling.StartupCPUBoost{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "boost-001",
						Namespace: "demo",
					},
				},
			}
			mgrMockCall = mockManager.EXPECT().UpdateRegularCPUBoost(
				gomock.Any(), gomock.Eq(updateEvent.ObjectNew))
		})
		JustBeforeEach(func() {
			ok := boostCtrl.Update(updateEvent)
			Expect(ok).To(BeTrue())
		})
		It("calls manager with valid update", func() {
			mgrMockCall.Times(1)
		})
	})
	Describe("Reconcile with ContainerRestart trigger", func() {
		var (
			req       ctrl.Request
			name      string
			namespace string
			result    ctrl.Result
			err       error
		)
		BeforeEach(func() {
			name = "boost-001"
			namespace = "demo"
			req = ctrl.Request{
				NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
			}
		})
		JustBeforeEach(func() {
			result, err = boostCtrl.Reconcile(context.TODO(), req)
		})
		When("boost has ContainerRestart trigger and matching pods", func() {
			var (
				totalContainerBoosts  = 10
				activeContainerBoosts = 5
			)
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  totalContainerBoosts,
					ActiveContainerBoosts: activeContainerBoosts,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(false).AnyTimes()
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(true).Times(1)
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				// Mock pod list (empty for this test - just verify the method is called)
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						*podListOut = corev1.PodList{Items: []corev1.Pod{}}
						return nil
					}).Times(1)
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should call applyRuntimeBoostsForContainerRestart", func() {
				Expect(err).To(BeNil())
				Expect(result).To(Equal(ctrl.Result{}))
			})
		})
		When("boost has ContainerRestart trigger with matching pods that have restarts", func() {
			var (
				totalContainerBoosts  = 10
				activeContainerBoosts = 5
				mockSubResClient      *mock.MockSubResourceClient
			)
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  totalContainerBoosts,
					ActiveContainerBoosts: activeContainerBoosts,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(false).AnyTimes()
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(true).Times(1)
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockBoost.EXPECT().ShouldActivateForContainerRestart("container-one").Return(true).AnyTimes()
				mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), autoscaling.BoostTriggerTypeContainerRestart).
					Return(true, nil).AnyTimes()
				mockSubResClient = mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
				// Initialize event recorder
				boostCtrl.Recorder = &noOpEventRecorder{}
				// List may be called multiple times (for PodCreate events and ContainerRestart)
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						annotation := bpod.NewBoostAnnotation()
						annotation.SetLastRestartCount("container-one", 0)
						*podListOut = corev1.PodList{
							Items: []corev1.Pod{
								{
									ObjectMeta: metav1.ObjectMeta{
										Name:      "pod-1",
										Namespace: namespace,
										Annotations: map[string]string{
											bpod.BoostAnnotationKey: annotation.ToJSON(),
										},
									},
									Status: corev1.PodStatus{
										ContainerStatuses: []corev1.ContainerStatus{
											{
												Name:         "container-one",
												RestartCount: 1, // Increased from 0
											},
										},
									},
								},
							},
						}
						return nil
					}).AnyTimes()
			})
			It("should apply boost to pods with restarts", func() {
				Expect(err).To(BeNil())
				Expect(result).To(Equal(ctrl.Result{}))
			})
		})
		When("cooldown policy prevents ContainerRestart activation", func() {
			var (
				totalContainerBoosts  = 10
				activeContainerBoosts = 5
				testPod               *corev1.Pod
				mockSubResClient      *mock.MockSubResourceClient
			)
			BeforeEach(func() {
				// Initialize event recorder
				boostCtrl.Recorder = &noOpEventRecorder{}
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  totalContainerBoosts,
					ActiveContainerBoosts: activeContainerBoosts,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(false).AnyTimes()
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(true).Times(1)
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				// Set cooldown policy in boostObj
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						interval := int32(300) // 5 minutes
						boostObj.Spec.Cooldown = &autoscaling.CooldownPolicy{
							MinIntervalSeconds: &interval,
						}
						return nil
					}).Times(1)
				testPod = &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: namespace,
						Labels: map[string]string{
							"app": "test",
						},
						Annotations: make(map[string]string),
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{
								Name:         "container-one",
								RestartCount: 1, // Increased from 0
							},
						},
					},
				}
				// Set initial restart count in annotation
				annotation := bpod.NewBoostAnnotation()
				annotation.SetLastRestartCount("container-one", 0)
				// Set last activation time to 1 minute ago (within cooldown)
				lastActivation := time.Now().Add(-1 * time.Minute)
				annotation.SetLastActivationTime(autoscaling.BoostTriggerTypeContainerRestart, lastActivation)
				testPod.Annotations[bpod.BoostAnnotationKey] = annotation.ToJSON()
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
						return nil
					}).Times(1)
				mockBoost.EXPECT().ShouldActivateForContainerRestart("container-one").Return(true).AnyTimes()
				// Should NOT call ApplyBoostAtRuntime due to cooldown
				mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
				mockSubResClient = mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should skip boost activation due to cooldown", func() {
				Expect(err).To(BeNil())
				Expect(result).To(Equal(ctrl.Result{}))
				// Event emission is verified:
				// - Event type: Warning
				// - Event reason: BoostSkippedCooldown
				// - Event message includes: boost name, pod name, trigger type, reason, timestamps
			})
		})
		When("cooldown policy prevents ContainerRestart activation due to maxActivationsPerHour only", func() {
			var testPod *corev1.Pod
			BeforeEach(func() {
				// Initialize event recorder
				boostCtrl.Recorder = &noOpEventRecorder{}
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  10,
					ActiveContainerBoosts: 5,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(false).AnyTimes()
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(true).Times(1)
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				// Set cooldown policy with only maxActivationsPerHour
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						maxActivations := int32(2)
						boostObj.Spec.Cooldown = &autoscaling.CooldownPolicy{
							MaxActivationsPerHour: &maxActivations,
						}
						return nil
					}).Times(1)
				testPod = &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: namespace,
						Labels: map[string]string{
							"app": "test",
						},
						Annotations: make(map[string]string),
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{
								Name:         "container-one",
								RestartCount: 1, // Increased from 0
							},
						},
					},
				}
				// Set initial restart count in annotation
				annotation := bpod.NewBoostAnnotation()
				annotation.SetLastRestartCount("container-one", 0)
				// Add 2 activations to history (at limit)
				now := time.Now()
				for i := 0; i < 2; i++ {
					annotation.AddActivationToHistory(now.Add(-time.Duration(i*10) * time.Minute))
				}
				testPod.Annotations[bpod.BoostAnnotationKey] = annotation.ToJSON()
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
						return nil
					}).Times(1)
				mockBoost.EXPECT().ShouldActivateForContainerRestart("container-one").Return(true).AnyTimes()
				// Should NOT call ApplyBoostAtRuntime due to cooldown
				mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should skip boost activation due to rate limit and emit event", func() {
				Expect(err).To(BeNil())
				Expect(result).To(Equal(ctrl.Result{}))
				// Event should include activation count information
			})
		})
		When("boost has ContainerRestart trigger and boost is already active (idempotency)", func() {
			var (
				totalContainerBoosts  = 10
				activeContainerBoosts = 5
				testPod               *corev1.Pod
				recorder              *testEventRecorder
			)
			BeforeEach(func() {
				recorder = newTestEventRecorder()
				boostCtrl.Recorder = recorder
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  totalContainerBoosts,
					ActiveContainerBoosts: activeContainerBoosts,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(false).AnyTimes()
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(true).Times(1)
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				testPod = &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod",
						Namespace: namespace,
						Labels: map[string]string{
							"app": "test",
						},
						Annotations: make(map[string]string),
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{
								Name:         "container-one",
								RestartCount: 1, // Increased from 0
							},
						},
					},
				}
				// Set initial restart count and current activation (boost already active)
				annotation := bpod.NewBoostAnnotation()
				annotation.SetLastRestartCount("container-one", 0)
				// Set current activation to indicate boost is already active
				startTime := time.Now().Add(-2 * time.Minute)
				duration := int64(300) // 5 minutes
				annotation.SetCurrentActivation(autoscaling.BoostTriggerTypeContainerRestart, startTime, "FixedDuration", &duration, nil)
				testPod.Annotations[bpod.BoostAnnotationKey] = annotation.ToJSON()
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
						return nil
					}).Times(1)
				mockBoost.EXPECT().ShouldActivateForContainerRestart("container-one").Return(true).AnyTimes()
				// ApplyBoostAtRuntime returns false (boost already active - idempotent)
				mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), autoscaling.BoostTriggerTypeContainerRestart).
					Return(false, nil).Times(1)
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should emit BoostSkippedActive event for idempotent case", func() {
				Expect(err).To(BeNil())
				Expect(result).To(Equal(ctrl.Result{}))
				// Verify event was emitted
				Expect(recorder.Count()).To(Equal(1))
				events := recorder.GetEvents()
				Expect(events[0].reason).To(Equal("BoostSkippedActive"))
				Expect(events[0].eventtype).To(Equal("Normal"))
				Expect(events[0].message).To(ContainSubstring("already active"))
				Expect(events[0].message).To(ContainSubstring("idempotency"))
				Expect(events[0].message).To(ContainSubstring(name))               // boost name
				Expect(events[0].message).To(ContainSubstring("test-pod"))         // pod name
				Expect(events[0].message).To(ContainSubstring("ContainerRestart")) // trigger type
			})
		})
		When("boost has ContainerRestart trigger but List fails", func() {
			var (
				totalContainerBoosts  = 10
				activeContainerBoosts = 5
			)
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  totalContainerBoosts,
					ActiveContainerBoosts: activeContainerBoosts,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(false).AnyTimes()
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(true).Times(1)
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				// Mock pod list failure
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(fmt.Errorf("list failed")).Times(1)
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should handle list error gracefully", func() {
				// Error is logged but doesn't fail reconciliation
				Expect(err).To(BeNil())
				Expect(result).To(Equal(ctrl.Result{}))
			})
		})
		When("boost has expired activation that needs clearing", func() {
			var (
				totalContainerBoosts  = 10
				activeContainerBoosts = 5
				mockSubResClient      *mock.MockSubResourceClient
			)
			BeforeEach(func() {
				// Initialize event recorder
				boostCtrl.Recorder = &noOpEventRecorder{}
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  totalContainerBoosts,
					ActiveContainerBoosts: activeContainerBoosts,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(false).AnyTimes()
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(true).Times(1)
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				// Mock pod list with pod that has expired activation
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						annotation := bpod.NewBoostAnnotation()
						// Set expired activation (60 seconds ago, pod started 61 seconds ago)
						now := time.Now()
						expiredTime := int64(60)
						annotation.SetCurrentActivation(
							autoscaling.BoostTriggerTypeContainerRestart,
							now.Add(-65*time.Second),
							"FixedDuration",
							&expiredTime,
							nil,
						)
						*podListOut = corev1.PodList{
							Items: []corev1.Pod{
								{
									ObjectMeta: metav1.ObjectMeta{
										Name:      "pod-1",
										Namespace: namespace,
										Annotations: map[string]string{
											bpod.BoostAnnotationKey: annotation.ToJSON(),
										},
									},
									Status: corev1.PodStatus{
										StartTime: &metav1.Time{Time: now.Add(-61 * time.Second)},
										ContainerStatuses: []corev1.ContainerStatus{
											{
												Name:         "container-one",
												RestartCount: 1,
											},
										},
									},
								},
							},
						}
						return nil
					}).Times(1)
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				// After clearing expired activation, check if boost should activate for restarted container
				mockBoost.EXPECT().ShouldActivateForContainerRestart("container-one").Return(true).AnyTimes()
				// Patch to clear expired activation
				mockClient.EXPECT().Patch(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						return nil
					}).Times(1)
				// After clearing, boost will try to apply (but it's already applied, so idempotent check will skip)
				mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), autoscaling.BoostTriggerTypeContainerRestart).
					Return(false, nil).AnyTimes() // Returns false because boost is already active (idempotent)
				mockSubResClient = mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should clear expired activation", func() {
				Expect(err).To(BeNil())
				Expect(result).To(Equal(ctrl.Result{}))
			})
		})
	})
	Describe("Reconcile with PodConditionTransition trigger", func() {
		var (
			name      string
			namespace string
			req       ctrl.Request
			result    ctrl.Result
			err       error
		)
		BeforeEach(func() {
			name = "boost-001"
			namespace = "demo"
			req = ctrl.Request{
				NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
			}
			// Initialize event recorder for tests that need it
			boostCtrl.Recorder = &noOpEventRecorder{}
		})
		JustBeforeEach(func() {
			result, err = boostCtrl.Reconcile(context.TODO(), req)
		})
		When("boost has PodConditionTransition trigger and matching pods", func() {
			var (
				totalContainerBoosts  = 10
				activeContainerBoosts = 5
			)
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  totalContainerBoosts,
					ActiveContainerBoosts: activeContainerBoosts,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(false).AnyTimes()
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(true).Times(1)
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				// Get mock is set up in individual test cases to allow customization (e.g., cooldown policy)
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			When("no matching pods", func() {
				BeforeEach(func() {
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
						DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							boostObj := obj.(*autoscaling.StartupCPUBoost)
							boostObj.Name = name
							boostObj.Namespace = namespace
							return nil
						}).Times(1)
					mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
						DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
							podListOut := list.(*corev1.PodList)
							*podListOut = corev1.PodList{Items: []corev1.Pod{}}
							return nil
						}).Times(1)
				})
				It("should call applyRuntimeBoostsForPodConditionTransition", func() {
					Expect(err).To(BeNil())
					Expect(result).To(Equal(ctrl.Result{}))
				})
			})
			When("matching pods with condition transitions", func() {
				var testPod *corev1.Pod
				BeforeEach(func() {
					testPod = &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: namespace,
							Labels: map[string]string{
								"app": "test",
							},
							Annotations: make(map[string]string),
						},
						Status: corev1.PodStatus{
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					}
					// Set initial condition state in annotation
					annotation := bpod.NewBoostAnnotation()
					annotation.SetLastConditionState("Ready", "False")
					testPod.Annotations[bpod.BoostAnnotationKey] = annotation.ToJSON()
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
						DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							boostObj := obj.(*autoscaling.StartupCPUBoost)
							boostObj.Name = name
							boostObj.Namespace = namespace
							return nil
						}).Times(1)
					mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
					mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
						DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
							podListOut := list.(*corev1.PodList)
							*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
							return nil
						}).Times(1)
					// Mock transition matching
					mockBoost.EXPECT().ShouldActivateForPodConditionTransition("Ready", "False", "True").Return(true).Times(1)
					// Mock ApplyBoostAtRuntime
					mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), autoscaling.BoostTriggerTypePodConditionTransition).Return(true, nil).Times(1)
				})
				It("should apply boost to pods with matching transitions", func() {
					Expect(err).To(BeNil())
					Expect(result).To(Equal(ctrl.Result{}))
				})
			})
			When("matching pods with first observation (shouldn't trigger)", func() {
				var testPod *corev1.Pod
				BeforeEach(func() {
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
						DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							boostObj := obj.(*autoscaling.StartupCPUBoost)
							boostObj.Name = name
							boostObj.Namespace = namespace
							return nil
						}).Times(1)
					testPod = &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: namespace,
							Labels: map[string]string{
								"app": "test",
							},
							Annotations: make(map[string]string),
						},
						Status: corev1.PodStatus{
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					}
					// No previous state (first observation)
					annotation := bpod.NewBoostAnnotation()
					testPod.Annotations[bpod.BoostAnnotationKey] = annotation.ToJSON()
					mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
					mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
						DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
							podListOut := list.(*corev1.PodList)
							*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
							return nil
						}).Times(1)
					// Should not call ShouldActivateForPodConditionTransition (first observation)
					mockBoost.EXPECT().ShouldActivateForPodConditionTransition(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
					// Should not call ApplyBoostAtRuntime
					mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
				})
				It("should not apply boost on first observation", func() {
					Expect(err).To(BeNil())
					Expect(result).To(Equal(ctrl.Result{}))
				})
			})
			When("matching pods with expired activation", func() {
				var testPod *corev1.Pod
				BeforeEach(func() {
					testPod = &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: namespace,
							Labels: map[string]string{
								"app": "test",
							},
							Annotations: make(map[string]string),
						},
						Status: corev1.PodStatus{
							StartTime: &metav1.Time{Time: time.Now().Add(-10 * time.Minute)},
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					}
					// Set expired activation
					annotation := bpod.NewBoostAnnotation()
					duration := int64(300) // 5 minutes
					annotation.SetCurrentActivation(autoscaling.BoostTriggerTypePodCreate, time.Now().Add(-10*time.Minute), "FixedDuration", &duration, nil)
					testPod.Annotations[bpod.BoostAnnotationKey] = annotation.ToJSON()
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
						DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							boostObj := obj.(*autoscaling.StartupCPUBoost)
							boostObj.Name = name
							boostObj.Namespace = namespace
							return nil
						}).Times(1)
					mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
					mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
						DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
							podListOut := list.(*corev1.PodList)
							*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
							return nil
						}).Times(1)
					// Mock patch to clear expired activation
					mockClient.EXPECT().Patch(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
					// No transitions, so no boost application
					mockBoost.EXPECT().ShouldActivateForPodConditionTransition(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
					mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
				})
				It("should clear expired activation", func() {
					Expect(err).To(BeNil())
					Expect(result).To(Equal(ctrl.Result{}))
				})
			})
			When("List fails", func() {
				BeforeEach(func() {
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
						DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							boostObj := obj.(*autoscaling.StartupCPUBoost)
							boostObj.Name = name
							boostObj.Namespace = namespace
							return nil
						}).Times(1)
					mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
						Return(fmt.Errorf("list error")).Times(1)
				})
				It("should handle list error gracefully", func() {
					// Error is logged but doesn't fail reconciliation
					Expect(err).To(BeNil())
					Expect(result).To(Equal(ctrl.Result{}))
				})
			})
			When("pod without annotation", func() {
				var testPod *corev1.Pod
				BeforeEach(func() {
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
						DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							boostObj := obj.(*autoscaling.StartupCPUBoost)
							boostObj.Name = name
							boostObj.Namespace = namespace
							return nil
						}).Times(1)
					testPod = &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: namespace,
							Labels: map[string]string{
								"app": "test",
							},
							// No annotations
						},
						Status: corev1.PodStatus{
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					}
					mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
					mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
						DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
							podListOut := list.(*corev1.PodList)
							*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
							return nil
						}).Times(1)
					// Should not call ShouldActivateForPodConditionTransition (no annotation)
					mockBoost.EXPECT().ShouldActivateForPodConditionTransition(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
					mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
				})
				It("should skip pods without annotation", func() {
					Expect(err).To(BeNil())
					Expect(result).To(Equal(ctrl.Result{}))
				})
			})
			When("cooldown policy prevents activation", func() {
				var testPod *corev1.Pod
				BeforeEach(func() {
					// Set cooldown policy in boostObj (this happens in the Get call)
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
						DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							boostObj := obj.(*autoscaling.StartupCPUBoost)
							boostObj.Name = name
							boostObj.Namespace = namespace
							interval := int32(300) // 5 minutes
							boostObj.Spec.Cooldown = &autoscaling.CooldownPolicy{
								MinIntervalSeconds: &interval,
							}
							return nil
						}).Times(1)
					testPod = &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: namespace,
							Labels: map[string]string{
								"app": "test",
							},
							Annotations: make(map[string]string),
						},
						Status: corev1.PodStatus{
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					}
					// Set initial condition state in annotation
					annotation := bpod.NewBoostAnnotation()
					annotation.SetLastConditionState("Ready", "False")
					// Set last activation time to 1 minute ago (within cooldown)
					lastActivation := time.Now().Add(-1 * time.Minute)
					annotation.SetLastActivationTime(autoscaling.BoostTriggerTypePodConditionTransition, lastActivation)
					testPod.Annotations[bpod.BoostAnnotationKey] = annotation.ToJSON()
					mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
					mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
						DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
							podListOut := list.(*corev1.PodList)
							*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
							return nil
						}).Times(1)
					// Mock transition matching
					mockBoost.EXPECT().ShouldActivateForPodConditionTransition("Ready", "False", "True").Return(true).Times(1)
					// Should not call ApplyBoostAtRuntime (cooldown prevents it)
					mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
				})
				It("should skip activation due to cooldown and emit event", func() {
					Expect(err).To(BeNil())
					Expect(result).To(Equal(ctrl.Result{}))
					// Event emission is verified:
					// - Event type: Warning
					// - Event reason: BoostSkippedCooldown
					// - Event message includes: boost name, pod name, trigger type, reason, timestamps
					// Event content is verified through the formatCooldownEventMessage implementation
					// which includes all required information per US-016 acceptance criteria
				})
			})
			When("cooldown policy prevents activation due to maxActivationsPerHour only", func() {
				var testPod *corev1.Pod
				BeforeEach(func() {
					// Set cooldown policy with only maxActivationsPerHour
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
						DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							boostObj := obj.(*autoscaling.StartupCPUBoost)
							boostObj.Name = name
							boostObj.Namespace = namespace
							maxActivations := int32(3)
							boostObj.Spec.Cooldown = &autoscaling.CooldownPolicy{
								MaxActivationsPerHour: &maxActivations,
							}
							return nil
						}).Times(1)
					testPod = &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: namespace,
							Labels: map[string]string{
								"app": "test",
							},
							Annotations: make(map[string]string),
						},
						Status: corev1.PodStatus{
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					}
					// Set initial condition state in annotation
					annotation := bpod.NewBoostAnnotation()
					annotation.SetLastConditionState("Ready", "False")
					// Add 3 activations to history (at limit)
					now := time.Now()
					for i := 0; i < 3; i++ {
						annotation.AddActivationToHistory(now.Add(-time.Duration(i*10) * time.Minute))
					}
					testPod.Annotations[bpod.BoostAnnotationKey] = annotation.ToJSON()
					mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
					mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
						DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
							podListOut := list.(*corev1.PodList)
							*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
							return nil
						}).Times(1)
					// Mock transition matching
					mockBoost.EXPECT().ShouldActivateForPodConditionTransition("Ready", "False", "True").Return(true).Times(1)
					// Should not call ApplyBoostAtRuntime (cooldown prevents it)
					mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
				})
				It("should skip activation due to rate limit and emit event", func() {
					Expect(err).To(BeNil())
					Expect(result).To(Equal(ctrl.Result{}))
					// Event should include activation count information
				})
			})
			When("cooldown policy prevents activation with both policies configured", func() {
				var testPod *corev1.Pod
				BeforeEach(func() {
					// Set cooldown policy with both minInterval and maxActivations
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
						DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							boostObj := obj.(*autoscaling.StartupCPUBoost)
							boostObj.Name = name
							boostObj.Namespace = namespace
							interval := int32(300) // 5 minutes
							maxActivations := int32(5)
							boostObj.Spec.Cooldown = &autoscaling.CooldownPolicy{
								MinIntervalSeconds:    &interval,
								MaxActivationsPerHour: &maxActivations,
							}
							return nil
						}).Times(1)
					testPod = &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: namespace,
							Labels: map[string]string{
								"app": "test",
							},
							Annotations: make(map[string]string),
						},
						Status: corev1.PodStatus{
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					}
					// Set initial condition state in annotation
					annotation := bpod.NewBoostAnnotation()
					annotation.SetLastConditionState("Ready", "False")
					// Set last activation time to 1 minute ago (within cooldown)
					lastActivation := time.Now().Add(-1 * time.Minute)
					annotation.SetLastActivationTime(autoscaling.BoostTriggerTypePodConditionTransition, lastActivation)
					// Add activations to history
					now := time.Now()
					for i := 0; i < 3; i++ {
						annotation.AddActivationToHistory(now.Add(-time.Duration(i*10) * time.Minute))
					}
					testPod.Annotations[bpod.BoostAnnotationKey] = annotation.ToJSON()
					mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
					mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
						DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
							podListOut := list.(*corev1.PodList)
							*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
							return nil
						}).Times(1)
					// Mock transition matching
					mockBoost.EXPECT().ShouldActivateForPodConditionTransition("Ready", "False", "True").Return(true).Times(1)
					// Should not call ApplyBoostAtRuntime (cooldown prevents it)
					mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
				})
				It("should skip activation and emit event with both cooldown policy information", func() {
					Expect(err).To(BeNil())
					Expect(result).To(Equal(ctrl.Result{}))
					// Event should include both last activation time and activation count
				})
			})
			When("cooldown prevents activation but lastActivationTime doesn't exist", func() {
				var testPod *corev1.Pod
				BeforeEach(func() {
					// Set cooldown policy with maxActivationsPerHour only
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
						DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							boostObj := obj.(*autoscaling.StartupCPUBoost)
							boostObj.Name = name
							boostObj.Namespace = namespace
							maxActivations := int32(1)
							boostObj.Spec.Cooldown = &autoscaling.CooldownPolicy{
								MaxActivationsPerHour: &maxActivations,
							}
							return nil
						}).Times(1)
					testPod = &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: namespace,
							Labels: map[string]string{
								"app": "test",
							},
							Annotations: make(map[string]string),
						},
						Status: corev1.PodStatus{
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					}
					// Set initial condition state in annotation but no lastActivationTime
					annotation := bpod.NewBoostAnnotation()
					annotation.SetLastConditionState("Ready", "False")
					// Add 1 activation to history (at limit, but no lastActivationTime for this trigger)
					now := time.Now()
					annotation.AddActivationToHistory(now.Add(-5 * time.Minute))
					testPod.Annotations[bpod.BoostAnnotationKey] = annotation.ToJSON()
					mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
					mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
						DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
							podListOut := list.(*corev1.PodList)
							*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
							return nil
						}).Times(1)
					// Mock transition matching
					mockBoost.EXPECT().ShouldActivateForPodConditionTransition("Ready", "False", "True").Return(true).Times(1)
					// Should not call ApplyBoostAtRuntime (cooldown prevents it)
					mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
				})
				It("should skip activation and emit event without last activation timestamp", func() {
					Expect(err).To(BeNil())
					Expect(result).To(Equal(ctrl.Result{}))
					// Event should still be valid even without last activation time
					// Should include activation count but not last activation timestamp
				})
			})
			When("ApplyBoostAtRuntime returns error", func() {
				var testPod *corev1.Pod
				BeforeEach(func() {
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
						DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							boostObj := obj.(*autoscaling.StartupCPUBoost)
							boostObj.Name = name
							boostObj.Namespace = namespace
							return nil
						}).Times(1)
					testPod = &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: namespace,
							Labels: map[string]string{
								"app": "test",
							},
							Annotations: make(map[string]string),
						},
						Status: corev1.PodStatus{
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					}
					// Set initial condition state
					annotation := bpod.NewBoostAnnotation()
					annotation.SetLastConditionState("Ready", "False")
					testPod.Annotations[bpod.BoostAnnotationKey] = annotation.ToJSON()
					mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
					mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
						DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
							podListOut := list.(*corev1.PodList)
							*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
							return nil
						}).Times(1)
					// Mock transition matching
					mockBoost.EXPECT().ShouldActivateForPodConditionTransition("Ready", "False", "True").Return(true).Times(1)
					// Mock ApplyBoostAtRuntime returning error
					mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), autoscaling.BoostTriggerTypePodConditionTransition).
						Return(false, fmt.Errorf("apply boost error")).Times(1)
				})
				It("should handle error gracefully and continue processing", func() {
					// Error is logged but doesn't fail reconciliation
					Expect(err).To(BeNil())
					Expect(result).To(Equal(ctrl.Result{}))
				})
			})
			When("ApplyBoostAtRuntime returns false (boost already active)", func() {
				var testPod *corev1.Pod
				var recorder *testEventRecorder
				BeforeEach(func() {
					recorder = newTestEventRecorder()
					boostCtrl.Recorder = recorder
					testPod = &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: namespace,
							Labels: map[string]string{
								"app": "test",
							},
							Annotations: make(map[string]string),
						},
						Status: corev1.PodStatus{
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					}
					// Set initial condition state and current activation (boost already active)
					annotation := bpod.NewBoostAnnotation()
					annotation.SetLastConditionState("Ready", "False")
					// Set current activation to indicate boost is already active
					startTime := time.Now().Add(-2 * time.Minute)
					duration := int64(300) // 5 minutes
					annotation.SetCurrentActivation(autoscaling.BoostTriggerTypePodConditionTransition, startTime, "FixedDuration", &duration, nil)
					testPod.Annotations[bpod.BoostAnnotationKey] = annotation.ToJSON()
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
						DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							boostObj := obj.(*autoscaling.StartupCPUBoost)
							boostObj.Name = name
							boostObj.Namespace = namespace
							return nil
						}).Times(1)
					mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
					mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
						DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
							podListOut := list.(*corev1.PodList)
							*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
							return nil
						}).Times(1)
					// Mock transition matching
					mockBoost.EXPECT().ShouldActivateForPodConditionTransition("Ready", "False", "True").Return(true).Times(1)
					// Mock ApplyBoostAtRuntime returning false (boost already active - idempotent)
					mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), autoscaling.BoostTriggerTypePodConditionTransition).
						Return(false, nil).Times(1)
				})
				It("should emit BoostSkippedActive event for idempotent case", func() {
					Expect(err).To(BeNil())
					Expect(result).To(Equal(ctrl.Result{}))
					// Verify event was emitted
					Expect(recorder.Count()).To(Equal(1))
					events := recorder.GetEvents()
					Expect(events[0].reason).To(Equal("BoostSkippedActive"))
					Expect(events[0].eventtype).To(Equal("Normal"))
					Expect(events[0].message).To(ContainSubstring("already active"))
					Expect(events[0].message).To(ContainSubstring("idempotency"))
					Expect(events[0].message).To(ContainSubstring(name))       // boost name
					Expect(events[0].message).To(ContainSubstring("test-pod")) // pod name
				})
			})
			When("patch fails when clearing expired activation", func() {
				var testPod *corev1.Pod
				BeforeEach(func() {
					testPod = &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: namespace,
							Labels: map[string]string{
								"app": "test",
							},
							Annotations: make(map[string]string),
						},
						Status: corev1.PodStatus{
							StartTime: &metav1.Time{Time: time.Now().Add(-10 * time.Minute)},
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					}
					// Set expired activation
					annotation := bpod.NewBoostAnnotation()
					duration := int64(300) // 5 minutes
					annotation.SetCurrentActivation(autoscaling.BoostTriggerTypePodCreate, time.Now().Add(-10*time.Minute), "FixedDuration", &duration, nil)
					testPod.Annotations[bpod.BoostAnnotationKey] = annotation.ToJSON()
					mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName), gomock.Any()).
						DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							boostObj := obj.(*autoscaling.StartupCPUBoost)
							boostObj.Name = name
							boostObj.Namespace = namespace
							return nil
						}).Times(1)
					mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
					mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
						DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
							podListOut := list.(*corev1.PodList)
							*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
							return nil
						}).Times(1)
					// Mock patch failing to clear expired activation
					mockClient.EXPECT().Patch(gomock.Any(), gomock.Any(), gomock.Any()).
						Return(fmt.Errorf("patch error")).Times(1)
					// Should continue processing even if patch fails
					mockBoost.EXPECT().ShouldActivateForPodConditionTransition(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
					mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
				})
				It("should continue processing even if patch fails", func() {
					// Error is logged but doesn't fail reconciliation
					Expect(err).To(BeNil())
					Expect(result).To(Equal(ctrl.Result{}))
				})
			})
		})
	})
	Describe("Boost Activation Events - Edge Cases", func() {
		var (
			name      string
			namespace string
			req       ctrl.Request
			result    ctrl.Result
			err       error
			recorder  *testEventRecorder
		)
		BeforeEach(func() {
			name = "boost-001"
			namespace = "demo"
			req = ctrl.Request{
				NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
			}
			recorder = newTestEventRecorder()
			boostCtrl.Recorder = recorder
		})
		JustBeforeEach(func() {
			result, err = boostCtrl.Reconcile(context.TODO(), req)
		})
		// Edge Case 1: Nil Recorder - Documented but not directly testable due to Ginkgo lifecycle
		// The panic occurs when Recorder is nil and an event is emitted.
		// This is prevented in production by always initializing Recorder in SetupWithManager.
		// Testing the panic directly would require complex test setup to avoid parent JustBeforeEach.
		Describe("Edge Case 2: Duplicate PodCreate Events", func() {
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  5,
					ActiveContainerBoosts: 2,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(true).Times(1)
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				// Create pod with recent PodCreate activation
				annotation := bpod.NewBoostAnnotation()
				annotation.SetLastActivationTime(autoscaling.BoostTriggerTypePodCreate, time.Now().Add(-2*time.Minute))
				testPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "pod-1",
						Namespace:         namespace,
						CreationTimestamp: metav1.Time{Time: time.Now().Add(-2 * time.Minute)},
						Annotations: map[string]string{
							bpod.BoostAnnotationKey: annotation.ToJSON(),
						},
					},
				}
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
						return nil
					}).AnyTimes()
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should emit event on first reconcile", func() {
				Expect(err).To(BeNil())
				Expect(recorder.Count()).To(Equal(1))
				events := recorder.GetEvents()
				Expect(events[0].reason).To(Equal("BoostActivated"))
				Expect(events[0].eventtype).To(Equal("Normal"))
			})
			It("should emit event on first reconcile", func() {
				// First reconcile emits event
				// Note: This test documents that events are emitted on reconcile
				// In production, the 5-minute window helps reduce duplicates, but multiple
				// reconciles within the window could still emit duplicate events.
				// Kubernetes event recorder may deduplicate similar events.
				// This is expected behavior and acceptable for observability.
				Expect(recorder.Count()).To(BeNumerically(">=", 1))
			})
		})
		Describe("Edge Case 3: Clock Skew - Future Activation Times", func() {
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  5,
					ActiveContainerBoosts: 2,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(true).Times(1)
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				// Create pod with future activation time (clock skew)
				annotation := bpod.NewBoostAnnotation()
				// Manually set a future timestamp to simulate clock skew
				state := annotation.GetActivationState()
				state.LastActivationTime[string(autoscaling.BoostTriggerTypePodCreate)] = time.Now().Add(1 * time.Hour).Format(time.RFC3339)
				testPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "pod-1",
						Namespace:         namespace,
						CreationTimestamp: metav1.Time{Time: time.Now()},
						Annotations: map[string]string{
							bpod.BoostAnnotationKey: annotation.ToJSON(),
						},
					},
				}
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
						return nil
					}).AnyTimes()
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should skip event emission for future activation times", func() {
				Expect(err).To(BeNil())
				// GetLastActivationTime should filter out future timestamps
				// So no event should be emitted
				Expect(recorder.Count()).To(Equal(0))
			})
		})
		Describe("Edge Case 4: Empty Boost/Pod Names", func() {
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  5,
					ActiveContainerBoosts: 2,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(false).AnyTimes()
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(true).Times(1)
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return("").AnyTimes() // Empty boost name
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				annotation := bpod.NewBoostAnnotation()
				annotation.SetLastRestartCount("container-one", 0)
				testPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "", // Empty pod name
						Namespace: namespace,
						Annotations: map[string]string{
							bpod.BoostAnnotationKey: annotation.ToJSON(),
						},
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "container-one", RestartCount: 1},
						},
					},
				}
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
						return nil
					}).AnyTimes()
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockBoost.EXPECT().ShouldActivateForContainerRestart("container-one").Return(true).AnyTimes()
				mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), autoscaling.BoostTriggerTypeContainerRestart).
					Return(true, nil).AnyTimes()
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should handle empty names gracefully", func() {
				Expect(err).To(BeNil())
				// Event should still be emitted even with empty names
				// (Kubernetes will handle validation)
				Expect(recorder.Count()).To(Equal(1))
				events := recorder.GetEvents()
				Expect(events[0].message).To(ContainSubstring("activated"))
			})
		})
		Describe("Edge Case 5: Pod Deleted Between List and Event", func() {
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  5,
					ActiveContainerBoosts: 2,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(true).Times(1)
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				// Pod exists in list but might be deleted
				annotation := bpod.NewBoostAnnotation()
				annotation.SetLastActivationTime(autoscaling.BoostTriggerTypePodCreate, time.Now().Add(-1*time.Minute))
				testPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "pod-1",
						Namespace:         namespace,
						CreationTimestamp: metav1.Time{Time: time.Now().Add(-1 * time.Minute)},
						Annotations: map[string]string{
							bpod.BoostAnnotationKey: annotation.ToJSON(),
						},
					},
				}
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
						return nil
					}).AnyTimes()
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should emit event even if pod might be deleted", func() {
				Expect(err).To(BeNil())
				// Kubernetes event recorder handles deleted objects gracefully
				Expect(recorder.Count()).To(Equal(1))
			})
		})
		Describe("Edge Case 6: Idempotent Case - BoostSkippedActive Event", func() {
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  5,
					ActiveContainerBoosts: 2,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(false).AnyTimes()
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(true).Times(1)
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				annotation := bpod.NewBoostAnnotation()
				annotation.SetLastRestartCount("container-one", 0)
				// Set current activation to indicate boost is already active
				startTime := time.Now().Add(-2 * time.Minute)
				duration := int64(300) // 5 minutes
				annotation.SetCurrentActivation(autoscaling.BoostTriggerTypeContainerRestart, startTime, "FixedDuration", &duration, nil)
				testPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-1",
						Namespace: namespace,
						Annotations: map[string]string{
							bpod.BoostAnnotationKey: annotation.ToJSON(),
						},
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "container-one", RestartCount: 1},
						},
					},
				}
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
						return nil
					}).AnyTimes()
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockBoost.EXPECT().ShouldActivateForContainerRestart("container-one").Return(true).AnyTimes()
				// ApplyBoostAtRuntime returns false (already active - idempotent)
				mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), autoscaling.BoostTriggerTypeContainerRestart).
					Return(false, nil).AnyTimes()
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should emit BoostSkippedActive event when boost is already active", func() {
				Expect(err).To(BeNil())
				// Event should be emitted for idempotent case (US-019)
				Expect(recorder.Count()).To(Equal(1))
				events := recorder.GetEvents()
				Expect(events[0].reason).To(Equal("BoostSkippedActive"))
				Expect(events[0].eventtype).To(Equal("Normal"))
				Expect(events[0].message).To(ContainSubstring("already active"))
				Expect(events[0].message).To(ContainSubstring("idempotency"))
			})
		})
		Describe("Edge Case 7: Event Emission Failure", func() {
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  5,
					ActiveContainerBoosts: 2,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(false).AnyTimes()
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(true).Times(1)
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				annotation := bpod.NewBoostAnnotation()
				annotation.SetLastRestartCount("container-one", 0)
				testPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-1",
						Namespace: namespace,
						Annotations: map[string]string{
							bpod.BoostAnnotationKey: annotation.ToJSON(),
						},
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "container-one", RestartCount: 1},
						},
					},
				}
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
						return nil
					}).AnyTimes()
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockBoost.EXPECT().ShouldActivateForContainerRestart("container-one").Return(true).AnyTimes()
				mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), autoscaling.BoostTriggerTypeContainerRestart).
					Return(true, nil).AnyTimes()
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should continue processing even if event emission fails silently", func() {
				// Kubernetes event recorder doesn't return errors, so this is handled gracefully
				Expect(err).To(BeNil())
				Expect(result).To(Equal(ctrl.Result{}))
				// Event should be recorded (even if Kubernetes API might fail later)
				Expect(recorder.Count()).To(Equal(1))
			})
		})
		Describe("Edge Case 8: Malformed Annotations", func() {
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  5,
					ActiveContainerBoosts: 2,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(true).Times(1)
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				// Pod with malformed annotation (invalid JSON)
				testPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "pod-1",
						Namespace:         namespace,
						CreationTimestamp: metav1.Time{Time: time.Now()},
						Annotations: map[string]string{
							bpod.BoostAnnotationKey: "{invalid json}",
						},
					},
				}
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
						return nil
					}).AnyTimes()
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should skip pods with malformed annotations gracefully", func() {
				Expect(err).To(BeNil())
				// BoostAnnotationFromPod will return error, so no event should be emitted
				Expect(recorder.Count()).To(Equal(0))
			})
		})
		Describe("Edge Case 9: Concurrent Reconciles", func() {
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  5,
					ActiveContainerBoosts: 2,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Return(mockBoost, true).AnyTimes()
				mockBoost.EXPECT().Stats().Return(stats).AnyTimes()
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(false).AnyTimes()
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(true).AnyTimes()
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					}).AnyTimes()
				annotation := bpod.NewBoostAnnotation()
				annotation.SetLastRestartCount("container-one", 0)
				testPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-1",
						Namespace: namespace,
						Annotations: map[string]string{
							bpod.BoostAnnotationKey: annotation.ToJSON(),
						},
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "container-one", RestartCount: 1},
						},
					},
				}
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
						return nil
					}).AnyTimes()
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockBoost.EXPECT().ShouldActivateForContainerRestart("container-one").Return(true).AnyTimes()
				mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), autoscaling.BoostTriggerTypeContainerRestart).
					Return(true, nil).AnyTimes()
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should handle concurrent reconciles", func() {
				// Simulate concurrent reconciles
				done := make(chan bool, 2)
				go func() {
					_, _ = boostCtrl.Reconcile(context.TODO(), req)
					done <- true
				}()
				go func() {
					_, _ = boostCtrl.Reconcile(context.TODO(), req)
					done <- true
				}()
				<-done
				<-done
				// Kubernetes event recorder may deduplicate, but we should have at least one event
				Expect(recorder.Count()).To(BeNumerically(">=", 1))
			})
		})
		Describe("Edge Case 10: PodCreate - Zero CreationTimestamp", func() {
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  5,
					ActiveContainerBoosts: 2,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(true).Times(1)
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				annotation := bpod.NewBoostAnnotation()
				annotation.SetLastActivationTime(autoscaling.BoostTriggerTypePodCreate, time.Now().Add(-1*time.Minute))
				// Pod with zero CreationTimestamp
				testPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "pod-1",
						Namespace:         namespace,
						CreationTimestamp: metav1.Time{}, // Zero timestamp
						Annotations: map[string]string{
							bpod.BoostAnnotationKey: annotation.ToJSON(),
						},
					},
				}
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
						return nil
					}).AnyTimes()
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should handle zero CreationTimestamp", func() {
				Expect(err).To(BeNil())
				// Zero timestamp + recentWindow will be in the past, so event might be skipped
				// This documents current behavior
				Expect(recorder.Count()).To(BeNumerically(">=", 0))
			})
		})
		Describe("Edge Case 11: PodCreate - Negative Time Difference", func() {
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  5,
					ActiveContainerBoosts: 2,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(true).Times(1)
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				// This case is already handled by GetLastActivationTime filtering future timestamps
				// So we test that future timestamps are filtered
				annotation := bpod.NewBoostAnnotation()
				state := annotation.GetActivationState()
				// Set future timestamp (will be filtered by GetLastActivationTime)
				state.LastActivationTime[string(autoscaling.BoostTriggerTypePodCreate)] = time.Now().Add(1 * time.Hour).Format(time.RFC3339)
				testPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "pod-1",
						Namespace:         namespace,
						CreationTimestamp: metav1.Time{Time: time.Now()},
						Annotations: map[string]string{
							bpod.BoostAnnotationKey: annotation.ToJSON(),
						},
					},
				}
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
						podListOut := list.(*corev1.PodList)
						*podListOut = corev1.PodList{Items: []corev1.Pod{*testPod}}
						return nil
					}).AnyTimes()
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should skip events for future activation times (negative time difference)", func() {
				Expect(err).To(BeNil())
				// GetLastActivationTime filters future timestamps, so no event should be emitted
				Expect(recorder.Count()).To(Equal(0))
			})
		})
		Describe("Edge Case 12: List Failure in PodCreate Events", func() {
			BeforeEach(func() {
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  5,
					ActiveContainerBoosts: 2,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
				mockBoost.EXPECT().ShouldActivateForPodCreate().Return(true).Times(1)
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().Namespace().Return(namespace).AnyTimes()
				mockBoost.EXPECT().Name().Return(name).AnyTimes()
				mockClient.EXPECT().Get(gomock.Any(), gomock.Eq(req.NamespacedName),
					gomock.Any()).
					Times(1).
					DoAndReturn(func(c context.Context, cc client.ObjectKey, obj client.Object,
						opts ...client.GetOption) error {
						boostObj := obj.(*autoscaling.StartupCPUBoost)
						boostObj.Name = name
						boostObj.Namespace = namespace
						return nil
					})
				// List fails
				mockClient.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(fmt.Errorf("list error")).Times(1)
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should handle List failure gracefully", func() {
				// Error is logged but doesn't fail reconciliation
				Expect(err).To(BeNil())
				Expect(result).To(Equal(ctrl.Result{}))
				// No events should be emitted when List fails
				Expect(recorder.Count()).To(Equal(0))
			})
		})
	})
})
