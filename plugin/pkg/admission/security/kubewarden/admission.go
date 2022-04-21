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

	// install conversions for types we need to convert
	_ "k8s.io/kubernetes/pkg/apis/apps/install"
	_ "k8s.io/kubernetes/pkg/apis/batch/install"
	_ "k8s.io/kubernetes/pkg/apis/core/install"

	admissionv1 "k8s.io/api/admission/v1"
	admissionv1beta1 "k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/configuration"
	"k8s.io/apiserver/pkg/admission/plugin/webhook"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/generic"
	webhookrequest "k8s.io/apiserver/pkg/admission/plugin/webhook/request"
	webhookutil "k8s.io/apiserver/pkg/util/webhook"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	covev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	"github.com/pkg/errors"
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

	policies []*Policy
	client   kubernetes.Interface
	recorder record.EventRecorder
}

// SetExternalKubeClientSet sets the client for the plugin
func (p *Plugin) SetExternalKubeClientSet(cl kubernetes.Interface) {
	p.client = cl

	// configure the event recorder, this requires the client that has just been given
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartStructuredLogging(0)
	eventBroadcaster.StartRecordingToSink(&covev1client.EventSinkImpl{Interface: p.client.CoreV1().Events("")})
	p.recorder = eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "kubewarden-embedded"})
}

var _ admission.ValidationInterface = &Plugin{}

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
	Request  *admissionv1.AdmissionRequest `json:"request"`
	Settings runtime.RawExtension          `json:"settings"`
}

type kubewardenValidationRequestV1beta1 struct {
	Request  *admissionv1beta1.AdmissionRequest `json:"request"`
	Settings runtime.RawExtension               `json:"settings"`
}

// newPlugin creates a new admission plugin.
func newPlugin(reader io.Reader) (*Plugin, error) {
	policies := []*Policy{}

	if reader != nil {
		settings, err := newSettings(reader)
		if err != nil {
			return nil, err
		}
		ctx := context.Background()

		for policyName, policySpec := range settings.Policies {
			policy, err := NewPolicy(ctx, policyName, &policySpec, settings.PoliciesDownloadDir)
			if err != nil {
				return nil, errors.Wrapf(err, "Cannot init Wasm policy %s", policyName)
			}
			policies = append(policies, policy)
		}

	}

	handler := admission.NewHandler(admission.Connect, admission.Create, admission.Delete, admission.Update)
	p := &Plugin{
		policies: policies,
	}

	var err error
	p.Webhook, err = generic.NewWebhook(handler, nil, configuration.NewValidatingWebhookConfigurationManager, newFakeDispatcher)
	if err != nil {
		return nil, err
	}

	return p, nil
}

// ValidateInitialization ensures an authorizer is set.
func (p *Plugin) ValidateInitialization() error {
	validationErrors := []error{}
	ctx := context.Background()

	if p.client == nil {
		return fmt.Errorf("missing client")
	}

	for _, policy := range p.policies {
		vsr, err := policy.ValidateSettings(ctx)
		if err != nil {
			validationErrors = append(validationErrors, err)
		} else if !vsr.Valid {
			validationErrors = append(validationErrors, fmt.Errorf("Settings for policy %s are not valid: %s", policy.Name, vsr.Message))
		}
	}

	if len(validationErrors) > 0 {
		klog.Errorf("KUBEWARDEN: %v", validationErrors)
		return errors.New("KUBEWARDEN: policies configuration is wrong")
	}
	return nil
}

type relevantPolicy struct {
	invocation *generic.WebhookInvocation
	policy     *Policy
}

func (p *Plugin) Validate(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) error {
	var relevantPolicies []relevantPolicy
	// Construct all the versions we need to call our webhooks
	versionedAttrs := map[schema.GroupVersionKind]*generic.VersionedAttributes{}
	for _, policy := range p.policies {
		invocation, statusError := p.Webhook.ShouldCallHook(policy.Hook, a, o)
		if statusError != nil {
			return statusError
		}
		if invocation == nil {
			continue
		}
		relevantPolicies = append(relevantPolicies, relevantPolicy{
			invocation: invocation,
			policy:     policy,
		})
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

	if len(relevantPolicies) == 0 {
		// no matching hooks
		return nil
	}

	for i := range relevantPolicies {
		relevantPolicy := relevantPolicies[i]
		invocation := relevantPolicy.invocation
		policy := relevantPolicy.policy

		versionedAttr := versionedAttrs[invocation.Kind]
		_, request, _, err := webhookrequest.CreateAdmissionObjects(versionedAttr, invocation)
		if err != nil {
			klog.Warningf("KUBEWARDEN: something went wrong creating admission req %+v\n", err)
			return apierrors.NewInternalError(err)
		}

		var kwValidationRequestJson []byte
		var objKind string
		var objApiVersion string

		switch t := request.(type) {
		case *admissionv1.AdmissionReview:
			objKind = t.Request.Kind.Kind
			objApiVersion = fmt.Sprintf("%s/%s", t.Request.Kind.Group, t.Request.Kind.Version)

			kwValidationRequest := kubewardenValidationRequestV1{
				Request:  t.Request,
				Settings: policy.Spec.Settings,
			}
			kwValidationRequestJson, err = json.Marshal(kwValidationRequest)
		case *admissionv1beta1.AdmissionReview:
			objKind = t.Request.Kind.Kind
			objApiVersion = fmt.Sprintf("%s/%s", t.Request.Kind.Group, t.Request.Kind.Version)

			kwValidationRequest := kubewardenValidationRequestV1beta1{
				Request:  t.Request,
				Settings: policy.Spec.Settings,
			}
			kwValidationRequestJson, err = json.Marshal(kwValidationRequest)
		default:
			err = fmt.Errorf("Unknonw admission review type: %+v", request)
		}

		if err != nil {
			err = errors.Wrap(err, "Cannot serialize kubewarden ValidationRequest")
			return rejectAndLog(a, err)
		}

		vr, err := policy.Validate(ctx, kwValidationRequestJson)
		if err != nil {
			err = errors.Wrapf(err, "Error evaluating Wasm policy %s", policy.Name)
			return rejectAndLog(a, err)
		} else {
			if !vr.Accepted {
				err := fmt.Errorf("Kubewarden policy %s rejection: %s",
					policy.Name,
					vr.Message)

				reportObj := map[string]interface{}{
					"apiVersion": objApiVersion,
					"kind":       objKind,
				}

				u := &unstructured.Unstructured{Object: reportObj}
				p.recorder.Eventf(u, corev1.EventTypeWarning, "ValidationRejection", "%s: %s", policy.Name, vr.Message)

				return rejectAndLog(a, err)
			}
		}
	}

	return nil
}

func rejectAndLog(a admission.Attributes, err error) error {
	klog.Errorf("KUBEWARDEN: %v", err)
	return admission.NewForbidden(a, err)
}
