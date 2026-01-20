// SPDX-License-Identifier:Apache-2.0

package tests

import (
	"context"
	"fmt"
	"sort"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/openperouter/openperouter/api/v1alpha1"
	"github.com/openperouter/openperouter/e2etests/pkg/config"
	"github.com/openperouter/openperouter/e2etests/pkg/k8sclient"
	"github.com/openperouter/openperouter/e2etests/pkg/openperouter"
	"github.com/openperouter/openperouter/e2etests/pkg/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientset "k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func getStatusList(k8sClient client.Client) *v1alpha1.RouterNodeConfigurationStatusList {
	GinkgoHelper()
	statusList := &v1alpha1.RouterNodeConfigurationStatusList{}
	err := k8sClient.List(context.Background(), statusList, client.InNamespace(openperouter.Namespace))
	Expect(err).To(Succeed())
	return statusList
}

func getControllerNodes(k8sClient client.Client, hostMode bool) []string {
	GinkgoHelper()
	if hostMode {
		nodeList := &corev1.NodeList{}
		err := k8sClient.List(context.Background(), nodeList)
		Expect(err).To(Succeed())

		nodeNames := make([]string, 0, len(nodeList.Items))
		for _, node := range nodeList.Items {
			nodeNames = append(nodeNames, node.Name)
		}
		return nodeNames
	}

	podList := &corev1.PodList{}
	err := k8sClient.List(context.Background(), podList,
		client.InNamespace(openperouter.Namespace),
		client.MatchingLabels{"app": "controller"})
	Expect(err).To(Succeed())

	controllerNodes := make(map[string]struct{})
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning {
			controllerNodes[pod.Spec.NodeName] = struct{}{}
		}
	}

	nodeNames := make([]string, 0, len(controllerNodes))
	for nodeName := range controllerNodes {
		nodeNames = append(nodeNames, nodeName)
	}
	return nodeNames
}

func sliceToSet(slice []string) map[string]struct{} {
	set := make(map[string]struct{}, len(slice))
	for _, item := range slice {
		set[item] = struct{}{}
	}
	return set
}

func setDifference(setA, setB map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{})
	for item := range setA {
		if _, exists := setB[item]; !exists {
			result[item] = struct{}{}
		}
	}
	return result
}

func setToSlice(set map[string]struct{}) []string {
	slice := make([]string, 0, len(set))
	for item := range set {
		slice = append(slice, item)
	}
	sort.Strings(slice)
	return slice
}

func getStabilizedStatusList(k8sClient client.Client, hostMode bool) (*v1alpha1.RouterNodeConfigurationStatusList, error) {
	controllerNodes := getControllerNodes(k8sClient, hostMode)
	statusList := getStatusList(k8sClient)

	if len(controllerNodes) == 0 {
		return nil, fmt.Errorf("expected at least one controller to be running")
	}

	if len(statusList.Items) != len(controllerNodes) {
		return nil, fmt.Errorf("expected %d RouterNodeConfigurationStatus resources (one per controller node), got %d",
			len(controllerNodes), len(statusList.Items))
	}

	return statusList, nil
}

