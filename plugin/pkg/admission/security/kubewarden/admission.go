/*
Copyright 2021 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kubewarden

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"

	// install conversions for types we need to convert
	_ "k8s.io/kubernetes/pkg/apis/apps/install"
	_ "k8s.io/kubernetes/pkg/apis/batch/install"
	_ "k8s.io/kubernetes/pkg/apis/core/install"

	admissionv1 "k8s.io/api/admission/v1"
	admissionv1beta1 "k8s.io/api/admission/v1beta1"
	"k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/configuration"
	"k8s.io/apiserver/pkg/admission/plugin/webhook"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/generic"
	webhookrequest "k8s.io/apiserver/pkg/admission/plugin/webhook/request"
	webhookutil "k8s.io/apiserver/pkg/util/webhook"

	"github.com/wapc/wapc-go"
)

// PluginName is a string with the name of the plugin
const PluginName = "Kubewarden"

// Register registers a plugin
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(reader io.Reader) (admission.Interface, error) {
		return newPlugin(reader)
	})
}

// Plugin holds state for and implements the admission plugin.
type Plugin struct {
	*generic.Webhook

	module   *wapc.Module
	instance *wapc.Instance
}

var _ admission.ValidationInterface = &Plugin{}

func hostCall(ctx context.Context, binding, namespace, operation string, payload []byte) ([]byte, error) {
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

func consoleLog(msg string) {
	fmt.Println(msg)
}

type fakeDispatcher struct{}

func (f *fakeDispatcher) Dispatch(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces, hooks []webhook.WebhookAccessor) error {
	return nil
}

func newFakeDispatcher(cm *webhookutil.ClientManager) generic.Dispatcher {
	return &fakeDispatcher{}
}

type kubewardenValidationRequestV1 struct {
	Request *admissionv1.AdmissionRequest `json:"request"`
	// TODO: make settings generic
	Settings map[string]string `json:"settings"`
}

type kubewardenValidationRequestV1beta1 struct {
	Request *admissionv1beta1.AdmissionRequest `json:"request"`
	// TODO: make settings generic
	Settings map[string]string `json:"settings"`
}

// newPlugin creates a new admission plugin.
func newPlugin(reader io.Reader) (*Plugin, error) {
	if reader == nil {
		// no reader specified - use default config
	} else {
		settings, err := newSettings(reader)
		if err != nil {
			fmt.Printf("Kubewarden - error processing config |%+v|\n", err)
		} else {
			fmt.Printf("Kubewarden - got config |%+v|\n", settings)
		}
	}

	code, err := ioutil.ReadFile("/hello.wasm")
	if err != nil {
		return nil, err
	}
	module, err := wapc.New(code, hostCall)
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

	handler := admission.NewHandler(admission.Connect, admission.Create, admission.Delete, admission.Update)
	p := &Plugin{
		module:   module,
		instance: instance,
	}

	p.Webhook, err = generic.NewWebhook(handler, nil, configuration.NewValidatingWebhookConfigurationManager, newFakeDispatcher)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Plugin) Validate(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) error {
	gr := a.GetResource().GroupResource()
	if gr.Resource == "pods" {
		fmt.Printf("KUBEWARDEN: admission attributes %+v\n", a)
	} else {
		return nil
	}

	allScopes := v1.AllScopes
	equivalentMatch := v1.Equivalent

	webhookConfiguration := &v1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "webhook1"},
		Webhooks: []v1.ValidatingWebhook{
			{
				Name:              "webhook1.1",
				NamespaceSelector: &metav1.LabelSelector{},
				ObjectSelector:    &metav1.LabelSelector{},
				MatchPolicy:       &equivalentMatch,
				// TODO: this doesn't look like something to be hard coded...
				// we should find a way to generate that
				AdmissionReviewVersions: []string{"v1", "v1beta"},
				Rules: []v1.RuleWithOperations{
					{
						Operations: []v1.OperationType{
							v1.Create,
							v1.Update,
						},
						Rule: v1.Rule{
							APIGroups:   []string{""},
							APIVersions: []string{"v1"},
							Resources:   []string{"pods"},
							Scope:       &allScopes,
						},
					},
				},
			},
		},
	}
	hooks := []webhook.WebhookAccessor{}
	for i := range webhookConfiguration.Webhooks {
		uid := fmt.Sprintf("testUID%d", i)
		name := fmt.Sprintf("testName%d", i)
		hooks = append(hooks, webhook.NewValidatingWebhookAccessor(
			uid,
			name,
			&webhookConfiguration.Webhooks[i]))
	}

	var relevantHooks []*generic.WebhookInvocation
	// Construct all the versions we need to call our webhooks
	versionedAttrs := map[schema.GroupVersionKind]*generic.VersionedAttributes{}
	for _, hook := range hooks {
		fmt.Printf("KUBEWARDEN: check for relevance of hook %+v\n", hook)
		invocation, statusError := p.Webhook.ShouldCallHook(hook, a, o)
		if statusError != nil {
			return statusError
		}
		if invocation == nil {
			fmt.Println("KUBEWARDEN: invocation is nil")
		} else {
			fmt.Println("KUBEWARDEN: invocation is NOT nil")
		}
		if invocation == nil {
			continue
		}
		relevantHooks = append(relevantHooks, invocation)
		// If we already have this version, continue
		if _, ok := versionedAttrs[invocation.Kind]; ok {
			continue
		}
		versionedAttr, err := generic.NewVersionedAttributes(a, invocation.Kind, o)
		if err != nil {
			return apierrors.NewInternalError(err)
		}
		versionedAttrs[invocation.Kind] = versionedAttr
	}

	if len(relevantHooks) == 0 {
		// no matching hooks
		fmt.Println("KUBEWARDEN: no relevant hooks")
		return nil
	}

	for i := range relevantHooks {
		fmt.Println("KUBEWARDEN: about to evaluate something")
		invocation := relevantHooks[i]
		versionedAttr := versionedAttrs[invocation.Kind]
		_, request, _, err := webhookrequest.CreateAdmissionObjects(versionedAttr, invocation)
		if err != nil {
			fmt.Printf("KUBEWARDEN: something went wrong creating admission req %+v\n", err)
			return apierrors.NewInternalError(err)
		}

		var kwValidationRequestJson []byte

		switch t := request.(type) {
		case *admissionv1.AdmissionReview:
			kwValidationRequest := kubewardenValidationRequestV1{
				Request:  t.Request,
				Settings: make(map[string]string),
			}
			kwValidationRequestJson, err = json.Marshal(kwValidationRequest)
		case *admissionv1beta1.AdmissionReview:
			kwValidationRequest := kubewardenValidationRequestV1beta1{
				Request:  t.Request,
				Settings: make(map[string]string),
			}
			kwValidationRequestJson, err = json.Marshal(kwValidationRequest)
		default:
			err = fmt.Errorf("Unknonw admission review type: %+v", request)
		}

		if err != nil {
			fmt.Printf("KUBEWARDEN: something went wrong with serialization %+v\n", err)
			return fmt.Errorf("Cannot serialize kubewarden ValidationRequest: %w", err)
		}
		fmt.Printf("KUBEWARDEN: kubewarden JSON req: |%s|\n", string(kwValidationRequestJson))

		result, err := p.instance.Invoke(ctx, "validate", kwValidationRequestJson)
		if err != nil {
			return fmt.Errorf("Cannot invoke Wasm policy: %w", err)
		}

		fmt.Printf("KUBEWARDEN: wasm eval is |%s|\n", string(result))
	}

	return nil
}
