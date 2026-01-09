// Copyright 2025 Google LLC
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

package v1alpha1

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestV1alpha1(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "v1alpha1 API Types Suite")
}

var _ = Describe("FixedDurationPolicyUnit constants", func() {
	It("should have correct values", func() {
		Expect(FixedDurationPolicyUnitSec).To(Equal(FixedDurationPolicyUnit("Seconds")))
		Expect(FixedDurationPolicyUnitMin).To(Equal(FixedDurationPolicyUnit("Minutes")))
	})
})

var _ = Describe("BoostTriggerType constants", func() {
	It("should have correct values", func() {
		Expect(BoostTriggerTypePodCreate).To(Equal(BoostTriggerType("PodCreate")))
		Expect(BoostTriggerTypeContainerRestart).To(Equal(BoostTriggerType("ContainerRestart")))
		Expect(BoostTriggerTypePodConditionTransition).To(Equal(BoostTriggerType("PodConditionTransition")))
	})
})

var _ = Describe("FixedDurationPolicy", func() {
	It("should create a valid policy", func() {
		policy := FixedDurationPolicy{
			Unit:  FixedDurationPolicyUnitSec,
			Value: 300,
		}
		Expect(policy.Unit).To(Equal(FixedDurationPolicyUnitSec))
		Expect(policy.Value).To(Equal(int64(300)))
	})
})

var _ = Describe("PodConditionDurationPolicy", func() {
	It("should create a valid policy", func() {
		policy := PodConditionDurationPolicy{
			Type:   corev1.PodReady,
			Status: corev1.ConditionTrue,
		}
		Expect(policy.Type).To(Equal(corev1.PodReady))
		Expect(policy.Status).To(Equal(corev1.ConditionTrue))
	})
})

var _ = Describe("DurationPolicy", func() {
	It("should create a policy with Fixed duration", func() {
		policy := DurationPolicy{
			Fixed: &FixedDurationPolicy{
				Unit:  FixedDurationPolicyUnitSec,
				Value: 300,
			},
		}
		Expect(policy.Fixed).NotTo(BeNil())
		Expect(policy.PodCondition).To(BeNil())
	})

	It("should create a policy with PodCondition duration", func() {
		policy := DurationPolicy{
			PodCondition: &PodConditionDurationPolicy{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			},
		}
		Expect(policy.Fixed).To(BeNil())
		Expect(policy.PodCondition).NotTo(BeNil())
	})
})

var _ = Describe("FixedResources", func() {
	It("should create a valid resource policy", func() {
		requests := resource.MustParse("100m")
		limits := resource.MustParse("200m")
		policy := FixedResources{
			Requests: requests,
			Limits:   limits,
		}
		Expect(policy.Requests.Equal(requests)).To(BeTrue())
		Expect(policy.Limits.Equal(limits)).To(BeTrue())
	})
})

var _ = Describe("PercentageIncrease", func() {
	It("should create a valid percentage policy", func() {
		policy := PercentageIncrease{
			Value: 300,
		}
		Expect(policy.Value).To(Equal(int64(300)))
	})
})

var _ = Describe("ContainerPolicy", func() {
	It("should create a policy with PercentageIncrease", func() {
		policy := ContainerPolicy{
			ContainerName: "test-container",
			PercentageIncrease: &PercentageIncrease{
				Value: 200,
			},
		}
		Expect(policy.ContainerName).To(Equal("test-container"))
		Expect(policy.PercentageIncrease).NotTo(BeNil())
		Expect(policy.FixedResources).To(BeNil())
	})

	It("should create a policy with FixedResources", func() {
		requests := resource.MustParse("100m")
		policy := ContainerPolicy{
			ContainerName: "test-container",
			FixedResources: &FixedResources{
				Requests: requests,
			},
		}
		Expect(policy.ContainerName).To(Equal("test-container"))
		Expect(policy.PercentageIncrease).To(BeNil())
		Expect(policy.FixedResources).NotTo(BeNil())
	})
})