var _ = Describe("RouterNodeConfigurationStatus CRD", func() {
	var cs clientset.Interface

	BeforeEach(func() {
		cs = k8sclient.New()
		Expect(Updater.CleanAll()).To(Succeed())
	})

	AfterEach(func() {
		dumpIfFails(cs)
		Expect(Updater.CleanAll()).To(Succeed())
	})

	Context("Lifecycle Management", func() {
		var k8sClient client.Client

		BeforeEach(func() {
			k8sClient = Updater.Client()
		})

		It("should automatically create RouterNodeConfigurationStatus resources with the expected name for each node with a running controller", func() {
			Eventually(func() error {
				controllerNodes := getControllerNodes(k8sClient, HostMode)
				statusList := getStatusList(k8sClient)

				if len(controllerNodes) == 0 {
					return fmt.Errorf("expected at least one controller to be running")
				}

				controllerNodesSet := sliceToSet(controllerNodes)

				resourceNames := make([]string, 0, len(statusList.Items))
				for _, status := range statusList.Items {
					resourceNames = append(resourceNames, status.Name)
				}
				resourceNamesSet := sliceToSet(resourceNames)

				uncoveredNodes := setDifference(controllerNodesSet, resourceNamesSet)
				unmatchedResources := setDifference(resourceNamesSet, controllerNodesSet)

				var errorParts []string
				if len(uncoveredNodes) > 0 {
					errorParts = append(errorParts, fmt.Sprintf("missing RouterNodeConfigurationStatus resources for controller nodes: %v", setToSlice(uncoveredNodes)))
				}
				if len(unmatchedResources) > 0 {
					errorParts = append(errorParts, fmt.Sprintf("unmatched RouterNodeConfigurationStatus resources (no running controller): %v", setToSlice(unmatchedResources)))
				}
				if len(errorParts) > 0 {
					return fmt.Errorf(strings.Join(errorParts, "; "))
				}

				return nil
			}, "60s", "5s").Should(Succeed(), "RouterNodeConfigurationStatus resources should be created for nodes with running controllers")
		})

		It("should recreate RouterNodeConfigurationStatus resources if manually deleted", func() {
			var initialStatusList *v1alpha1.RouterNodeConfigurationStatusList

			Eventually(func() error {
				statusList, err := getStabilizedStatusList(k8sClient, HostMode)
				if err != nil {
					return err
				}
				initialStatusList = statusList
				return nil
			}, "60s", "5s").Should(Succeed(), "Initial RouterNodeConfigurationStatus resources should be created")

			resourceToDelete := &initialStatusList.Items[0]
			originalName := resourceToDelete.Name
			originalNamespace := resourceToDelete.Namespace
			originalLastUpdateTime := resourceToDelete.Status.LastUpdateTime

			Expect(k8sClient.Delete(context.Background(), resourceToDelete)).To(Succeed(),
				fmt.Sprintf("Should be able to delete RouterNodeConfigurationStatus %s", originalName))

			Eventually(func() error {
				recreatedResource := &v1alpha1.RouterNodeConfigurationStatus{}
				err := k8sClient.Get(context.Background(),
					types.NamespacedName{Name: originalName, Namespace: originalNamespace}, recreatedResource)
				if err != nil {
					return fmt.Errorf("RouterNodeConfigurationStatus %s should be recreated: %v", originalName, err)
				}
				if recreatedResource.Status.LastUpdateTime == nil {
					return fmt.Errorf("recreated RouterNodeConfigurationStatus %s should have lastUpdateTime set", recreatedResource.Name)
				}
				if originalLastUpdateTime != nil && !recreatedResource.Status.LastUpdateTime.After(originalLastUpdateTime.Time) {
					return fmt.Errorf("recreated RouterNodeConfigurationStatus %s should have a newer timestamp", recreatedResource.Name)
				}
				return nil
			}, "60s", "5s").Should(Succeed(), "RouterNodeConfigurationStatus should be recreated with proper configuration")
		})

		It("should have proper owner references linking to Node resources", func() {
			Eventually(func() error {
				statusList, err := getStabilizedStatusList(k8sClient, HostMode)
				if err != nil {
					return err
				}

				for _, status := range statusList.Items {
					if len(status.OwnerReferences) == 0 {
						return fmt.Errorf("RouterNodeConfigurationStatus %s should have owner references", status.Name)
					}

					var nodeOwnerRef *metav1.OwnerReference
					for _, ownerRef := range status.OwnerReferences {
						if ownerRef.Kind == "Node" && ownerRef.APIVersion == "v1" {
							nodeOwnerRef = &ownerRef
							break
						}
					}

					if nodeOwnerRef == nil {
						return fmt.Errorf("RouterNodeConfigurationStatus %s should have Node owner reference", status.Name)
					}
					if nodeOwnerRef.Name != status.Name {
						return fmt.Errorf("Owner reference should point to node %s, got %s", status.Name, nodeOwnerRef.Name)
					}
				}

				return nil
			}, "60s", "5s").Should(Succeed(), "RouterNodeConfigurationStatus resources should have proper owner references")
		})

		It("should track multiple resource failures and recover properly", func() {
			invalidUnderlay := v1alpha1.Underlay{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-underlay",
					Namespace: openperouter.Namespace,
				},
				Spec: v1alpha1.UnderlaySpec{
					ASN:  64514,
					Nics: []string{"nonexistent"},
					EVPN: &v1alpha1.EVPNConfig{
						VTEPCIDR: "100.65.0.0/24",
					},
					Neighbors: []v1alpha1.Neighbor{
						{
							ASN:     64512,
							Address: "192.168.11.2",
						},
					},
				},
			}

			By("creating invalid underlay")
			Expect(Updater.Update(config.Resources{
				Underlays: []v1alpha1.Underlay{invalidUnderlay},
			})).To(Succeed())

			By("confirming underlay status is failed")
			status.ExpectResourceFailure(k8sClient, "Underlay", invalidUnderlay.Name, HostMode)

			fixedUnderlay := invalidUnderlay.DeepCopy()
			fixedUnderlay.Spec.Nics = []string{"toswitch"}

			By("fixing underlay")
			Expect(Updater.Update(config.Resources{
				Underlays: []v1alpha1.Underlay{*fixedUnderlay},
			})).To(Succeed())

			By("confirming underlay status is now OK")
			status.ExpectSuccessfulStatus(k8sClient, HostMode)

			invalidL2VNI := v1alpha1.L2VNI{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-l2vni",
					Namespace: openperouter.Namespace,
				},
				Spec: v1alpha1.L2VNISpec{
					VNI: 500,
					HostMaster: &v1alpha1.HostMaster{
						Name:       "nonexist-br",
						Type:       "bridge",
						AutoCreate: false,
					},
				},
			}

			By("creating invalid L2VNI referencing non-existent host bridge")
			Expect(Updater.Update(config.Resources{
				Underlays: []v1alpha1.Underlay{*fixedUnderlay},
				L2VNIs:    []v1alpha1.L2VNI{invalidL2VNI},
			})).To(Succeed())

			By("confirming L2VNI status is failed")
			status.ExpectResourceFailure(k8sClient, "L2VNI", invalidL2VNI.Name, HostMode)

			By("removing the failing L2VNI")
			Expect(k8sClient.Delete(context.Background(), &invalidL2VNI)).To(Succeed())

			By("confirming status is OK after removing L2VNI")
			status.ExpectSuccessfulStatus(k8sClient, HostMode)
		})

		It("should verify owner references enable garbage collection on node removal", func() {
			// This test verifies that RouterNodeConfigurationStatus resources have proper
			// owner references to Node resources, which enables automatic cleanup via
			// Kubernetes garbage collection when a node is removed from the cluster.
			// Actual node removal is not tested as it would disrupt the cluster.
			Eventually(func() error {
				statusList, err := getStabilizedStatusList(k8sClient, HostMode)
				if err != nil {
					return err
				}

				for _, statusResource := range statusList.Items {
					if len(statusResource.OwnerReferences) == 0 {
						return fmt.Errorf("RouterNodeConfigurationStatus %s has no owner references", statusResource.Name)
					}

					var hasNodeOwner bool
					for _, ownerRef := range statusResource.OwnerReferences {
						if ownerRef.Kind == "Node" && ownerRef.APIVersion == "v1" {
							hasNodeOwner = true
							if ownerRef.Name != statusResource.Name {
								return fmt.Errorf("RouterNodeConfigurationStatus %s has Node owner reference pointing to %s instead of itself",
									statusResource.Name, ownerRef.Name)
							}
							break
						}
					}

					if !hasNodeOwner {
						return fmt.Errorf("RouterNodeConfigurationStatus %s missing Node owner reference for garbage collection", statusResource.Name)
					}
				}

				return nil
			}, "60s", "5s").Should(Succeed(), "RouterNodeConfigurationStatus resources should have Node owner references for garbage collection")
		})

		It("should create status resources only for nodes where controller is running", func() {
			// This test verifies that RouterNodeConfigurationStatus resources are created
			// only for nodes that have a running controller (honoring any node selectors
			// configured for the controller deployment).
			Eventually(func() error {
				controllerNodes := getControllerNodes(k8sClient, HostMode)
				if len(controllerNodes) == 0 {
					return fmt.Errorf("expected at least one controller node")
				}

				statusList := getStatusList(k8sClient)
				statusNames := make(map[string]struct{}, len(statusList.Items))
				for _, s := range statusList.Items {
					statusNames[s.Name] = struct{}{}
				}

				controllerNodesSet := sliceToSet(controllerNodes)

				// Verify each status resource corresponds to a controller node
				for statusName := range statusNames {
					if _, ok := controllerNodesSet[statusName]; !ok {
						return fmt.Errorf("RouterNodeConfigurationStatus %s exists but no controller running on that node", statusName)
					}
				}

				// Verify each controller node has a corresponding status resource
				for node := range controllerNodesSet {
					if _, ok := statusNames[node]; !ok {
						return fmt.Errorf("controller node %s has no RouterNodeConfigurationStatus resource", node)
					}
				}

				return nil
			}, "60s", "5s").Should(Succeed(), "RouterNodeConfigurationStatus resources should match controller node selection")
		})
	})
})
