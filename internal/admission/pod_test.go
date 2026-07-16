package admission

import (
	"context"
	"reflect"
	"testing"

	wayv1 "github.com/Amoenus/waycloak/api/v1alpha1"
	"github.com/Amoenus/waycloak/internal/contract"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testAgentImage = "registry.example/waycloak-agent@sha256:1111111111111111111111111111111111111111111111111111111111111111"
const testAdmissionGeneration = "1111111111111111111111111111111111111111111111111111111111111111"

func testMutator(t *testing.T, selector metav1.LabelSelector) *PodMutator {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = wayv1.AddToScheme(scheme)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "apps", Labels: map[string]string{"waycloak": "allowed"}}}
	gw := &wayv1.VPNGateway{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "egress"}, Spec: wayv1.VPNGatewaySpec{WorkloadAccess: wayv1.WorkloadAccessSpec{NamespaceSelector: selector}}}
	generation := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "admission-generation", Namespace: "system"}, Data: map[string]string{contract.AdmissionGenerationKey: testAdmissionGeneration}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns, gw, generation).Build()
	return &PodMutator{Client: cl, Scheme: scheme, AgentImage: testAgentImage, GenerationGate: &GenerationGate{Reader: cl, Namespace: "system", ConfigMap: generation.Name, Generation: testAdmissionGeneration}}
}

func TestUnannotatedPodCompletelyUnchanged(t *testing.T) {
	m := testMutator(t, metav1.LabelSelector{})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "apps"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app"}}}}
	before := pod.DeepCopy()
	changed, err := m.Mutate(context.Background(), pod)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !reflect.DeepEqual(before, pod) {
		t.Fatal("unannotated Pod changed")
	}
}

func TestAnnotatedMutationIsDeterministicAndIdempotent(t *testing.T) {
	m := testMutator(t, metav1.LabelSelector{MatchLabels: map[string]string{"waycloak": "allowed"}})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", Annotations: map[string]string{contract.GatewayAnnotation: "egress/private"}}, Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: "application-init", Image: "app"}}, Containers: []corev1.Container{{Name: "app", Image: "app"}}}}
	originalApp := pod.Spec.Containers[0].DeepCopy()
	changed, err := m.Mutate(context.Background(), pod)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	after := pod.DeepCopy()
	changed, err = m.Mutate(context.Background(), pod)
	if err != nil || changed {
		t.Fatalf("second changed=%v err=%v", changed, err)
	}
	if !reflect.DeepEqual(after, pod) {
		t.Fatal("second mutation changed Pod")
	}
	if !reflect.DeepEqual(originalApp, &pod.Spec.Containers[0]) {
		t.Fatal("application container was modified")
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("token automount not disabled")
	}
	if pod.Annotations[contract.AdmissionGenerationAnnotation] != testAdmissionGeneration {
		t.Fatalf("admission generation = %q", pod.Annotations[contract.AdmissionGenerationAnnotation])
	}
	if pod.Spec.Volumes[len(pod.Spec.Volumes)-1].ConfigMap.Optional == nil || *pod.Spec.Volumes[len(pod.Spec.Volumes)-1].ConfigMap.Optional {
		t.Fatal("allocation volume must be required")
	}
	names := make([]string, 0, len(pod.Spec.InitContainers))
	for _, container := range pod.Spec.InitContainers {
		names = append(names, container.Name)
	}
	if !reflect.DeepEqual(names, []string{contract.PrepareContainer, contract.VerifyContainer, "application-init"}) {
		t.Fatalf("deny-first init ordering = %v", names)
	}
	for _, injected := range pod.Spec.InitContainers[:2] {
		if injected.SecurityContext == nil || injected.SecurityContext.Capabilities == nil || !reflect.DeepEqual(injected.SecurityContext.Capabilities.Add, []corev1.Capability{"NET_ADMIN"}) {
			t.Fatalf("%s capabilities = %#v", injected.Name, injected.SecurityContext)
		}
	}
	agent := pod.Spec.Containers[len(pod.Spec.Containers)-1]
	if agent.Name != contract.AgentContainer || agent.ReadinessProbe == nil || agent.ReadinessProbe.Exec == nil || !reflect.DeepEqual(agent.ReadinessProbe.Exec.Command, []string{"/proc/1/exe", "probe"}) {
		t.Fatalf("agent readiness probe = %#v", agent.ReadinessProbe)
	}
}