var _ = Describe("ResourcePolicy", func() {
	It("should create a valid resource policy", func() {
		policy := ResourcePolicy{
			ContainerPolicies: []ContainerPolicy{
				{
					ContainerName: "container-1",
					PercentageIncrease: &PercentageIncrease{
						Value: 200,
					},
				},
			},
		}
		Expect(len(policy.ContainerPolicies)).To(Equal(1))
		Expect(policy.ContainerPolicies[0].ContainerName).To(Equal("container-1"))
	})
})

var _ = Describe("BoostTrigger", func() {
	It("should create a PodCreate trigger", func() {
		trigger := BoostTrigger{
			Type: BoostTriggerTypePodCreate,
		}
		Expect(trigger.Type).To(Equal(BoostTriggerTypePodCreate))
		Expect(trigger.ContainerName).To(BeNil())
		Expect(trigger.ConditionType).To(BeNil())
	})

	It("should create a ContainerRestart trigger with container name", func() {
		containerName := "my-container"
		trigger := BoostTrigger{
			Type:          BoostTriggerTypeContainerRestart,
			ContainerName: &containerName,
		}
		Expect(trigger.Type).To(Equal(BoostTriggerTypeContainerRestart))
		Expect(trigger.ContainerName).NotTo(BeNil())
		Expect(*trigger.ContainerName).To(Equal("my-container"))
	})

	It("should create a ContainerRestart trigger with wildcard", func() {
		wildcard := "*"
		trigger := BoostTrigger{
			Type:          BoostTriggerTypeContainerRestart,
			ContainerName: &wildcard,
		}
		Expect(trigger.Type).To(Equal(BoostTriggerTypeContainerRestart))
		Expect(trigger.ContainerName).NotTo(BeNil())
		Expect(*trigger.ContainerName).To(Equal("*"))
	})

	It("should create a PodConditionTransition trigger", func() {
		conditionType := "Ready"
		fromStatus := "False"
		toStatus := "True"
		trigger := BoostTrigger{
			Type:          BoostTriggerTypePodConditionTransition,
			ConditionType: &conditionType,
			FromStatus:    &fromStatus,
			ToStatus:      &toStatus,
		}
		Expect(trigger.Type).To(Equal(BoostTriggerTypePodConditionTransition))
		Expect(trigger.ConditionType).NotTo(BeNil())
		Expect(*trigger.ConditionType).To(Equal("Ready"))
		Expect(*trigger.FromStatus).To(Equal("False"))
		Expect(*trigger.ToStatus).To(Equal("True"))
	})
})

var _ = Describe("CooldownPolicy", func() {
	It("should create a policy with MinIntervalSeconds", func() {
		interval := int32(300)
		policy := CooldownPolicy{
			MinIntervalSeconds: &interval,
		}
		Expect(policy.MinIntervalSeconds).NotTo(BeNil())
		Expect(*policy.MinIntervalSeconds).To(Equal(int32(300)))
		Expect(policy.MaxActivationsPerHour).To(BeNil())
	})

	It("should create a policy with MaxActivationsPerHour", func() {
		maxActivations := int32(5)
		policy := CooldownPolicy{
			MaxActivationsPerHour: &maxActivations,
		}
		Expect(policy.MinIntervalSeconds).To(BeNil())
		Expect(policy.MaxActivationsPerHour).NotTo(BeNil())
		Expect(*policy.MaxActivationsPerHour).To(Equal(int32(5)))
	})

	It("should create a policy with both fields", func() {
		interval := int32(300)
		maxActivations := int32(3)
		policy := CooldownPolicy{
			MinIntervalSeconds:    &interval,
			MaxActivationsPerHour: &maxActivations,
		}
		Expect(policy.MinIntervalSeconds).NotTo(BeNil())
		Expect(policy.MaxActivationsPerHour).NotTo(BeNil())
		Expect(*policy.MinIntervalSeconds).To(Equal(int32(300)))
		Expect(*policy.MaxActivationsPerHour).To(Equal(int32(3)))
	})
})

