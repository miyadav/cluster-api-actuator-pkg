package capi

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	mapiv1 "github.com/openshift/api/machine/v1beta1"
	framework "github.com/openshift/cluster-api-actuator-pkg/pkg/framework"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gcpv1 "sigs.k8s.io/cluster-api-provider-gcp/api/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest/komega"
	yaml "sigs.k8s.io/yaml"
)

const (
	infraAPIVersion        = "infrastructure.cluster.x-k8s.io/v1beta1"
	gcpMachineTemplateName = "gcp-machine-template"
)

var (
	ctx         = context.Background()
	clusterName string
	cl          client.Client
)

var _ = Describe("Cluster API GCP MachineSet", Ordered, func() {
	var gcpMachineTemplate *gcpv1.GCPMachineTemplate
	var machineSet *clusterv1.MachineSet
	var mapiMachineSpec *mapiv1.GCPMachineProviderSpec
	var ctx context.Context
	var platform configv1.PlatformType
	var clusterName string
	var err error

	BeforeAll(func() {
		cl, err = framework.LoadClient()
		Expect(err).NotTo(HaveOccurred(), "Failed to create Kubernetes client for test")
		komega.SetClient(cl)
		ctx = framework.GetContext()
		platform, err = framework.GetPlatform(ctx, cl)
		Expect(err).ToNot(HaveOccurred(), "Failed to get platform")
		if platform != configv1.GCPPlatformType {
			Skip("Skipping GCP E2E tests")
		}

		infra, err := framework.GetInfrastructure(ctx, cl)
		Expect(err).NotTo(HaveOccurred(), "Failed to get cluster infrastructure object")
		Expect(infra.Status.InfrastructureName).ShouldNot(BeEmpty(), "infrastructure name was empty on Infrastructure.Status.")
		clusterName = infra.Status.InfrastructureName

		framework.CreateCoreCluster(ctx, cl, clusterName, "GCPCluster")
		mapiMachineSpec = getGCPMAPIProviderSpec(cl)
	})
	AfterEach(func() {
		if platform != configv1.GCPPlatformType {
			// Because AfterEach always runs, even when tests are skipped, we have to
			// explicitly skip it here for other platforms.
			Skip("Skipping GCP E2E tests")
		}
		framework.DeleteCAPIMachineSets(ctx, cl, machineSet)
		framework.WaitForCAPIMachineSetsDeleted(ctx, cl, machineSet)
		framework.DeleteObjects(ctx, cl, gcpMachineTemplate)
	})

	It("should be able to run a machine", func() {
		gcpMachineTemplate = createGCPMachineTemplate(cl, mapiMachineSpec)
		machineSet, _ = framework.CreateCAPIMachineSet(ctx, cl, framework.NewCAPIMachineSetParams(
			"gcp-machineset",
			clusterName,
			mapiMachineSpec.Zone,
			1,
			corev1.ObjectReference{
				Kind:       "GCPMachineTemplate",
				APIVersion: infraAPIVersion,
				Name:       gcpMachineTemplateName,
			},
		))
		framework.WaitForCAPIMachineSet(framework.GetContext(), cl, machineSet.Name)
	})
})

func getGCPMAPIProviderSpec(cl client.Client) *mapiv1.GCPMachineProviderSpec {
	machineSetList := &mapiv1.MachineSetList{}
	Expect(cl.List(ctx, machineSetList, client.InNamespace(framework.MachineAPINamespace))).To(Succeed())

	Expect(machineSetList.Items).ToNot(HaveLen(0))
	machineSet := machineSetList.Items[0]
	Expect(machineSet.Spec.Template.Spec.ProviderSpec.Value).ToNot(BeNil())

	providerSpec := &mapiv1.GCPMachineProviderSpec{}
	Expect(yaml.Unmarshal(machineSet.Spec.Template.Spec.ProviderSpec.Value.Raw, providerSpec)).To(Succeed())

	return providerSpec
}

func createGCPMachineTemplate(cl client.Client, mapiProviderSpec *mapiv1.GCPMachineProviderSpec) *gcpv1.GCPMachineTemplate {
	By("Creating GCP machine template")

	Expect(mapiProviderSpec).ToNot(BeNil())
	Expect(mapiProviderSpec.Disks).ToNot(BeNil())
	Expect(len(mapiProviderSpec.Disks)).To(BeNumerically(">", 0))
	Expect(mapiProviderSpec.Disks[0].Type).ToNot(BeEmpty())
	Expect(mapiProviderSpec.MachineType).ToNot(BeEmpty())
	Expect(mapiProviderSpec.NetworkInterfaces).ToNot(BeNil())
	Expect(len(mapiProviderSpec.NetworkInterfaces)).To(BeNumerically(">", 0))
	Expect(mapiProviderSpec.NetworkInterfaces[0].Subnetwork).ToNot(BeEmpty())
	Expect(mapiProviderSpec.ServiceAccounts).ToNot(BeNil())
	Expect(mapiProviderSpec.ServiceAccounts[0].Email).ToNot(BeEmpty())
	Expect(mapiProviderSpec.ServiceAccounts[0].Scopes).ToNot(BeNil())
	Expect(len(mapiProviderSpec.ServiceAccounts)).To(BeNumerically(">", 0))
	Expect(mapiProviderSpec.Tags).ToNot(BeNil())
	Expect(len(mapiProviderSpec.Tags)).To(BeNumerically(">", 0))

	var rootDeviceType gcpv1.DiskType

	switch mapiProviderSpec.Disks[0].Type {
	case "pd-standard":
		rootDeviceType = gcpv1.PdStandardDiskType
	case "pd-ssd":
		rootDeviceType = gcpv1.PdSsdDiskType
	case "local-ssd":
		rootDeviceType = gcpv1.LocalSsdDiskType
	}

	ipForwardingDisabled := gcpv1.IPForwardingDisabled
	gcpMachineSpec := gcpv1.GCPMachineSpec{
		RootDeviceType: &rootDeviceType,
		RootDeviceSize: mapiProviderSpec.Disks[0].SizeGB,
		InstanceType:   mapiProviderSpec.MachineType,
		Image:          &mapiProviderSpec.Disks[0].Image,
		Subnet:         &mapiProviderSpec.NetworkInterfaces[0].Subnetwork,
		ServiceAccount: &gcpv1.ServiceAccount{
			Email:  mapiProviderSpec.ServiceAccounts[0].Email,
			Scopes: mapiProviderSpec.ServiceAccounts[0].Scopes,
		},
		AdditionalNetworkTags: mapiProviderSpec.Tags,
		AdditionalLabels:      gcpv1.Labels{fmt.Sprintf("kubernetes-io-cluster-%s", clusterName): "owned"},
		IPForwarding:          &ipForwardingDisabled,
	}

	gcpMachineTemplate := &gcpv1.GCPMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gcpMachineTemplateName,
			Namespace: framework.ClusterAPINamespace,
		},
		Spec: gcpv1.GCPMachineTemplateSpec{
			Template: gcpv1.GCPMachineTemplateResource{
				Spec: gcpMachineSpec,
			},
		},
	}

	if err := cl.Create(ctx, gcpMachineTemplate); err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).ToNot(HaveOccurred())
	}

	return gcpMachineTemplate
}