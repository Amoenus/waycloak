package admission

import (
	"context"
	"encoding/json"

	"github.com/Amoenus/waycloak/internal/contract"
	waystatus "github.com/Amoenus/waycloak/internal/status"
	corev1 "k8s.io/api/core/v1"
	cradmission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

type PodValidator struct{ AgentImage string }

func (v *PodValidator) Handle(_ context.Context, req cradmission.Request) cradmission.Response {
	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		return cradmission.Errored(400, err)
	}
	if pod.Annotations[contract.GatewayAnnotation] == "" {
		return cradmission.Allowed("unannotated Pod")
	}
	if pod.Annotations[contract.InjectionVersionAnnotation] != contract.InjectionVersion {
		return cradmission.Denied(waystatus.ReasonAdmissionVersionConflict + ": annotated Pod is not injected with the required version")
	}
	if err := validateInjected(&pod, contract.AllocationConfigMapName(pod.Namespace, pod.Name), v.AgentImage); err != nil {
		return cradmission.Denied(waystatus.ReasonAdmissionVersionConflict + ": " + err.Error())
	}
	return cradmission.Allowed("injection contract is valid")
}