var _ = Describe("StartupCPUBoostSpec", func() {
	It("should create a valid spec", func() {
		spec := StartupCPUBoostSpec{
			ResourcePolicy: ResourcePolicy{
				ContainerPolicies: []ContainerPolicy{
					{
						ContainerName: "test-container",
						PercentageIncrease: &PercentageIncrease{
							Value: 200,
						},
					},
				},
			},
			DurationPolicy: DurationPolicy{
				Fixed: &FixedDurationPolicy{
					Unit:  FixedDurationPolicyUnitSec,
					Value: 300,
				},
			},
			Triggers: []BoostTrigger{
				{Type: BoostTriggerTypePodCreate},
			},
		}
		Expect(len(spec.ResourcePolicy.ContainerPolicies)).To(Equal(1))
		Expect(spec.DurationPolicy.Fixed).NotTo(BeNil())
		Expect(len(spec.Triggers)).To(Equal(1))
		Expect(spec.Triggers[0].Type).To(Equal(BoostTriggerTypePodCreate))
	})
})

var _ = Describe("StartupCPUBoostStatus", func() {
	It("should create a valid status", func() {
		status := StartupCPUBoostStatus{
			ActiveContainerBoosts: 5,
			TotalContainerBoosts:  10,
			Conditions: []metav1.Condition{
				{
					Type:   "Active",
					Status: metav1.ConditionTrue,
					Reason: "Ready",
				},
			},
		}
		Expect(status.ActiveContainerBoosts).To(Equal(int32(5)))
		Expect(status.TotalContainerBoosts).To(Equal(int32(10)))
		Expect(len(status.Conditions)).To(Equal(1))
	})
})

var _ = Describe("StartupCPUBoost", func() {
	It("should create a valid StartupCPUBoost", func() {
		boost := StartupCPUBoost{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-boost",
				Namespace: "default",
			},
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "test",
				},
			},
			Spec: StartupCPUBoostSpec{
				DurationPolicy: DurationPolicy{
					Fixed: &FixedDurationPolicy{
						Unit:  FixedDurationPolicyUnitSec,
						Value: 300,
					},
				},
			},
		}
		Expect(boost.Name).To(Equal("test-boost"))
		Expect(boost.Namespace).To(Equal("default"))
		Expect(boost.Spec.DurationPolicy.Fixed).NotTo(BeNil())
	})
})

var _ = Describe("StartupCPUBoostList", func() {
	It("should create a valid list", func() {
		list := StartupCPUBoostList{
			Items: []StartupCPUBoost{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "boost-1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "boost-2",
					},
				},
			},
		}
		Expect(len(list.Items)).To(Equal(2))
		Expect(list.Items[0].Name).To(Equal("boost-1"))
		Expect(list.Items[1].Name).To(Equal("boost-2"))
	})
})