func TestStaleGenerationRejectsOnlyAnnotatedPods(t *testing.T) {
	m := testMutator(t, metav1.LabelSelector{})
	m.GenerationGate.Generation = "stale"
	protected := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "protected", Namespace: "apps", Annotations: map[string]string{contract.GatewayAnnotation: "egress/private"}}}
	if _, err := m.Mutate(context.Background(), protected); err == nil {
		t.Fatal("stale webhook admitted an annotated Pod")
	} else if rejection, ok := err.(*Rejection); !ok || rejection.Reason != waystatus.ReasonAdmissionGenerationConflict {
		t.Fatalf("error = %#v", err)
	}
	plain := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "apps"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app"}}}}
	before := plain.DeepCopy()
	if changed, err := m.Mutate(context.Background(), plain); err != nil || changed || !reflect.DeepEqual(before, plain) {
		t.Fatalf("stale gate changed unannotated Pod: changed=%v error=%v", changed, err)
	}
}

func TestGeneratedPodAllocationMarkerSurvivesFinalNameAndReinvocation(t *testing.T) {
	m := testMutator(t, metav1.LabelSelector{MatchLabels: map[string]string{"waycloak": "allowed"}})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: "app-", Namespace: "apps", Annotations: map[string]string{contract.GatewayAnnotation: "egress/private"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app"}}}}
	changed, err := m.mutate(context.Background(), pod, "admission-request-1")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	marker := pod.Annotations[contract.AllocationNameAnnotation]
	if marker != contract.AllocationConfigMapName("apps", "admission-request-1") {
		t.Fatalf("allocation marker = %q", marker)
	}
	pod.Name = "app-generated"
	changed, err = m.mutate(context.Background(), pod, "admission-reinvocation-2")
	if err != nil || changed {
		t.Fatalf("reinvocation changed=%v err=%v", changed, err)
	}
	if pod.Annotations[contract.AllocationNameAnnotation] != marker {
		t.Fatal("reinvocation changed the allocation marker")
	}
}

func TestUnauthorizedGatewayRejected(t *testing.T) {
	m := testMutator(t, metav1.LabelSelector{MatchLabels: map[string]string{"other": "yes"}})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", Annotations: map[string]string{contract.GatewayAnnotation: "egress/private"}}}
	_, err := m.Mutate(context.Background(), pod)
	r, ok := err.(*Rejection)
	if !ok || r.Reason != "UnauthorizedGateway" {
		t.Fatalf("got %#v", err)
	}
}

func TestExplicitServiceAccountTokenRejected(t *testing.T) {
	yes := true
	m := testMutator(t, metav1.LabelSelector{})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", Annotations: map[string]string{contract.GatewayAnnotation: "egress/private"}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &yes}}
	_, err := m.Mutate(context.Background(), pod)
	r, ok := err.(*Rejection)
	if !ok || r.Reason != "ApplicationCredentialsForbidden" {
		t.Fatalf("got %#v", err)
	}
}

func TestDefaultServiceAccountProjectionIsRemovedFromProtectedPod(t *testing.T) {
	m := testMutator(t, metav1.LabelSelector{})
	mode := int32(420)
	expires := int64(3607)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", Annotations: map[string]string{contract.GatewayAnnotation: "egress/private"}},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{
				{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "kube-api-access-abcde", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{DefaultMode: &mode, Sources: []corev1.VolumeProjection{
					{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token", ExpirationSeconds: &expires}},
					{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"}, Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}},
					{DownwardAPI: &corev1.DownwardAPIProjection{Items: []corev1.DownwardAPIVolumeFile{{Path: "namespace", FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.namespace"}}}}},
				}}}},
			},
			InitContainers:      []corev1.Container{{Name: "application-init", Image: "app", VolumeMounts: []corev1.VolumeMount{{Name: "kube-api-access-abcde", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount"}}}},
			Containers:          []corev1.Container{{Name: "app", Image: "app", VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}, {Name: "kube-api-access-abcde", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount", ReadOnly: true}}}},
			EphemeralContainers: []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: "debug", VolumeMounts: []corev1.VolumeMount{{Name: "kube-api-access-abcde", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount"}}}}},
		},
	}
	changed, err := m.Mutate(context.Background(), pod)
	if err != nil || !changed {
		t.Fatalf("changed=%v error=%v", changed, err)
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == "kube-api-access-abcde" || hasServiceAccountTokenProjection(volume) {
			t.Fatalf("service-account token volume remains: %#v", volume)
		}
	}
	if len(pod.Spec.Containers[0].VolumeMounts) != 1 || pod.Spec.Containers[0].VolumeMounts[0].Name != "data" || len(pod.Spec.InitContainers[2].VolumeMounts) != 0 || len(pod.Spec.EphemeralContainers[0].VolumeMounts) != 0 {
		t.Fatalf("application mounts were not selectively sanitized: containers=%#v init=%#v ephemeral=%#v", pod.Spec.Containers, pod.Spec.InitContainers, pod.Spec.EphemeralContainers)
	}
	if pod.Annotations[contract.InjectionVersionAnnotation] != "v1alpha2" {
		t.Fatalf("injection version = %q", pod.Annotations[contract.InjectionVersionAnnotation])
	}
	if changed, err := m.Mutate(context.Background(), pod); err != nil || changed {
		t.Fatalf("sanitized reinvocation changed=%v error=%v", changed, err)
	}
}

func TestExplicitServiceAccountProjectionRejected(t *testing.T) {
	m := testMutator(t, metav1.LabelSelector{})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", Annotations: map[string]string{contract.GatewayAnnotation: "egress/private"}}, Spec: corev1.PodSpec{
		Volumes: []corev1.Volume{{Name: "explicit-token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "credential"}}}}}}},
	}}
	_, err := m.Mutate(context.Background(), pod)
	rejection, ok := err.(*Rejection)
	if !ok || rejection.Reason != waystatus.ReasonApplicationCredentialsForbidden {
		t.Fatalf("error = %#v", err)
	}
}

func TestPortForwardFileMountTargetsOnlyExplicitApplicationContainer(t *testing.T) {
	m := testMutator(t, metav1.LabelSelector{})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", Annotations: map[string]string{contract.GatewayAnnotation: "egress/private", contract.PortForwardContainerAnnotation: "torrent"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "metrics", Image: "metrics"}, {Name: "torrent", Image: "torrent"}}}}
	changed, err := m.Mutate(context.Background(), pod)
	if err != nil || !changed {
		t.Fatalf("changed=%v error=%v", changed, err)
	}
	if len(pod.Spec.Containers[0].VolumeMounts) != 0 {
		t.Fatalf("unselected container mounts = %#v", pod.Spec.Containers[0].VolumeMounts)
	}
	selected := pod.Spec.Containers[1]
	if len(selected.VolumeMounts) != 1 || selected.VolumeMounts[0].Name != contract.PortForwardVolume || selected.VolumeMounts[0].MountPath != contract.ApplicationLeaseMountPath || !selected.VolumeMounts[0].ReadOnly {
		t.Fatalf("selected container mounts = %#v", selected.VolumeMounts)
	}
	if selected.SecurityContext != nil {
		t.Fatalf("application security context was modified: %#v", selected.SecurityContext)
	}
	var deliveryVolume *corev1.ConfigMapVolumeSource
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == contract.PortForwardVolume {
			deliveryVolume = pod.Spec.Volumes[i].ConfigMap
		}
	}
	if deliveryVolume == nil || deliveryVolume.Name != contract.AllocationConfigMapName("apps", "app") || deliveryVolume.Optional == nil || *deliveryVolume.Optional || !reflect.DeepEqual(deliveryVolume.Items, []corev1.KeyToPath{{Key: contract.PortForwardLeasesKey, Path: contract.PortForwardLeasesKey}}) {
		t.Fatalf("filtered delivery volume = %#v", deliveryVolume)
	}
	if changed, err := m.Mutate(context.Background(), pod); err != nil || changed {
		t.Fatalf("idempotent mutation changed=%v error=%v", changed, err)
	}
}

func TestPortForwardFileMountRejectsMissingContainer(t *testing.T) {
	m := testMutator(t, metav1.LabelSelector{})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", Annotations: map[string]string{contract.GatewayAnnotation: "egress/private", contract.PortForwardContainerAnnotation: "missing"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app"}}}}
	_, err := m.Mutate(context.Background(), pod)
	if rejection, ok := err.(*Rejection); !ok || rejection.Reason != waystatus.ReasonAdmissionVersionConflict {
		t.Fatalf("error = %#v", err)
	}
}

func TestTrustedWorkloadAdapterSelectionIsExactAndIdempotent(t *testing.T) {
	m := testMutator(t, metav1.LabelSelector{})
	image := "registry.example/qbittorrent-adapter@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	trusted := &wayv1.WorkloadAdapter{ObjectMeta: metav1.ObjectMeta{Name: "qbittorrent"}, Spec: wayv1.WorkloadAdapterSpec{ProtocolVersion: contract.AdapterProtocolVersion, Image: image}}
	if err := m.Client.Create(context.Background(), trusted); err != nil {
		t.Fatal(err)
	}
	no := false
	yes := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", Annotations: map[string]string{
		contract.GatewayAnnotation:          "egress/private",
		contract.WorkloadAdapterAnnotation:  "qbittorrent",
		contract.AdapterContainerAnnotation: "adapter",
	}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app", Image: "app",
	}, {
		Name:            "adapter",
		Image:           image,
		ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"adapter", "probe"}}}},
		SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, RunAsNonRoot: &yes, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
	}}}}
	changed, err := m.Mutate(context.Background(), pod)
	if err != nil || !changed {
		t.Fatalf("changed=%v error=%v", changed, err)
	}
	adapter := pod.Spec.Containers[1]
	want := map[string]string{contract.AdapterProtocolEnv: contract.AdapterProtocolVersion, contract.AdapterLeaseEndpointEnv: "http://127.0.0.1:9809/v1/port-forward/leases"}
	for _, env := range adapter.Env {
		delete(want, env.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing admission-owned adapter environment: %v", want)
	}
	if changed, err := m.Mutate(context.Background(), pod); err != nil || changed {
		t.Fatalf("reinvocation changed=%v error=%v", changed, err)
	}
}

