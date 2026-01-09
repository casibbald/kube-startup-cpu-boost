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

package metrics_test

import (
	"github.com/google/kube-startup-cpu-boost/internal/metrics"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Metrics", func() {
	Describe("registers new boost configuration", func() {
		var (
			namespace = "default"
		)
		BeforeEach(func() {
			metrics.ClearSystemMetrics()
		})
		JustBeforeEach(func() {
			metrics.NewBoostConfiguration(namespace)
			metrics.NewBoostConfiguration(namespace)
		})
		It("updates the boost configurations metric", func() {
			Expect(metrics.BoostConfigurations(namespace)).To(Equal(float64(2)))
		})
	})
	Describe("deletes boost configuration", func() {
		var (
			namespace = "default"
		)
		BeforeEach(func() {
			metrics.ClearSystemMetrics()
		})
		JustBeforeEach(func() {
			metrics.NewBoostConfiguration(namespace)
			metrics.NewBoostConfiguration(namespace)
			metrics.DeleteBoostConfiguration(namespace)
		})
		It("updates the boost configurations metric", func() {
			Expect(metrics.BoostConfigurations(namespace)).To(Equal(float64(1)))
		})
	})
	Describe("sets active container boost metric", func() {
		var (
			namespace = "default"
			boost     = "boost-01"
			value     = float64(5)
		)
		BeforeEach(func() {
			metrics.ClearBoostMetrics(namespace, boost)
		})
		JustBeforeEach(func() {
			metrics.SetBoostContainersActive(namespace, boost, value)
		})
		It("updates the active container boosts metric", func() {
			Expect(metrics.BoostContainersActive(namespace, boost)).To(Equal(value))
		})
	})
	Describe("adds total container boost metric", func() {
		var (
			namespace = "default"
			boost     = "boost-01"
			value     = float64(5)
		)
		BeforeEach(func() {
			metrics.ClearBoostMetrics(namespace, boost)
		})
		JustBeforeEach(func() {
			metrics.AddBoostContainersTotal(namespace, boost, 3)
			metrics.AddBoostContainersTotal(namespace, boost, value)
		})
		It("updates the total container boosts metric", func() {
			Expect(metrics.BoostContainersTotal(namespace, boost)).To(Equal(float64(8)))
		})
	})
	Describe("increments boost activations metric", func() {
		var (
			namespace = "default"
			boost     = "boost-01"
			trigger   = "PodCreate"
		)
		BeforeEach(func() {
			metrics.ClearBoostMetrics(namespace, boost)
		})
		JustBeforeEach(func() {
			metrics.IncrementBoostActivations(trigger, boost, namespace)
			metrics.IncrementBoostActivations(trigger, boost, namespace)
		})
		It("updates the boost activations total metric", func() {
			Expect(metrics.BoostActivationsTotal(trigger, boost, namespace)).To(Equal(float64(2)))
		})
	})
	Describe("sets active boost metric", func() {
		var (
			namespace = "default"
			boost     = "boost-01"
		)
		BeforeEach(func() {
			metrics.ClearBoostMetrics(namespace, boost)
		})
		JustBeforeEach(func() {
			metrics.SetBoostActive(boost, namespace, 1)
		})
		It("updates the active boost gauge", func() {
			Expect(metrics.BoostActive(boost, namespace)).To(Equal(float64(1)))
		})
		When("boost is deactivated", func() {
			JustBeforeEach(func() {
				metrics.SetBoostActive(boost, namespace, 0)
			})
			It("sets the active boost gauge to zero", func() {
				Expect(metrics.BoostActive(boost, namespace)).To(Equal(float64(0)))
			})
		})
	})
	Describe("increments boost skipped metric", func() {
		var (
			namespace = "default"
			boost     = "boost-01"
			reason    = "cooldown"
		)
		BeforeEach(func() {
			metrics.ClearBoostMetrics(namespace, boost)
		})
		JustBeforeEach(func() {
			metrics.IncrementBoostSkipped(reason, boost, namespace)
			metrics.IncrementBoostSkipped(reason, boost, namespace)
		})
		It("updates the boost skipped total metric", func() {
			Expect(metrics.BoostSkippedTotal(reason, boost, namespace)).To(Equal(float64(2)))
		})
	})
	Describe("tracks boost skipped metric for different reasons", func() {
		var (
			namespace = "default"
			boost     = "boost-02"
			reason1   = "cooldown"
			reason2   = "idempotency"
		)
		BeforeEach(func() {
			metrics.ClearBoostMetrics(namespace, boost)
		})
		JustBeforeEach(func() {
			metrics.IncrementBoostSkipped(reason1, boost, namespace)
			metrics.IncrementBoostSkipped(reason1, boost, namespace)
			metrics.IncrementBoostSkipped(reason2, boost, namespace)
		})
		It("tracks each reason separately", func() {
			Expect(metrics.BoostSkippedTotal(reason1, boost, namespace)).To(Equal(float64(2)))
			Expect(metrics.BoostSkippedTotal(reason2, boost, namespace)).To(Equal(float64(1)))
		})
	})
	Describe("tracks activations for all trigger types", func() {
		var (
			namespace = "default"
			boost     = "boost-03"
		)
		BeforeEach(func() {
			metrics.ClearBoostMetrics(namespace, boost)
		})
		JustBeforeEach(func() {
			metrics.IncrementBoostActivations("PodCreate", boost, namespace)
			metrics.IncrementBoostActivations("ContainerRestart", boost, namespace)
			metrics.IncrementBoostActivations("PodConditionTransition", boost, namespace)
		})
		It("tracks each trigger type separately", func() {
			Expect(metrics.BoostActivationsTotal("PodCreate", boost, namespace)).To(Equal(float64(1)))
			Expect(metrics.BoostActivationsTotal("ContainerRestart", boost, namespace)).To(Equal(float64(1)))
			Expect(metrics.BoostActivationsTotal("PodConditionTransition", boost, namespace)).To(Equal(float64(1)))
		})
	})
	Describe("active gauge lifecycle", func() {
		var (
			namespace = "default"
			boost     = "boost-04"
		)
		BeforeEach(func() {
			metrics.ClearBoostMetrics(namespace, boost)
		})
		When("boost is activated", func() {
			JustBeforeEach(func() {
				metrics.SetBoostActive(boost, namespace, 1)
			})
			It("sets active gauge to 1", func() {
				Expect(metrics.BoostActive(boost, namespace)).To(Equal(float64(1)))
			})
			When("boost is activated again", func() {
				JustBeforeEach(func() {
					metrics.SetBoostActive(boost, namespace, 1)
				})
				It("stays at 1 (does not increment)", func() {
					Expect(metrics.BoostActive(boost, namespace)).To(Equal(float64(1)))
				})
			})
			When("boost expires", func() {
				JustBeforeEach(func() {
					metrics.SetBoostActive(boost, namespace, 0)
				})
				It("sets active gauge to 0", func() {
					Expect(metrics.BoostActive(boost, namespace)).To(Equal(float64(0)))
				})
				When("boost is reactivated", func() {
					JustBeforeEach(func() {
						metrics.SetBoostActive(boost, namespace, 1)
					})
					It("sets active gauge back to 1", func() {
						Expect(metrics.BoostActive(boost, namespace)).To(Equal(float64(1)))
					})
				})
			})
		})
	})
	Describe("namespace isolation", func() {
		var (
			namespace1 = "namespace-1"
			namespace2 = "namespace-2"
			boost      = "same-boost-name"
		)
		BeforeEach(func() {
			metrics.ClearBoostMetrics(namespace1, boost)
			metrics.ClearBoostMetrics(namespace2, boost)
		})
		JustBeforeEach(func() {
			metrics.IncrementBoostActivations("PodCreate", boost, namespace1)
			metrics.IncrementBoostActivations("PodCreate", boost, namespace2)
			metrics.SetBoostActive(boost, namespace1, 1)
			metrics.SetBoostActive(boost, namespace2, 1)
		})
		It("tracks metrics separately per namespace", func() {
			Expect(metrics.BoostActivationsTotal("PodCreate", boost, namespace1)).To(Equal(float64(1)))
			Expect(metrics.BoostActivationsTotal("PodCreate", boost, namespace2)).To(Equal(float64(1)))
			Expect(metrics.BoostActive(boost, namespace1)).To(Equal(float64(1)))
			Expect(metrics.BoostActive(boost, namespace2)).To(Equal(float64(1)))
		})
	})
	Describe("multiple activations with different triggers", func() {
		var (
			namespace = "default"
			boost     = "boost-05"
		)
		BeforeEach(func() {
			metrics.ClearBoostMetrics(namespace, boost)
		})
		JustBeforeEach(func() {
			// Activate via PodCreate
			metrics.IncrementBoostActivations("PodCreate", boost, namespace)
			metrics.SetBoostActive(boost, namespace, 1)
			// Try to activate via ContainerRestart (idempotency - should skip)
			metrics.IncrementBoostSkipped("idempotency", boost, namespace)
			// Boost expires
			metrics.SetBoostActive(boost, namespace, 0)
			// Reactivate via ContainerRestart
			metrics.IncrementBoostActivations("ContainerRestart", boost, namespace)
			metrics.SetBoostActive(boost, namespace, 1)
		})
		It("tracks activations and skips correctly", func() {
			Expect(metrics.BoostActivationsTotal("PodCreate", boost, namespace)).To(Equal(float64(1)))
			Expect(metrics.BoostActivationsTotal("ContainerRestart", boost, namespace)).To(Equal(float64(1)))
			Expect(metrics.BoostSkippedTotal("idempotency", boost, namespace)).To(Equal(float64(1)))
			Expect(metrics.BoostActive(boost, namespace)).To(Equal(float64(1)))
		})
	})
})