var _ = Describe("DeepCopy methods", func() {
	It("should deep copy BoostTrigger", func() {
		containerName := "test-container"
		original := &BoostTrigger{
			Type:          BoostTriggerTypeContainerRestart,
			ContainerName: &containerName,
		}
		copy := original.DeepCopy()
		Expect(copy).NotTo(BeIdenticalTo(original))
		Expect(copy.Type).To(Equal(original.Type))
		Expect(copy.ContainerName).NotTo(BeNil())
		Expect(*copy.ContainerName).To(Equal(*original.ContainerName))
		// Modify copy and verify original is unchanged
		*copy.ContainerName = "modified"
		Expect(*original.ContainerName).To(Equal("test-container"))
	})

	It("should deep copy ContainerPolicy", func() {
		original := &ContainerPolicy{
			ContainerName: "test-container",
			PercentageIncrease: &PercentageIncrease{
				Value: 200,
			},
		}
		copy := original.DeepCopy()
		Expect(copy).NotTo(BeIdenticalTo(original))
		Expect(copy.ContainerName).To(Equal(original.ContainerName))
		Expect(copy.PercentageIncrease).NotTo(BeNil())
		Expect(copy.PercentageIncrease.Value).To(Equal(original.PercentageIncrease.Value))
		// Modify copy and verify original is unchanged
		copy.PercentageIncrease.Value = 300
		Expect(original.PercentageIncrease.Value).To(Equal(int64(200)))
	})

	It("should deep copy CooldownPolicy", func() {
		interval := int32(300)
		original := &CooldownPolicy{
			MinIntervalSeconds: &interval,
		}
		copy := original.DeepCopy()
		Expect(copy).NotTo(BeIdenticalTo(original))
		Expect(copy.MinIntervalSeconds).NotTo(BeNil())
		Expect(*copy.MinIntervalSeconds).To(Equal(*original.MinIntervalSeconds))
		// Modify copy and verify original is unchanged
		*copy.MinIntervalSeconds = 600
		Expect(*original.MinIntervalSeconds).To(Equal(int32(300)))
	})

	It("should deep copy DurationPolicy", func() {
		original := &DurationPolicy{
			Fixed: &FixedDurationPolicy{
				Unit:  FixedDurationPolicyUnitSec,
				Value: 300,
			},
		}
		copy := original.DeepCopy()
		Expect(copy).NotTo(BeIdenticalTo(original))
		Expect(copy.Fixed).NotTo(BeNil())
		Expect(copy.Fixed.Value).To(Equal(original.Fixed.Value))
		// Modify copy and verify original is unchanged
		copy.Fixed.Value = 600
		Expect(original.Fixed.Value).To(Equal(int64(300)))
	})

	It("should deep copy FixedDurationPolicy", func() {
		original := &FixedDurationPolicy{
			Unit:  FixedDurationPolicyUnitSec,
			Value: 300,
		}
		copy := original.DeepCopy()
		Expect(copy).NotTo(BeIdenticalTo(original))
		Expect(copy.Unit).To(Equal(original.Unit))
		Expect(copy.Value).To(Equal(original.Value))
	})

	It("should deep copy FixedResources", func() {
		requests := resource.MustParse("100m")
		limits := resource.MustParse("200m")
		original := &FixedResources{
			Requests: requests,
			Limits:   limits,
		}
		copy := original.DeepCopy()
		Expect(copy).NotTo(BeIdenticalTo(original))
		Expect(copy.Requests.Equal(requests)).To(BeTrue())
		Expect(copy.Limits.Equal(limits)).To(BeTrue())
	})

	It("should deep copy PercentageIncrease", func() {
		original := &PercentageIncrease{
			Value: 200,
		}
		copy := original.DeepCopy()
		Expect(copy).NotTo(BeIdenticalTo(original))
		Expect(copy.Value).To(Equal(original.Value))
	})

	It("should deep copy PodConditionDurationPolicy", func() {
		original := &PodConditionDurationPolicy{
			Type:   corev1.PodReady,
			Status: corev1.ConditionTrue,
		}
		copy := original.DeepCopy()
		Expect(copy).NotTo(BeIdenticalTo(original))
		Expect(copy.Type).To(Equal(original.Type))
		Expect(copy.Status).To(Equal(original.Status))
	})

	It("should deep copy ResourcePolicy", func() {
		original := &ResourcePolicy{
			ContainerPolicies: []ContainerPolicy{
				{
					ContainerName: "container-1",
					PercentageIncrease: &PercentageIncrease{
						Value: 200,
					},
				},
			},
		}
		copy := original.DeepCopy()
		Expect(copy).NotTo(BeIdenticalTo(original))
		Expect(len(copy.ContainerPolicies)).To(Equal(len(original.ContainerPolicies)))
		Expect(copy.ContainerPolicies[0].ContainerName).To(Equal(original.ContainerPolicies[0].ContainerName))
		// Modify copy and verify original is unchanged
		copy.ContainerPolicies[0].ContainerName = "modified"
		Expect(original.ContainerPolicies[0].ContainerName).To(Equal("container-1"))
	})

	It("should deep copy StartupCPUBoost", func() {
		original := &StartupCPUBoost{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-boost",
				Namespace: "default",
			},
			Spec: StartupCPUBoostSpec{
				DurationPolicy: DurationPolicy{
					Fixed: &FixedDurationPolicy{
						Unit:  FixedDurationPolicyUnitSec,
						Value: 300,
					},
				},
			},
		}
		copy := original.DeepCopy()
		Expect(copy).NotTo(BeIdenticalTo(original))
		Expect(copy.Name).To(Equal(original.Name))
		Expect(copy.Spec.DurationPolicy.Fixed.Value).To(Equal(original.Spec.DurationPolicy.Fixed.Value))
		// Modify copy and verify original is unchanged
		copy.Spec.DurationPolicy.Fixed.Value = 600
		Expect(original.Spec.DurationPolicy.Fixed.Value).To(Equal(int64(300)))
	})

	It("should deep copy StartupCPUBoostSpec", func() {
		original := &StartupCPUBoostSpec{
			DurationPolicy: DurationPolicy{
				Fixed: &FixedDurationPolicy{
					Unit:  FixedDurationPolicyUnitSec,
					Value: 300,
				},
			},
			Triggers: []BoostTrigger{
				{Type: BoostTriggerTypePodCreate},
			},
		}
		copy := original.DeepCopy()
		Expect(copy).NotTo(BeIdenticalTo(original))
		Expect(copy.DurationPolicy.Fixed.Value).To(Equal(original.DurationPolicy.Fixed.Value))
		Expect(len(copy.Triggers)).To(Equal(len(original.Triggers)))
		// Modify copy and verify original is unchanged
		copy.Triggers[0].Type = BoostTriggerTypeContainerRestart
		Expect(original.Triggers[0].Type).To(Equal(BoostTriggerTypePodCreate))
	})

	It("should deep copy StartupCPUBoostStatus", func() {
		original := &StartupCPUBoostStatus{
			ActiveContainerBoosts: 5,
			TotalContainerBoosts:  10,
			Conditions: []metav1.Condition{
				{
					Type:   "Active",
					Status: metav1.ConditionTrue,
				},
			},
		}
		copy := original.DeepCopy()
		Expect(copy).NotTo(BeIdenticalTo(original))
		Expect(copy.ActiveContainerBoosts).To(Equal(original.ActiveContainerBoosts))
		Expect(len(copy.Conditions)).To(Equal(len(original.Conditions)))
		// Modify copy and verify original is unchanged
		copy.ActiveContainerBoosts = 20
		Expect(original.ActiveContainerBoosts).To(Equal(int32(5)))
	})

	It("should deep copy StartupCPUBoostList", func() {
		original := &StartupCPUBoostList{
			Items: []StartupCPUBoost{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "boost-1",
					},
				},
			},
		}
		copy := original.DeepCopy()
		Expect(copy).NotTo(BeIdenticalTo(original))
		Expect(len(copy.Items)).To(Equal(len(original.Items)))
		Expect(copy.Items[0].Name).To(Equal(original.Items[0].Name))
		// Modify copy and verify original is unchanged
		copy.Items[0].Name = "modified"
		Expect(original.Items[0].Name).To(Equal("boost-1"))
	})

	It("should implement DeepCopyObject for StartupCPUBoost", func() {
		boost := &StartupCPUBoost{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-boost",
			},
		}
		obj := boost.DeepCopyObject()
		Expect(obj).NotTo(BeNil())
		copy, ok := obj.(*StartupCPUBoost)
		Expect(ok).To(BeTrue())
		Expect(copy.Name).To(Equal(boost.Name))
	})

	It("should implement DeepCopyObject for StartupCPUBoostList", func() {
		list := &StartupCPUBoostList{
			Items: []StartupCPUBoost{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "boost-1",
					},
				},
			},
		}
		obj := list.DeepCopyObject()
		Expect(obj).NotTo(BeNil())
		copy, ok := obj.(*StartupCPUBoostList)
		Expect(ok).To(BeTrue())
		Expect(len(copy.Items)).To(Equal(len(list.Items)))
	})
})

var _ = Describe("init function and scheme registration", func() {
	It("should have GroupVersion defined", func() {
		Expect(GroupVersion.Group).To(Equal("autoscaling.x-k8s.io"))
		Expect(GroupVersion.Version).To(Equal("v1alpha1"))
	})

	It("should have SchemeBuilder defined", func() {
		Expect(SchemeBuilder).NotTo(BeNil())
		Expect(SchemeBuilder.GroupVersion).To(Equal(GroupVersion))
	})

	It("should have AddToScheme function", func() {
		Expect(AddToScheme).NotTo(BeNil())
	})
})
