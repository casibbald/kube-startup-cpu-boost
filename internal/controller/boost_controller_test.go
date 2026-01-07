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

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

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
				// Mock pod list with pods that have restarts
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
					}).Times(1)
				mockBoost.EXPECT().Matches(gomock.Any()).Return(true).AnyTimes()
				mockBoost.EXPECT().ShouldActivateForContainerRestart("container-one").Return(true).AnyTimes()
				mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), autoscaling.BoostTriggerTypeContainerRestart).
					Return(true, nil).AnyTimes()
				mockSubResClient = mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			It("should apply boost to pods with restarts", func() {
				Expect(err).To(BeNil())
				Expect(result).To(Equal(ctrl.Result{}))
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
				stats := boost.StartupCPUBoostStats{
					TotalContainerBoosts:  totalContainerBoosts,
					ActiveContainerBoosts: activeContainerBoosts,
				}
				mockManager.EXPECT().GetRegularCPUBoost(gomock.Any(), gomock.Eq(name),
					gomock.Eq(namespace)).Times(1).Return(mockBoost, true)
				mockBoost.EXPECT().Stats().Times(1).Return(stats)
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
				mockBoost.EXPECT().HasContainerRestartTrigger().Return(false).AnyTimes()
				mockBoost.EXPECT().HasPodConditionTransitionTrigger().Return(true).Times(1)
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
				mockSubResClient := mock.NewMockSubResourceClient(mockCtrl)
				mockSubResClient.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockClient.EXPECT().Status().Return(mockSubResClient).AnyTimes()
			})
			When("no matching pods", func() {
				BeforeEach(func() {
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
			When("ApplyBoostAtRuntime returns error", func() {
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
					// Mock ApplyBoostAtRuntime returning false (boost already active - idempotent)
					mockBoost.EXPECT().ApplyBoostAtRuntime(gomock.Any(), gomock.Any(), autoscaling.BoostTriggerTypePodConditionTransition).
						Return(false, nil).Times(1)
				})
				It("should handle idempotent case gracefully", func() {
					// Boost already active, so applied=false is expected
					Expect(err).To(BeNil())
					Expect(result).To(Equal(ctrl.Result{}))
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
})