func TestWorkloadAdapterRejectsUntrustedImageAndPrivilege(t *testing.T) {
	m := testMutator(t, metav1.LabelSelector{})
	trustedImage := "registry.example/adapter@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	trusted := &wayv1.WorkloadAdapter{ObjectMeta: metav1.ObjectMeta{Name: "sample"}, Spec: wayv1.WorkloadAdapterSpec{ProtocolVersion: contract.AdapterProtocolVersion, Image: trustedImage}}
	if err := m.Client.Create(context.Background(), trusted); err != nil {
		t.Fatal(err)
	}
	no := false
	yes := true
	base := corev1.Container{Name: "adapter", Image: trustedImage, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"probe"}}}}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, RunAsNonRoot: &yes, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}}
	newPod := func(container corev1.Container) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "apps", Annotations: map[string]string{contract.GatewayAnnotation: "egress/private", contract.WorkloadAdapterAnnotation: "sample", contract.AdapterContainerAnnotation: "adapter"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{container}}}
	}
	untrusted := base.DeepCopy()
	untrusted.Image = "registry.example/adapter@sha256:3333333333333333333333333333333333333333333333333333333333333333"
	if _, err := m.Mutate(context.Background(), newPod(*untrusted)); err == nil {
		t.Fatal("untrusted adapter digest was accepted")
	} else if rejection, ok := err.(*Rejection); !ok || rejection.Reason != "UntrustedAdapterImage" {
		t.Fatalf("untrusted error = %#v", err)
	}
	privileged := base.DeepCopy()
	privileged.SecurityContext.Capabilities.Add = []corev1.Capability{"NET_ADMIN"}
	if _, err := m.Mutate(context.Background(), newPod(*privileged)); err == nil {
		t.Fatal("privileged adapter was accepted")
	} else if rejection, ok := err.(*Rejection); !ok || rejection.Reason != "UnsafeWorkloadAdapter" {
		t.Fatalf("privileged error = %#v", err)
	}
}
