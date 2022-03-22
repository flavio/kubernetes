package kubewarden

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	//"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission/plugin/webhook"

	"github.com/pkg/errors"
	"github.com/wapc/wapc-go"
	"github.com/wapc/wapc-go/engines/wazero"
)

func wapcHostCall(ctx context.Context, binding, namespace, operation string, payload []byte) ([]byte, error) {
	// Route the payload to any custom functionality accordingly.
	// You can even route to other waPC modules!!!
	switch namespace {
	case "foo":
		switch operation {
		case "echo":
			return payload, nil // echo
		}
	}
	return []byte("default"), nil
}

type Policy struct {
	Name string
	Spec *PolicySpec
	Hook webhook.WebhookAccessor

	module   wapc.Module
	instance wapc.Instance
}

func NewPolicy(ctx context.Context, name string, spec *PolicySpec, downloadDir string) (*Policy, error) {
	wasmFile, err := DownloadWasmFromRegistry(ctx, spec.Module, downloadDir)
	if err != nil {
		return nil, err
	}

	wasmData, err := os.ReadFile(wasmFile)
	if err != nil {
		return nil, err
	}

	engine := wazero.Engine()
	module, err := engine.New(wasmData, wapcHostCall)
	if err != nil {
		return nil, err
	}
	module.SetLogger(wapc.Println) // Send __console_log calls to stardard out
	module.SetWriter(wapc.Print)   // Send WASI fd_write calls to stardard out
	// TODO: find when to invoke that
	//defer module.Close()

	instance, err := module.Instantiate()
	if err != nil {
		return nil, err
	}
	// TODO: find when to invoke that
	//defer instance.Close()

	policy := Policy{
		Name:     name,
		Spec:     spec,
		Hook:     validatingWebhookAccessor(name, spec),
		module:   module,
		instance: instance,
	}

	return &policy, nil
}

func validatingWebhookAccessor(name string, spec *PolicySpec) webhook.WebhookAccessor {
	webhookConfiguration := v1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Webhooks: []v1.ValidatingWebhook{
			{
				Name:                    name,
				NamespaceSelector:       spec.NamespaceSelector,
				ObjectSelector:          spec.ObjectSelector,
				MatchPolicy:             spec.MatchPolicy,
				AdmissionReviewVersions: []string{"v1", "v1beta"},
				Rules:                   spec.Rules,
			},
		},
	}

	return webhook.NewValidatingWebhookAccessor(
		fmt.Sprintf("UID-%s", name),
		fmt.Sprintf("name-%s", name),
		&webhookConfiguration.Webhooks[0])
}

func (p *Policy) Validate(ctx context.Context, req []byte) (ValidationResponse, error) {
	data, err := p.instance.Invoke(ctx, "validate", req)
	if err != nil {
		err = errors.Wrapf(
			err,
			"[policy %s] - cannot decode validation response: %s",
			p.Name,
			string(data))
		return ValidationResponse{}, err
	}
	return NewValidationResponse(data)
}

func (p *Policy) ValidateSettings(ctx context.Context) (SettingsValidationResponse, error) {
	settings, err := json.Marshal(p.Spec.Settings)
	if err != nil {
		err = errors.Wrapf(err, "[policy %s] - cannot convert settings to JSON", p.Name)

		return SettingsValidationResponse{}, err
	}

	data, err := p.instance.Invoke(ctx, "validate_settings", settings)
	if err != nil {
		err = errors.Wrapf(err, "[policy %s] - settings validation failure: %s", p.Name)
		return SettingsValidationResponse{}, err
	}
	return NewSettingsValidationResponse(data)
}
